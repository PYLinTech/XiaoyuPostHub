package group

import (
	"context"
	"errors"
	"fmt"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RoleReader 是 group 需要的 role 只读能力。
// 由 role.Repo 实现；group.Repo 通过它做 assignable 校验，
// 避免 anonymous 这类 assignable=false 的 role 被绑给用户组。
type RoleReader interface {
	GetByID(ctx context.Context, id int64) (sqlcgen.Role, error)
}

var (
	ErrGroupNotFound     = errors.New("group: 不存在")
	ErrGroupIsSystem     = errors.New("group: 系统用户组不可删除")
	ErrRoleNotAssignable = errors.New("group: role 不允许绑定给用户组")
	ErrRoleReaderMissing = errors.New("group: role reader 未初始化")
)

// Repo 业务层访问 user_groups / user_group_memberships / group_roles 表的入口。
type Repo struct {
	q     *sqlcgen.Queries
	roles RoleReader
}

// NewRepo 构造 Repo。roles 用于 AssignRoleToGroup 内部校验 assignable。
//
// 启动期装配：roles == nil 直接 panic。这不是业务输入错误，是 main.go
// 漏注 roleRepo 的硬错——越早炸越好。业务运行期不允许走 NewRepo。
func NewRepo(q *sqlcgen.Queries, roles RoleReader) *Repo {
	if roles == nil {
		panic("group: NewRepo 第二个参数 role reader 是 nil；main.go 初始化时漏注 roleRepo 是装配错误")
	}
	return &Repo{q: q, roles: roles}
}

// ---------- user_groups CRUD ----------

func (r *Repo) GetByName(ctx context.Context, name string) (sqlcgen.UserGroup, error) {
	g, err := r.q.GetUserGroupByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.UserGroup{}, fmt.Errorf("%w: %s", ErrGroupNotFound, name)
	}
	return g, err
}

func (r *Repo) GetByID(ctx context.Context, id int64) (sqlcgen.UserGroup, error) {
	g, err := r.q.GetUserGroupByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.UserGroup{}, fmt.Errorf("%w: id=%d", ErrGroupNotFound, id)
	}
	return g, err
}

func (r *Repo) List(ctx context.Context) ([]sqlcgen.UserGroup, error) {
	return r.q.ListUserGroups(ctx)
}

// CreateGroup 业务层创建用户组。
//   - is_system 永远 false（系统 group 由 bootstrap 创建）
//   - quotaProfileID 可为 nil（表示不绑 quota）
func (r *Repo) CreateGroup(ctx context.Context, name, description string, quotaProfileID *int64, priority int32) (sqlcgen.UserGroup, error) {
	return r.q.CreateUserGroup(ctx, sqlcgen.CreateUserGroupParams{
		Name:           name,
		IsSystem:       false,
		Description:    strToText(description),
		QuotaProfileID: int64PtrToPgtype(quotaProfileID),
		Priority:       priority,
	})
}

// UpdateGroupDescription 改 description。
// **允许**改系统 group 的 description（与 name/row 不可改的语义解耦）。
func (r *Repo) UpdateGroupDescription(ctx context.Context, groupID int64, description string) error {
	if _, err := r.GetByID(ctx, groupID); err != nil {
		return err
	}
	if _, err := r.q.UpdateUserGroupDescription(ctx, sqlcgen.UpdateUserGroupDescriptionParams{
		ID:          groupID,
		Description: strToText(description),
	}); err != nil {
		return err
	}
	return nil
}

// UpdateGroupQuotaProfile 改 quota 绑定（系统 group 也允许，可传 nil 解绑）。
func (r *Repo) UpdateGroupQuotaProfile(ctx context.Context, groupID int64, quotaProfileID *int64) error {
	if _, err := r.GetByID(ctx, groupID); err != nil {
		return err
	}
	if _, err := r.q.UpdateUserGroupQuotaProfile(ctx, sqlcgen.UpdateUserGroupQuotaProfileParams{
		ID:             groupID,
		QuotaProfileID: int64PtrToPgtype(quotaProfileID),
	}); err != nil {
		return err
	}
	return nil
}

