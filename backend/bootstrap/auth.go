// Package bootstrap 负责启动期一次性健康检查 + 修复系统不变量。
//
// 原则（项目当前阶段）：
//   - **不**新增 bootstrap_state 表（plan 明确要求）—— 状态全部用 SQL 行为表达
//   - 启动期**不**覆盖后台可配置内容（system role 的 description / permission
//     绑定、quota profile 的限额字段、user group 的 description / quota / priority）
//   - 启动期**只修复**系统不变量：
//     · permission upsert + 检测 DB 是否有 Go 未定义的 code
//     · system role 的 is_system / assignable 标志位
//     · system quota / group 的 is_system 标志位
//     · 清理 user_roles / group_roles 中 assignable=false 的脏关系
//
// 并发：
//   - 启动期所有操作在**单事务**里做，事务开头拿 pg_advisory_xact_lock
//     (hashtext('xiaoyu_auth_bootstrap'))，多实例并发部署不会重复初始化。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/PYLinTech/XiaoyuPostHub/backend/role"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey 是 pg_advisory_xact_lock 的 key 字符串。
// 跨进程唯一，go 通过 hashtext 转为 bigint。
const advisoryLockKey = "xiaoyu_auth_bootstrap"

// ErrUnknownPermissionInDB 数据库里有 Go 代码未定义的 permission code。
// 这是强警告：意味着有人绕过代码层加过脏数据，bootstrap **拒绝**继续。
var ErrUnknownPermissionInDB = errors.New("bootstrap: 数据库存在 Go 未定义的 permission")

// AuthCatalog 启动期健康检查 + 不变量修复。
type AuthCatalog struct {
	pool *pgxpool.Pool
}

func NewAuthCatalog(pool *pgxpool.Pool) *AuthCatalog {
	return &AuthCatalog{pool: pool}
}

