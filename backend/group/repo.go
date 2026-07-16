package group

import (
	"context"
	"errors"
	"fmt"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrGroupNotFound = errors.New("group: 不存在")
	ErrGroupIsSystem = errors.New("group: 系统用户组不可删除")
)

// Repo 业务层访问 user_groups / user_group_memberships 的入口。
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo 构造用户组仓库。
func NewRepo(q *sqlcgen.Queries) *Repo { return &Repo{q: q} }

// ---------- user_groups CRUD ----------

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
func (r *Repo) CreateGroup(ctx context.Context, name, description string, quotaProfileID int64, priority int32) (sqlcgen.UserGroup, error) {
	return r.q.CreateUserGroup(ctx, sqlcgen.CreateUserGroupParams{
		Name:           name,
		IsSystem:       false,
		Description:    strToText(description),
		QuotaProfileID: quotaProfileID,
		Priority:       priority,
	})
}

// UpdateGroupQuotaProfile 修改用户组配额方案。每个用户组始终绑定一个方案。
func (r *Repo) UpdateGroupQuotaProfile(ctx context.Context, groupID, quotaProfileID int64) error {
	if _, err := r.GetByID(ctx, groupID); err != nil {
		return err
	}
	if _, err := r.q.UpdateUserGroupQuotaProfile(ctx, sqlcgen.UpdateUserGroupQuotaProfileParams{
		ID:             groupID,
		QuotaProfileID: quotaProfileID,
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
// ON DELETE CASCADE 会清掉成员关系与用户组权限。
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

// ListGroupIDsByUser 列出 user 的所有 group id。
func (r *Repo) ListGroupIDsByUser(ctx context.Context, userID int64) ([]int64, error) {
	return r.q.ListGroupIDsByUser(ctx, userID)
}

// ---------- 工具函数 ----------

func strToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
