package user

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/PYLinTech/XiaoyuPostHub/backend/config"
	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5"
)

// BootstrapSuperAdmin 启动期调用一次，处理 .env 中的超管信息：
//
//   - 账号不存在 → 创建（roles = ['user']，groups = []），不写 'all'
//   - 账号存在 + 哈希不一致 → UPDATE password_hash 直接覆盖
//   - 账号存在 + 哈希一致 → 不动
//
// 运行时对 EnvSuperAdmin 的 'all' 临时附加由 Repo.GetByUsername 处理（仅追加到 Roles，不动 Groups）。
func BootstrapSuperAdmin(ctx context.Context, q *sqlcgen.Queries) error {
	name := config.EnvSuperAdmin
	hash := config.EnvSuperAdminPasswordHash

	existing, err := q.GetUserByUsername(ctx, name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err := q.CreateUser(ctx, sqlcgen.CreateUserParams{
			Username:     name,
			PasswordHash: hash,
			Roles:        []string{"user"},
			Groups:       []string{},
		})
		if err != nil {
			return fmt.Errorf("创建超管失败: %w", err)
		}
		log.Printf("INFO: 已创建超管账号: %s", name)
		return nil

	case err != nil:
		return fmt.Errorf("查询超管失败: %w", err)

	default:
		if existing.PasswordHash != hash {
			if _, err := q.UpdatePasswordHashByUsername(ctx, sqlcgen.UpdatePasswordHashByUsernameParams{
				Username:     name,
				PasswordHash: hash,
			}); err != nil {
				return fmt.Errorf("同步超管密码哈希失败: %w", err)
			}
			log.Printf("INFO: 超管 %s 密码哈希已从 .env 同步", name)
		} else {
			log.Printf("INFO: 超管 %s 已存在，无需变更", name)
		}
		return nil
	}
}

// HealAllRole 启动期调用一次，清理库中残留的 'all'（篡改/脏数据兜底）。
// 正常流程下 CHECK no_all 已经保证不会写入 'all'，这里兜历史数据。
func HealAllRole(ctx context.Context, q *sqlcgen.Queries) error {
	affected, err := q.RemoveAllFromAllUsers(ctx)
	if err != nil {
		return fmt.Errorf("自愈清理失败: %w", err)
	}
	if affected > 0 {
		log.Printf("WARN: 检测到 %d 个用户 roles 含 'all'（篡改或脏数据），已自动清除", affected)
	}
	return nil
}
