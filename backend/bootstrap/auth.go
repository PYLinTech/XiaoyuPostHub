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
	return tx.Commit(ctx)
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
