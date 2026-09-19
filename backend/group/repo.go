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

// StorageBinding 是用户的有效存储绑定解析结果。
type StorageBinding struct {
	// BackendID 为 0 表示未绑定任何存储后端（调用方使用全局默认后端）。
	BackendID int64
	// Enabled 是绑定后端当前是否可用于写入；BackendID == 0 时无意义。
	Enabled bool
}

// UpdateGroupStorageBackend 修改用户组绑定的存储后端；backendID 为 nil 表示
// 解绑（组内新上传回退全局默认）。后端的存在性与启用状态由调用方（管理端）
// 校验；组不存在时返回 ErrGroupNotFound。不在此处重复查询组是否存在——由
// UPDATE 的影响行数判定，避免与调用方的读取重复。
func (r *Repo) UpdateGroupStorageBackend(ctx context.Context, groupID int64, backendID *int64) error {
	target := pgtype.Int8{}
	if backendID != nil {
		target = pgtype.Int8{Int64: *backendID, Valid: true}
	}
	affected, err := r.q.UpdateUserGroupStorageBackend(ctx, sqlcgen.UpdateUserGroupStorageBackendParams{
		ID:               groupID,
		StorageBackendID: target,
	})
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: id=%d", ErrGroupNotFound, groupID)
	}
	return nil
}

// EffectiveStorageBackend 解析用户的有效存储后端：所属用户组中 priority 最高
// 且已绑定后端的组（与配额方案的选择规则一致）。
//
// 未绑定任何后端时返回零值（BackendID == 0），调用方使用全局默认后端。
// BackendID != 0 且 Enabled == false 表示绑定后端已被停用：调用方应明确拒绝
// 上传，不静默回退——静默改写落盘位置会破坏「组 = 存储」的可预期性，也会让
// 后续按组迁移的范围失真。
func (r *Repo) EffectiveStorageBackend(ctx context.Context, userID int64) (StorageBinding, error) {
	row, err := r.q.GetEffectiveStorageBackendByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StorageBinding{}, nil
	}
	if err != nil {
		return StorageBinding{}, err
	}
	if !row.StorageBackendID.Valid {
		return StorageBinding{}, nil
	}
	return StorageBinding{BackendID: row.StorageBackendID.Int64, Enabled: row.IsEnabled}, nil
}

// ListGroupIDsByUser 列出 user 的所有 group id。
func (r *Repo) ListGroupIDsByUser(ctx context.Context, userID int64) ([]int64, error) {
	return r.q.ListGroupIDsByUser(ctx, userID)
}

// PermissionSetByName 返回指定名称用户组当前被授予的权限集合。
//
// 未登录访客的消费侧权限（preview / download）按 guest 系统用户组读取：
// 该组不接受成员，无法走 ListEffectivePermissionsByUser。组不存在时返回
// ErrGroupNotFound，调用方按"配置不可用"处理（fail-closed）。
func (r *Repo) PermissionSetByName(ctx context.Context, name string) (map[string]bool, error) {
	if _, err := r.q.GetUserGroupByName(ctx, name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: name=%s", ErrGroupNotFound, name)
		}
		return nil, fmt.Errorf("读取用户组 %s 失败：%w", name, err)
	}
	perms, err := r.q.ListPermissionsByGroupName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("读取用户组 %s 权限失败：%w", name, err)
	}
	set := make(map[string]bool, len(perms))
	for _, code := range perms {
		set[code] = true
	}
	return set, nil
}

// ---------- 工具函数 ----------

func strToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
