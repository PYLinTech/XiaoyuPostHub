package user

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapSuperAdmin 启动期调用一次，处理 .env 中的超管信息：
//
//   - 账号不存在 → 创建
//   - 账号存在 + 哈希不一致 → UPDATE password_hash 直接覆盖
//   - 账号存在 + 哈希一致 → 不动
//   - 无论新建还是已存在，最终只加入 default_user 用户组
//
// 哈希格式：bcrypt cost=12（用 ValidatePasswordHash 在入口校验）。
// 不再支持旧的 sha256:<salt>:<hash>，启动期直接报错。
//
// **关键**：超管账号固定加入 default_user 用户组，以继承默认组权限并让所有
// 配额查询都有明确来源。如果该 username 之前是普通用户，升级时先清空原用户组
// 关系，再重新绑定 default_user。
//
// 超管身份由 username == config.EnvSuperAdmin 单点决定，DB 操作无法影响。
//
// 启动期和 bootstrap.AuthCatalog 用同一把 advisory lock：
// "xiaoyu_auth_bootstrap"。多实例部署时，所有启动期初始化串行执行。
func BootstrapSuperAdmin(ctx context.Context, pool *pgxpool.Pool) error {
	name := config.EnvSuperAdmin
	hash := config.EnvSuperAdminPasswordHash

	// 入口即校验 .env 中的 password_hash 是合法 bcrypt cost=12；
	// 不合法直接拒绝启动，避免把错误格式写进 DB。
	if err := ValidatePasswordHash(hash); err != nil {
		return fmt.Errorf("SUPER_ADMIN_PASSWORD_HASH 无效：%w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "xiaoyu_auth_bootstrap"); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	qtx := sqlcgen.New(pool).WithTx(tx)

	// 1. 查询现有 user，统一拿到后续绑定默认用户组所需的 user id。
	existing, err := qtx.GetUserByUsername(ctx, name)
	var userID int64
	created := false
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 2a. 不存在：创建账号。
		createdUser, err := qtx.CreateUser(ctx, sqlcgen.CreateUserParams{
			Username:     name,
			PasswordHash: hash,
		})
		if err != nil {
			return fmt.Errorf("创建超管失败: %w", err)
		}
		userID = createdUser.ID
		created = true

	case err != nil:
		return fmt.Errorf("查询超管失败: %w", err)

	default:
		userID = existing.ID
		// 2b. 存在：清理原用户组关系，避免非默认组的高优先级配额覆盖默认配额。
		if _, err := qtx.UnassignAllGroupsFromUser(ctx, existing.ID); err != nil {
			return fmt.Errorf("清理超管 %s 的 group 关联失败: %w", name, err)
		}

		// 3. 同步密码哈希。
		if existing.PasswordHash != hash {
			if _, err := qtx.UpdatePasswordHashByUsername(ctx, sqlcgen.UpdatePasswordHashByUsernameParams{
				Username:     name,
				PasswordHash: hash,
			}); err != nil {
				return fmt.Errorf("同步超管密码哈希失败: %w", err)
			}
			if err := qtx.DeleteUserSessionsByUserID(ctx, existing.ID); err != nil {
				return fmt.Errorf("使超管旧会话失效失败: %w", err)
			}
		}
	}

	// 4. AuthCatalog 先于本函数运行，default_user 必须存在；绑定使用
	// ON CONFLICT DO NOTHING，保证多次启动幂等。
	defaultGroup, err := qtx.GetUserGroupByName(ctx, group.NameDefaultUser)
	if err != nil {
		return fmt.Errorf("读取默认用户组失败: %w", err)
	}
	if _, err := qtx.AssignUserToGroup(ctx, sqlcgen.AssignUserToGroupParams{
		UserID: userID, GroupID: defaultGroup.ID,
	}); err != nil {
		return fmt.Errorf("超管加入默认用户组失败: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if created {
		log.Printf("INFO: 已创建超管账号并加入默认用户组: %s", name)
	} else if existing.PasswordHash != hash {
		log.Printf("INFO: 超管 %s 密码哈希已从 .env 同步并匹配默认用户组", name)
	} else {
		log.Printf("INFO: 超管 %s 已匹配默认用户组，无需同步密码", name)
	}
	return nil
}