// Run 在**单事务**里跑完所有启动期检查 + 修复。
// 任何一步失败整体回滚。
//
// 顺序（在同一事务内）：
//  1. 拿 advisory lock 防多实例并发
//  2. permissions：upsert Definitions，校验 DB 没有非法 code
//  3. roles：upsert system role（anonymous / user），修复身份字段（is_system / assignable），
//     首次创建时绑定默认权限
//  4. quota_profiles：upsert default_user，修复 is_system
//  5. user_groups：upsert default_user，修复 is_system，首次创建时绑 default_user quota + user role
//  6. 清理 user_roles / group_roles 中 assignable=false 的脏关系
func (c *AuthCatalog) Run(ctx context.Context) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", advisoryLockKey); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	q := sqlcgen.New(c.pool).WithTx(tx)

	if err := c.seedPermissions(ctx, q); err != nil {
		return fmt.Errorf("seed permissions: %w", err)
	}
	if err := c.seedSystemRoles(ctx, q); err != nil {
		return fmt.Errorf("seed 系统 role: %w", err)
	}
	if err := c.seedDefaultQuotaProfile(ctx, q); err != nil {
		return fmt.Errorf("seed default quota profile: %w", err)
	}
	if err := c.seedDefaultUserGroup(ctx, q); err != nil {
		return fmt.Errorf("seed default user group: %w", err)
	}
	if err := c.cleanupNonAssignableRoleBindings(ctx, q); err != nil {
		return fmt.Errorf("清理 assignable=false 的 role 绑定: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ---------- 1. permissions ----------

// seedPermissions：
//   - 把 Go Definitions 里的 code 全部 upsert（description 同步）
//   - 校验 DB 里**没有** Go 未定义的 code（拒绝继续）
//
// **不**删除 DB 多余的 code——可能是合法的扩展（未来想加新 permission），
// 但当前未在 Go 里声明。报错让运维知道有未授权的 code 漂在 DB。
func (c *AuthCatalog) seedPermissions(ctx context.Context, q *sqlcgen.Queries) error {
	// 1. 校验 DB 是否有 Go 未定义的 code
	dbCodes, err := q.ListPermissionCodes(ctx)
	if err != nil {
		return fmt.Errorf("list DB permissions: %w", err)
	}
	known := make(map[string]bool, len(permission.All))
	for _, code := range permission.All {
		known[code] = true
	}
	for _, code := range dbCodes {
		if !known[code] {
			return fmt.Errorf("%w: code=%q", ErrUnknownPermissionInDB, code)
		}
	}

	// 2. upsert Definitions
	for _, d := range permission.Definitions {
		if err := q.UpsertPermissionDefinition(ctx, sqlcgen.UpsertPermissionDefinitionParams{
			Code:        d.Code,
			Description: d.Description,
		}); err != nil {
			return fmt.Errorf("upsert %q: %w", d.Code, err)
		}
	}
	log.Printf("INFO: upsert %d 个 permission（description 同步）", len(permission.Definitions))
	return nil
}

// ---------- 2. system roles ----------

// systemRoleSpec 系统 role 的种子定义（首次创建时用）。
type systemRoleSpec struct {
	Name               string
	IsSystem           bool
	Assignable         bool
	Description        string
	DefaultPermissions []string // 仅首次创建时写入
}

var systemRoleSpecs = []systemRoleSpec{
	{
		Name:               role.NameAnonymous,
		IsSystem:           true,
		Assignable:         false, // 访客身份不可分配
		Description:        "未登录访客角色；访客权限从数据库 role_permissions 读取，可由权限配置面板修改。",
		DefaultPermissions: []string{permission.Login},
	},
	{
		Name:               role.NameUser,
		IsSystem:           true,
		Assignable:         true,
		Description:        "普通用户基线角色；通常挂载到 default_user 用户组。",
		DefaultPermissions: []string{
			permission.Login, permission.Upload, permission.Download, permission.Preview,
			permission.Rename, permission.DeleteOwn, permission.Share, permission.DirectLink,
		},
	},
}

// seedSystemRoles 对每个系统 role：
//   - 防御性修复 is_system / assignable（不重置 description / permission 绑定）
//   - 首次创建（之前不存在）时：插入并绑定默认权限
//   - 已存在时：只修身份字段；不补、不删、不重置默认权限
func (c *AuthCatalog) seedSystemRoles(ctx context.Context, q *sqlcgen.Queries) error {
	for _, spec := range systemRoleSpecs {
		if spec.Name == role.ReservedRoleName {
			// 防御性：specs 列表里不应含 super_admin
			return fmt.Errorf("systemRoleSpecs 包含 reserved name %q", role.ReservedRoleName)
		}

		// 查询当前状态（行级是否存在）
		existing, err := q.GetRoleByName(ctx, spec.Name)
		exists := true
		if errors.Is(err, pgx.ErrNoRows) {
			exists = false
		} else if err != nil {
			return fmt.Errorf("query role %q: %w", spec.Name, err)
		}

		if !exists {
			// 首次创建：插入 + 写默认权限
			if _, err := q.CreateRole(ctx, sqlcgen.CreateRoleParams{
				Name:        spec.Name,
				IsSystem:    spec.IsSystem,
				Assignable:  spec.Assignable,
				Description: strToText(spec.Description),
			}); err != nil {
				return fmt.Errorf("insert role %q: %w", spec.Name, err)
			}
			// 首次创建时绑定默认权限
			for _, code := range spec.DefaultPermissions {
				if !permission.IsValid(code) {
					return fmt.Errorf("systemRoleSeed %q 引用未知 permission %q", spec.Name, code)
				}
				if _, err := q.InsertSystemPermissionForRoleIfMissing(ctx, sqlcgen.InsertSystemPermissionForRoleIfMissingParams{
					Name:       spec.Name,
					Permission: code,
				}); err != nil {
					return fmt.Errorf("role %q 绑默认 permission %q: %w", spec.Name, code, err)
				}
			}
			log.Printf("INFO: seed 新建系统 role %q + %d 个默认 permission", spec.Name, len(spec.DefaultPermissions))
			continue
		}

		// 已存在：**只**修复 is_system / assignable，**不**改 description，**不**重置 permission
		if existing.IsSystem != spec.IsSystem || existing.Assignable != spec.Assignable {
			if _, err := q.UpdateRoleSystemFlags(ctx, sqlcgen.UpdateRoleSystemFlagsParams{
				Name:      spec.Name,
				IsSystem:  spec.IsSystem,
				Assignable: spec.Assignable,
			}); err != nil {
				return fmt.Errorf("修复 role %q 系统标志失败: %w", spec.Name, err)
			}
			log.Printf("INFO: 修复 role %q 身份字段 (is_system=%v, assignable=%v)",
				spec.Name, spec.IsSystem, spec.Assignable)
		}
	}
	return nil
}

// ---------- 3. default quota profile ----------

// seedDefaultQuotaProfile：
//   - 不存在 → 创建 is_system=true 的 default_user quota
//   - 已存在 → **只**修复 is_system=true，**不**重置 description / 限额字段
func (c *AuthCatalog) seedDefaultQuotaProfile(ctx context.Context, q *sqlcgen.Queries) error {
	_, err := q.GetQuotaProfileByName(ctx, quota.NameDefaultUser)
	if errors.Is(err, pgx.ErrNoRows) {
		// 首次创建：限额字段全部 NULL（不限），is_system=true
		if _, err := q.InsertQuotaProfileIfMissing(ctx, sqlcgen.InsertQuotaProfileIfMissingParams{
			Name:        quota.NameDefaultUser,
			Description: strToText("普通用户默认配额（所有字段 NULL = 不限）"),
		}); err != nil {
			return fmt.Errorf("insert default quota profile: %w", err)
		}
		log.Printf("INFO: seed 新建 default quota profile (%q)", quota.NameDefaultUser)
		return nil
	}
	if err != nil {
		return fmt.Errorf("query default quota profile: %w", err)
	}

	// 已存在：只修 is_system
	if _, err := q.UpdateQuotaProfileSystemFlag(ctx, sqlcgen.UpdateQuotaProfileSystemFlagParams{
		Name:     quota.NameDefaultUser,
		IsSystem: true,
	}); err != nil {
		return fmt.Errorf("修复 default quota profile 系统标志失败: %w", err)
	}
	return nil
}

// ---------- 4. default user group ----------

// seedDefaultUserGroup：
//   - 不存在 → 创建 is_system=true, priority=0, 绑 default_user quota + user role
//   - 已存在 → **只**修复 is_system=true，**不**重置 description / priority / quota_profile_id
//   - **不**补 group_roles（只首次创建时绑 user role）
func (c *AuthCatalog) seedDefaultUserGroup(ctx context.Context, q *sqlcgen.Queries) error {
	// 查 default_user quota（已通过 seedDefaultQuotaProfile 修复）
	quotaRow, err := q.GetQuotaProfileByName(ctx, quota.NameDefaultUser)
	if err != nil {
		return fmt.Errorf("query default quota profile: %w", err)
	}

	_, err = q.GetUserGroupByName(ctx, "default_user")
	if errors.Is(err, pgx.ErrNoRows) {
		// 首次创建
		if _, err := q.InsertSystemUserGroupIfMissing(ctx, sqlcgen.InsertSystemUserGroupIfMissingParams{
			Name:           "default_user",
			Description:    strToText("普通用户默认用户组"),
			QuotaProfileID: pgtype.Int8{Int64: quotaRow.ID, Valid: true},
			Priority:       0,
		}); err != nil {
			return fmt.Errorf("insert default user group: %w", err)
		}
		// 首次创建时绑 user role（配置面板后续可以解绑/调整）
		if _, err := q.InsertDefaultUserGroupRoleBindingIfMissing(ctx, sqlcgen.InsertDefaultUserGroupRoleBindingIfMissingParams{
			Name:   "default_user",
			Name_2: role.NameUser,
		}); err != nil {
			return fmt.Errorf("default_user group 绑 user role: %w", err)
		}
		log.Printf("INFO: seed 新建 default user group + 绑 user role")
		return nil
	}
	if err != nil {
		return fmt.Errorf("query default user group: %w", err)
	}

	// 已存在：只修 is_system
	if _, err := q.UpdateUserGroupSystemFlag(ctx, sqlcgen.UpdateUserGroupSystemFlagParams{
		Name:     "default_user",
		IsSystem: true,
	}); err != nil {
		return fmt.Errorf("修复 default user group 系统标志失败: %w", err)
	}
	return nil
}

// ---------- 5. 清理非法关系 ----------

// cleanupNonAssignableRoleBindings 删除 user_roles / group_roles 中
// role.assignable=false 的记录（典型：anonymous）。
// 每次启动都跑，保证数据库里**不会**残留 assignable=false 的 role 绑定。
func (c *AuthCatalog) cleanupNonAssignableRoleBindings(ctx context.Context, q *sqlcgen.Queries) error {
	if n, err := q.DeleteUserRolesByNonAssignableRoles(ctx); err != nil {
		return fmt.Errorf("delete user_roles by non-assignable: %w", err)
	} else if n > 0 {
		log.Printf("WARN: 清理了 %d 条 user_roles 中 assignable=false 的脏关系", n)
	}
	if n, err := q.DeleteGroupRolesByNonAssignableRoles(ctx); err != nil {
		return fmt.Errorf("delete group_roles by non-assignable: %w", err)
	} else if n > 0 {
		log.Printf("WARN: 清理了 %d 条 group_roles 中 assignable=false 的脏关系", n)
	}
	return nil
}

// ---------- 工具函数 ----------

func strToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