// UpdateGroupPriority 改 priority（系统 group 也允许）。
func (r *Repo) UpdateGroupPriority(ctx context.Context, groupID int64, priority int32) error {
	if _, err := r.GetByID(ctx, groupID); err != nil {
		return err
	}
	if _, err := r.q.UpdateUserGroupPriority(ctx, sqlcgen.UpdateUserGroupPriorityParams{
		ID:       groupID,
		Priority: priority,
	}); err != nil {
		return err
	}
	return nil
}

// DeleteGroup 删 group（仅非系统 group）。
// ON DELETE CASCADE 会清掉 user_group_memberships 与 group_roles 关联。
func (r *Repo) DeleteGroup(ctx context.Context, groupID int64) error {
	g, err := r.GetByID(ctx, groupID)
	if err != nil {
		return err
	}
	if g.IsSystem {
		return ErrGroupIsSystem
	}
	if _, err := r.q.DeleteUserGroup(ctx, groupID); err != nil {
		return err
	}
	return nil
}

// ---------- user ↔ group 关联 ----------

// AssignUserToGroup 给 user 分配 group（幂等）。
func (r *Repo) AssignUserToGroup(ctx context.Context, userID, groupID int64) error {
	_, err := r.q.AssignUserToGroup(ctx, sqlcgen.AssignUserToGroupParams{
		UserID:  userID,
		GroupID: groupID,
	})
	return err
}

// UnassignUserFromGroup 取消 user 的 group 分配。
func (r *Repo) UnassignUserFromGroup(ctx context.Context, userID, groupID int64) error {
	_, err := r.q.UnassignUserFromGroup(ctx, sqlcgen.UnassignUserFromGroupParams{
		UserID:  userID,
		GroupID: groupID,
	})
	return err
}

// ListGroupIDsByUser 列出 user 的所有 group id。
func (r *Repo) ListGroupIDsByUser(ctx context.Context, userID int64) ([]int64, error) {
	return r.q.ListGroupIDsByUser(ctx, userID)
}

// ---------- group ↔ role 关联 ----------

// AssignRoleToGroup 给 group 绑 role（幂等）。
//
// 结构性保证 assignable=false 的 role（典型：anonymous）**不会**进入 group_roles：
//   - 必须传 RoleReader（构造时 NewRepo 强制要求）
//   - 内部查 role，assignable=false 直接返回 ErrRoleNotAssignable
//   - 调用方**不要**在外部提前过滤 role——避免上层某天漏过滤把 anonymous 塞进来
func (r *Repo) AssignRoleToGroup(ctx context.Context, groupID, roleID int64) error {
	if r.roles == nil {
		return ErrRoleReaderMissing
	}
	roleRow, err := r.roles.GetByID(ctx, roleID)
	if err != nil {
		return err
	}
	if !roleRow.Assignable {
		return ErrRoleNotAssignable
	}
	_, err = r.q.AssignRoleToGroup(ctx, sqlcgen.AssignRoleToGroupParams{
		GroupID: groupID,
		RoleID:  roleID,
	})
	return err
}

// UnassignRoleFromGroup 解绑 group 的 role。
func (r *Repo) UnassignRoleFromGroup(ctx context.Context, groupID, roleID int64) error {
	_, err := r.q.UnassignRoleFromGroup(ctx, sqlcgen.UnassignRoleFromGroupParams{
		GroupID: groupID,
		RoleID:  roleID,
	})
	return err
}

// ListRoleIDsByGroup 列出 group 的所有 role id。
func (r *Repo) ListRoleIDsByGroup(ctx context.Context, groupID int64) ([]int64, error) {
	return r.q.ListRoleIDsByGroup(ctx, groupID)
}

// ---------- 工具函数 ----------

func strToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func int64PtrToPgtype(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *p, Valid: true}
}
