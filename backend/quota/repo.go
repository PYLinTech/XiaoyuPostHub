package quota

import (
	"context"
	"errors"
	"fmt"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrQuotaProfileNotFound = errors.New("quota: 不存在")
	ErrQuotaProfileIsSystem = errors.New("quota: 系统 quota profile 不可删除")
)

// Repo 业务层访问 quota_profiles 表的入口。
type Repo struct {
	q *sqlcgen.Queries
}

func NewRepo(q *sqlcgen.Queries) *Repo { return &Repo{q: q} }

// GetByName 按 name 查 quota profile。
func (r *Repo) GetByName(ctx context.Context, name string) (sqlcgen.QuotaProfile, error) {
	qp, err := r.q.GetQuotaProfileByName(ctx, name)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.QuotaProfile{}, fmt.Errorf("%w: %s", ErrQuotaProfileNotFound, name)
	}
	return qp, err
}

// GetByID 按 id 查 quota profile。
func (r *Repo) GetByID(ctx context.Context, id int64) (sqlcgen.QuotaProfile, error) {
	qp, err := r.q.GetQuotaProfileByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.QuotaProfile{}, fmt.Errorf("%w: id=%d", ErrQuotaProfileNotFound, id)
	}
	return qp, err
}

// List 返回所有 quota profile（系统在前，业务在后）。
func (r *Repo) List(ctx context.Context) ([]sqlcgen.QuotaProfile, error) {
	return r.q.ListQuotaProfiles(ctx)
}

// CreateQuotaProfile 业务层创建非系统 quota profile。
// is_system 永远 false（系统 profile 由 bootstrap 创建）。
// 任意限额字段传 nil 表示"不限"。
func (r *Repo) CreateQuotaProfile(
	ctx context.Context,
	name, description string,
	storageBytesLimit *int64,
	singleFileBytesLimit *int64,
	dailyUploadBytesLimit *int64,
	dailyUploadCountLimit *int64,
	activeShareCountLimit *int64,
	activeDirectLinkLimit *int64,
) (sqlcgen.QuotaProfile, error) {
	return r.q.CreateQuotaProfile(ctx, sqlcgen.CreateQuotaProfileParams{
		Name:                  name,
		Description:           strToText(description),
		StorageBytesLimit:     int64PtrToPgtype(storageBytesLimit),
		SingleFileBytesLimit:  int64PtrToPgtype(singleFileBytesLimit),
		DailyUploadBytesLimit: int64PtrToPgtype(dailyUploadBytesLimit),
		DailyUploadCountLimit: int64PtrToPgtype(dailyUploadCountLimit),
		ActiveShareCountLimit: int64PtrToPgtype(activeShareCountLimit),
		ActiveDirectLinkLimit: int64PtrToPgtype(activeDirectLinkLimit),
	})
}

// UpdateQuotaProfile 改 quota profile 的描述与限额。
// **允许**修改系统 profile：default_user 的限额可以通过配置面板调整。
func (r *Repo) UpdateQuotaProfile(
	ctx context.Context,
	id int64,
	description string,
	storageBytesLimit *int64,
	singleFileBytesLimit *int64,
	dailyUploadBytesLimit *int64,
	dailyUploadCountLimit *int64,
	activeShareCountLimit *int64,
	activeDirectLinkLimit *int64,
) error {
	// 先校验存在（不存在时 sqlc UPDATE 影响 0 行，调用方会以为成功）
	if _, err := r.GetByID(ctx, id); err != nil {
		return err
	}
	if _, err := r.q.UpdateQuotaProfile(ctx, sqlcgen.UpdateQuotaProfileParams{
		ID:                    id,
		Description:           strToText(description),
		StorageBytesLimit:     int64PtrToPgtype(storageBytesLimit),
		SingleFileBytesLimit:  int64PtrToPgtype(singleFileBytesLimit),
		DailyUploadBytesLimit: int64PtrToPgtype(dailyUploadBytesLimit),
		DailyUploadCountLimit: int64PtrToPgtype(dailyUploadCountLimit),
		ActiveShareCountLimit: int64PtrToPgtype(activeShareCountLimit),
		ActiveDirectLinkLimit: int64PtrToPgtype(activeDirectLinkLimit),
	}); err != nil {
		return err
	}
	return nil
}

// DeleteQuotaProfile 删 quota profile（仅非系统）。
// 系统 profile 不可删——它们是兜底和基础设施。
func (r *Repo) DeleteQuotaProfile(ctx context.Context, id int64) error {
	qp, err := r.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if qp.IsSystem {
		return ErrQuotaProfileIsSystem
	}
	if _, err := r.q.DeleteQuotaProfile(ctx, id); err != nil {
		return err
	}
	return nil
}

// GetEffectiveQuotaByUser 拿 user 的有效 quota profile。
// 3 级优先级由 SQL 内部合并：users.quota_profile_id > group.quota_profile_id > name='default_user'。
// 用户不存在时返回 ErrQuotaProfileNotFound（不会误用 default_user）。
//
// 返回标准 sqlcgen.QuotaProfile（不暴露 priority_tier 等内部排序字段）。
func (r *Repo) GetEffectiveQuotaByUser(ctx context.Context, userID int64) (sqlcgen.QuotaProfile, error) {
	qp, err := r.q.GetEffectiveQuotaByUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.QuotaProfile{}, fmt.Errorf("%w: user=%d 无任何可用 quota profile", ErrQuotaProfileNotFound, userID)
	}
	return qp, err
}

// ---------- 工具函数 ----------

func strToText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

// int64PtrToPgtype 把 *int64 转成 pgtype.Int8。
// 用法：quota 字段 NULL 含义是"不限"，所以传 nil 表达不限，传 *int64 表达具体值。
func int64PtrToPgtype(p *int64) pgtype.Int8 {
	if p == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *p, Valid: true}
}
