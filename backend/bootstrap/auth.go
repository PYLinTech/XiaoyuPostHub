// Package bootstrap 保证默认配额方案和默认用户组存在。
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/PYLinTech/XiaoyuPostHub/backend/group"
	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/quota"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const advisoryLockKey = "xiaoyu_auth_bootstrap"

type AuthCatalog struct{ pool *pgxpool.Pool }

func NewAuthCatalog(pool *pgxpool.Pool) *AuthCatalog { return &AuthCatalog{pool: pool} }

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
	if err := seedDefaultQuotaProfile(ctx, q); err != nil {
		return err
	}
	if err := seedDefaultUserGroup(ctx, q); err != nil {
		return err
	}
	if err := seedGuestQuotaProfile(ctx, q); err != nil {
		return err
	}
	if err := seedGuestUserGroup(ctx, q); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// seedGuestQuotaProfile 保证 guest（未登录访客）配额方案存在：未登录访问按
// 来源 IP 识别为独立"用户"，统一使用该方案限流（默认不限，管理员可在
// 「权限与配额」中调整，或把 guest 用户组改绑到其它方案）。
func seedGuestQuotaProfile(ctx context.Context, q *sqlcgen.Queries) error {
	_, err := q.GetQuotaProfileByName(ctx, quota.NameGuest)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.InsertQuotaProfileIfMissing(ctx, sqlcgen.InsertQuotaProfileIfMissingParams{
			Name: quota.NameGuest, Description: text("未登录访客配额（按 IP 识别，空值表示不限）"),
		}); err != nil {
			return fmt.Errorf("创建访客配额方案: %w", err)
		}
		log.Printf("INFO: 已创建访客配额方案 %q", quota.NameGuest)
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取访客配额方案: %w", err)
	}
	_, err = q.UpdateQuotaProfileSystemFlag(ctx, sqlcgen.UpdateQuotaProfileSystemFlagParams{Name: quota.NameGuest, IsSystem: true})
	return err
}

// seedGuestUserGroup 保证 guest 系统用户组存在（不可删除、不接受成员）。
// 未登录请求不匹配任何成员身份，统一按该组绑定的配额方案限流。
func seedGuestUserGroup(ctx context.Context, q *sqlcgen.Queries) error {
	quotaRow, err := q.GetQuotaProfileByName(ctx, quota.NameGuest)
	if err != nil {
		return fmt.Errorf("读取访客配额方案: %w", err)
	}
	_, err = q.GetUserGroupByName(ctx, quota.NameGuest)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.InsertSystemUserGroupIfMissing(ctx, sqlcgen.InsertSystemUserGroupIfMissingParams{
			Name: quota.NameGuest, Description: text("未登录访客用户组（按 IP 识别为一个用户，不可删除）"),
			QuotaProfileID: quotaRow.ID, Priority: 0,
		}); err != nil {
			return fmt.Errorf("创建访客用户组: %w", err)
		}
		log.Printf("INFO: 已创建未登录访客用户组 %q", quota.NameGuest)
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取访客用户组: %w", err)
	}
	_, err = q.UpdateUserGroupSystemFlag(ctx, sqlcgen.UpdateUserGroupSystemFlagParams{Name: quota.NameGuest, IsSystem: true})
	return err
}

func seedDefaultQuotaProfile(ctx context.Context, q *sqlcgen.Queries) error {
	_, err := q.GetQuotaProfileByName(ctx, quota.NameDefaultUser)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.InsertQuotaProfileIfMissing(ctx, sqlcgen.InsertQuotaProfileIfMissingParams{
			Name: quota.NameDefaultUser, Description: text("普通用户默认配额（空值表示不限）"),
		}); err != nil {
			return fmt.Errorf("创建默认配额方案: %w", err)
		}
		log.Printf("INFO: 已创建默认配额方案 %q", quota.NameDefaultUser)
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取默认配额方案: %w", err)
	}
	_, err = q.UpdateQuotaProfileSystemFlag(ctx, sqlcgen.UpdateQuotaProfileSystemFlagParams{Name: quota.NameDefaultUser, IsSystem: true})
	return err
}

func seedDefaultUserGroup(ctx context.Context, q *sqlcgen.Queries) error {
	quotaRow, err := q.GetQuotaProfileByName(ctx, quota.NameDefaultUser)
	if err != nil {
		return fmt.Errorf("读取默认配额方案: %w", err)
	}
	_, err = q.GetUserGroupByName(ctx, group.NameDefaultUser)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := q.InsertSystemUserGroupIfMissing(ctx, sqlcgen.InsertSystemUserGroupIfMissingParams{
			Name: group.NameDefaultUser, Description: text("普通用户默认用户组"),
			QuotaProfileID: quotaRow.ID, Priority: 0,
		}); err != nil {
			return fmt.Errorf("创建默认用户组: %w", err)
		}
		defaults := []string{
			permission.Login, permission.Upload, permission.Download, permission.Preview,
			permission.Rename, permission.DeleteOwn, permission.Share, permission.PickupShare, permission.DirectLink,
		}
		for _, code := range defaults {
			if err := q.InsertDefaultGroupPermissionIfMissing(ctx, sqlcgen.InsertDefaultGroupPermissionIfMissingParams{
				Name: group.NameDefaultUser, Permission: code,
			}); err != nil {
				return fmt.Errorf("写入默认用户组权限 %s: %w", code, err)
			}
		}
		log.Printf("INFO: 已创建默认用户组并授予基础权限")
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取默认用户组: %w", err)
	}
	_, err = q.UpdateUserGroupSystemFlag(ctx, sqlcgen.UpdateUserGroupSystemFlagParams{Name: group.NameDefaultUser, IsSystem: true})
	return err
}

func text(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }
