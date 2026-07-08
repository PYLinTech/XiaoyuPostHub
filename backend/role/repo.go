package role

import (
	"context"
	"errors"
	"fmt"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// ReservedRoleName 是绝对不能出现在 roles 表里的 role name。
// 真超管身份由 .env 单点决定；任何把 'super_admin' 写入 roles 表的操作
// 都被视为 bug，启动期会拒绝、运行时 Repo 会拒绝。
const ReservedRoleName = "super_admin"

var (
	ErrReservedRoleName    = errors.New("role: 保留名称，禁止写入数据库")
	ErrRoleNotFound        = errors.New("role: 不存在")
	ErrRoleIsSystem        = errors.New("role: 系统 role 不可删除")
	ErrRoleNotAssignable   = errors.New("role: 此 role 不允许分配给用户或用户组")
)

// Repo 业务层访问 roles / role_permissions / user_roles 表的入口。
type Repo struct {
	q *sqlcgen.Queries
}

func NewRepo(q *sqlcgen.Queries) *Repo { return &Repo{q: q} }

// ---------- role CRUD（不含 seed 流程） ----------

func (r *Repo) GetByName(ctx context.Context, name string) (sqlcgen.Role, error) {
	role, err := r.q.GetRoleByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.Role{}, fmt.Errorf("%w: %s", ErrRoleNotFound, name)
	}
	return role, err
}

func (r *Repo) GetByID(ctx context.Context, id int64) (sqlcgen.Role, error) {
	role, err := r.q.GetRoleByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.Role{}, fmt.Errorf("%w: id=%d", ErrRoleNotFound, id)
	}
	return role, err
}

func (r *Repo) List(ctx context.Context) ([]sqlcgen.Role, error) {
	return r.q.ListRoles(ctx)
}

// CreateRole 业务层创建 role（**不**用于 seed 启动期）。
//   - 拒绝 name = 'super_admin'
//   - 业务层需要先校验调用方有 permission.ManageRoles
//   - 创建的 role 永远是 is_system=false、assignable 取决于调用方
func (r *Repo) CreateRole(ctx context.Context, name, description string, assignable bool) (sqlcgen.Role, error) {
	if name == ReservedRoleName {
		return sqlcgen.Role{}, ErrReservedRoleName
	}
	return r.q.CreateRole(ctx, sqlcgen.CreateRoleParams{
		Name:        name,
		IsSystem:    false,
		Assignable:  assignable,
		Description: strToText(description),
	})
}

// UpdateRoleDescription 改 role 的 description。
// **允许**改系统 role 的 description（与 name/row 不可改的语义解耦）。
// 配置面板需要展示自定义文案，文案改了不影响权限语义。
func (r *Repo) UpdateRoleDescription(ctx context.Context, id int64, description string) error {
	role, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if role.Name == ReservedRoleName {
		return ErrReservedRoleName
	}
	if _, err := r.q.UpdateRoleDescription(ctx, sqlcgen.UpdateRoleDescriptionParams{
		ID:          id,
		Description: strToText(description),
	}); err != nil {
		return err
	}
	return nil
}

// DeleteRole 删 role（仅非系统 role）。
// ON DELETE CASCADE 会自动清掉 role_permissions 与 user_roles 关联。
func (r *Repo) DeleteRole(ctx context.Context, id int64) error {
	role, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if role.IsSystem {
		return ErrRoleIsSystem
	}
	if role.Name == ReservedRoleName {
		return ErrReservedRoleName
	}
	if _, err := r.q.DeleteRole(ctx, id); err != nil {
		return err
	}
	return nil
}

// ---------- role ↔ permission 绑定（**允许**改系统 role） ----------

// ListPermissions 返回该 role 拥有的 permission code 列表（按 code 排序）。
func (r *Repo) ListPermissions(ctx context.Context, roleID int64) ([]string, error) {
	return r.q.ListRolePermissions(ctx, roleID)
}

// GrantPermission 给 role 添加一个 permission（幂等）。
//   - 拒绝未知 permission code（白名单由 permission.All 决定）
//   - **不**限制 is_system：系统 role 的 permission 允许通过配置面板调整
//   - 业务层需要先校验调用方有 permission.ManageRoles
func (r *Repo) GrantPermission(ctx context.Context, roleID int64, code string) error {
	if !permission.IsValid(code) {
		return fmt.Errorf("role: 未知 permission code: %s", code)
	}
	_, err := r.q.AddPermissionToRole(ctx, sqlcgen.AddPermissionToRoleParams{
		RoleID:     roleID,
		Permission: code,
	})
	return err
}

// RevokePermission 从 role 移除一个 permission。
//   - 拒绝未知 permission code（白名单由 permission.All 决定），与 GrantPermission 保持一致
//   - **不**限制 is_system：系统 role 的 permission 允许通过配置面板调整
func (r *Repo) RevokePermission(ctx context.Context, roleID int64, code string) error {
	if !permission.IsValid(code) {
		return fmt.Errorf("role: 未知 permission code: %s", code)
	}
	_, err := r.q.RemovePermissionFromRole(ctx, sqlcgen.RemovePermissionFromRoleParams{
		RoleID:     roleID,
		Permission: code,
	})
	return err
}

// ---------- user ↔ role 关联 ----------

// AssignRoleToUser 给 user 分配 role（幂等）。
//   - 拒绝 assignable=false 的 role（典型：anonymous）
//   - 业务层需要先校验调用方有 permission.ManageUsers / ManageRoles
func (r *Repo) AssignRoleToUser(ctx context.Context, userID, roleID int64) error {
	role, err := r.GetByID(ctx, roleID)
	if err != nil {
		return err
	}
	if !role.Assignable {
		return ErrRoleNotAssignable
	}
	_, err = r.q.AssignRoleToUser(ctx, sqlcgen.AssignRoleToUserParams{
		UserID: userID,
		RoleID: roleID,
	})
	return err
}

// UnassignRoleFromUser 取消 user 的 role 分配。
func (r *Repo) UnassignRoleFromUser(ctx context.Context, userID, roleID int64) error {
	_, err := r.q.UnassignRoleFromUser(ctx, sqlcgen.UnassignRoleFromUserParams{
		UserID: userID,
		RoleID: roleID,
	})
	return err
}

// ListEffectivePermissionsByUser 合并 user_roles + group_roles + override 的最终权限。
// 完整 SQL 在 user_roles.sql 的 ListEffectivePermissionsByUser。
// 业务层直接调这个，单条 SQL 出结果。
func (r *Repo) ListEffectivePermissionsByUser(ctx context.Context, userID int64) ([]string, error) {
	return r.q.ListEffectivePermissionsByUser(ctx, userID)
}

// ListAnonymousPermissions 访客权限（从 anonymous role 的 role_permissions 读）。
// 由中间件在 anonymous 上下文里调用。
func (r *Repo) ListAnonymousPermissions(ctx context.Context) ([]string, error) {
	return r.q.ListAnonymousPermissions(ctx)
}

// ---------- 工具函数 ----------

// strToText 把 Go string 映射成 pgtype.Text：空字符串转 SQL NULL。
func strToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
