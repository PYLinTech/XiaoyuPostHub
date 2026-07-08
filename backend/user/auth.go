package user

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapSuperAdmin 启动期调用一次，处理 .env 中的超管信息：
//
//   - 账号不存在 → 创建
//   - 账号存在 + 哈希不一致 → UPDATE password_hash 直接覆盖
//   - 账号存在 + 哈希一致 → 不动
//
// **关键**：超管账号**不**分配任何 role、**不**加入 default_user group、**不**保留
// 任何 user_permission_overrides。如果该 username 之前是普通用户（带残留关系），
// 升级为超管时**全部清掉**。所有操作在**单事务**里完成，避免中途失败导致半成品。
//
// 超管身份由 username == config.EnvSuperAdmin 单点决定，DB 操作无法影响。
//
// 启动期和 bootstrap.AuthCatalog 用同一把 advisory lock：
// "xiaoyu_auth_bootstrap"。多实例部署时，所有启动期初始化串行执行。
func BootstrapSuperAdmin(ctx context.Context, pool *pgxpool.Pool) error {
	name := config.EnvSuperAdmin
	hash := config.EnvSuperAdminPasswordHash

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", "xiaoyu_auth_bootstrap"); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}

	qtx := sqlcgen.New(pool).WithTx(tx)

	// 1. 查询现有 user
	existing, err := qtx.GetUserByUsername(ctx, name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 2a. 不存在：直接创建（不分配 role / 不入 group / 不留 override）
		_, err := qtx.CreateUser(ctx, sqlcgen.CreateUserParams{
			Username:     name,
			PasswordHash: hash,
		})
		if err != nil {
			return fmt.Errorf("创建超管失败: %w", err)
		}
		// commit 前无残留关系可清——新用户表里没记录。
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		log.Printf("INFO: 已创建超管账号: %s", name)
		return nil

	case err != nil:
		return fmt.Errorf("查询超管失败: %w", err)

	default:
		// 2b. 存在：清理残留关系（即使是之前是普通用户，也要彻底清空）
		// 删 user_permission_overrides（即使没有也 OK，0 affected）
		if _, err := qtx.ClearAllPermissionOverridesByUserID(ctx, existing.ID); err != nil {
			return fmt.Errorf("清理超管 %s 的 permission override 失败: %w", name, err)
		}
		// 删 user_group_memberships
		if _, err := qtx.UnassignAllGroupsFromUser(ctx, existing.ID); err != nil {
			return fmt.Errorf("清理超管 %s 的 group 关联失败: %w", name, err)
		}
		// 删 user_roles
		if _, err := qtx.UnassignAllRolesFromUser(ctx, existing.ID); err != nil {
			return fmt.Errorf("清理超管 %s 的 role 关联失败: %w", name, err)
		}

		// 3. 同步密码哈希
		if existing.PasswordHash != hash {
			if _, err := qtx.UpdatePasswordHashByUsername(ctx, sqlcgen.UpdatePasswordHashByUsernameParams{
				Username:     name,
				PasswordHash: hash,
			}); err != nil {
				return fmt.Errorf("同步超管密码哈希失败: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit: %w", err)
			}
			log.Printf("INFO: 超管 %s 密码哈希已从 .env 同步（已清残留关系）", name)
		} else {
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit: %w", err)
			}
			log.Printf("INFO: 超管 %s 已存在，无需变更（已清残留关系）", name)
		}
		return nil
	}
}
