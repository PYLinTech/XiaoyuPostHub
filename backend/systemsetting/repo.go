// Package systemsetting 管理程序自身的全部非敏感运行期配置。
package systemsetting

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultSiteName    = "XiaoyuPostHub"
	DefaultStoragePath = "/data/uploads"
	// RetrievalProxy 表示分享取数走本机中转优先；RetrievalRedirect 表示 302 优先
	// （浏览器直连第三方存储/云盘，默认）。302 不可用时的行为由 RedirectFallback 决定。
	RetrievalProxy    = "proxy"
	RetrievalRedirect = "redirect"
)

var (
	ErrNotInitialized     = errors.New("systemsetting: 尚未初始化")
	ErrSiteNameBlank      = errors.New("systemsetting: 站点名称不能为空")
	ErrStoragePathInvalid = errors.New("systemsetting: 文件存储路径必须是绝对路径")
	ErrRandomCodeInvalid  = errors.New("systemsetting: 随机码配置无效")
	ErrUploadChunkSize    = errors.New("systemsetting: 分片大小必须在 1M 到 64M 之间")
	ErrUploadConcurrency  = errors.New("systemsetting: 上传并发数必须在 1 到 8 之间")
	ErrTrashRetention     = errors.New("systemsetting: 回收期限必须在 1 到 3650 天之间")
	ErrPickupLifetime     = errors.New("systemsetting: 取件码有效期上限无效")
	ErrInvitationValidity = errors.New("systemsetting: 邀请码有效期必须在 0（永久）到 3650 天之间")
	ErrUploadMaxFileBytes = errors.New("systemsetting: 单文件上限必须在 16MiB 到 1TiB 之间")
	ErrRetrievalMode      = errors.New("systemsetting: 交付方式无效")
	ErrStorageChunkSize   = errors.New("systemsetting: 分片大小必须是 4MiB 的整数倍（4MiB~1GiB），分片存储不可关闭")
)

type Config struct {
	SiteName                   string
	StoragePath                string
	InvitationLength           int16
	InvitationCaseSensitive    bool
	InvitationIncludeLetters   bool
	InvitationIncludeNumbers   bool
	ShareLength                int16
	ShareCaseSensitive         bool
	ShareIncludeLetters        bool
	ShareIncludeNumbers        bool
	PickupLength               int16
	PickupCaseSensitive        bool
	PickupIncludeLetters       bool
	PickupIncludeNumbers       bool
	PickupMaxLifetimeSeconds   *int64
	LoginTOTPEnabled           bool
	UploadRequiresReview       bool
	CustomShareRequiresReview  bool
	UploadChunkSizeBytes       int32
	UploadTaskChunkConcurrency int16
	UploadUserTaskConcurrency  int16
	TrashRetentionDays         int16
	// EncryptNewFiles 开启后新上传文件使用分块 AES-256-GCM 加密存储。
	EncryptNewFiles bool
	// ShareRetrievalMode 是分享取数方式：redirect=302 优先（默认），proxy=本机中转优先。
	ShareRetrievalMode string
	// StorageChunkSizeBytes 是新上传对象的分片粒度（0 = 不分片）；必须为
	// 加密块（4MiB）的整数倍，保证加密后分片仍可独立随机读取。
	StorageChunkSizeBytes int32
	// PickupAllowPermanent 允许创建/修改"永久有效"的取件码分享（管理端开关）。
	PickupAllowPermanent bool
	// InvitationValidDays 邀请码有效期（天），0 = 永久。
	InvitationValidDays int32
	// UploadMaxFileBytes 单文件系统硬上限（独立于用户组配额的安全上限）。
	UploadMaxFileBytes int64
	// RedirectFallback 表示 302 取数不可用时是否自动降级本机中转（默认开启）。
	RedirectFallback bool
	// CrossUserDedupe 秒传是否允许跨用户复用物理对象（false = 只复用本人对象）。
	CrossUserDedupe bool
}

// DefaultUploadMaxFileBytes 是单文件系统硬上限的默认值（100GiB）。
const DefaultUploadMaxFileBytes int64 = 100 << 30

type Repo struct {
	q *sqlcgen.Queries
	// pool 仅用于 post-034 新增列的读写（见 Knobs）。为 nil 时退化为默认值，
	// 便于不依赖数据库连接的单元测试构造。
	pool *pgxpool.Pool
}

func NewRepo(q *sqlcgen.Queries, pools ...*pgxpool.Pool) *Repo {
	repo := &Repo{q: q}
	if len(pools) > 0 {
		repo.pool = pools[0]
	}
	return repo
}

// Knobs 是后加的管理端开关（独立于 sqlc 生成查询）：新增列在无 sqlc 环境无法
// 重新生成绑定，这里用最小原始 SQL 读写，避免改动生成代码。
type Knobs struct {
	// PickupAllowPermanent 允许创建/修改"永久有效"的取件码分享。
	PickupAllowPermanent bool
	// InvitationValidDays 邀请码有效期（天），0 表示永久。
	InvitationValidDays int32
	// UploadMaxFileBytes 单文件系统硬上限（安全上限，独立于用户组配额）。
	UploadMaxFileBytes int64
	// CrossUserDedupe 秒传是否允许跨用户复用物理对象（全平台去重）。
	// TRUE：任何用户凭 sha256+精确大小即可复用他人对象（省存储，但知道哈希即可
	// 取得他人私有文件内容）；FALSE：只复用自己已有引用的对象。
	CrossUserDedupe bool
	// RedirectFallback 表示 302 取数不可用（后端无直链能力 / 请求方无法前端解密）
	// 时是否自动降级为本机中转；关闭时按下载失败处理（不向用户暴露内部原因）。
	RedirectFallback bool
}

// DefaultKnobs 返回新开关的出厂默认值（与迁移 034/035/037 的列默认值一致）。
func DefaultKnobs() Knobs {
	return Knobs{
		PickupAllowPermanent: true, InvitationValidDays: 90,
		UploadMaxFileBytes: DefaultUploadMaxFileBytes, CrossUserDedupe: true,
		RedirectFallback: true,
	}
}

// GetKnobs 读取管理端开关；未接连接池时返回默认值。
func (r *Repo) GetKnobs(ctx context.Context) (Knobs, error) {
	if r.pool == nil {
		return DefaultKnobs(), nil
	}
	out := DefaultKnobs()
	if err := r.pool.QueryRow(ctx, `
		SELECT pickup_allow_permanent, invitation_valid_days, upload_max_file_bytes, cross_user_dedupe,
		       redirect_fallback
		FROM system_settings WHERE id=1`).
		Scan(&out.PickupAllowPermanent, &out.InvitationValidDays, &out.UploadMaxFileBytes,
			&out.CrossUserDedupe, &out.RedirectFallback); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Knobs{}, ErrNotInitialized
		}
		return Knobs{}, err
	}
	return out, nil
}

// UpdateKnobs 保存管理端开关（含校验）；未接连接池时为无操作，便于测试。
func (r *Repo) UpdateKnobs(ctx context.Context, k Knobs) (Knobs, error) {
	if k.InvitationValidDays < 0 || k.InvitationValidDays > 3650 {
		return Knobs{}, ErrInvitationValidity
	}
	if k.UploadMaxFileBytes < 16<<20 || k.UploadMaxFileBytes > 1<<40 {
		return Knobs{}, ErrUploadMaxFileBytes
	}
	if r.pool == nil {
		return k, nil
	}
	out := k
	if err := r.pool.QueryRow(ctx, `
		UPDATE system_settings
		SET pickup_allow_permanent=$1, invitation_valid_days=$2, upload_max_file_bytes=$3,
		    cross_user_dedupe=$4, redirect_fallback=$5, updated_at=NOW()
		WHERE id=1
		RETURNING pickup_allow_permanent, invitation_valid_days, upload_max_file_bytes,
		          cross_user_dedupe, redirect_fallback`,
		k.PickupAllowPermanent, k.InvitationValidDays, k.UploadMaxFileBytes, k.CrossUserDedupe,
		k.RedirectFallback).
		Scan(&out.PickupAllowPermanent, &out.InvitationValidDays, &out.UploadMaxFileBytes,
			&out.CrossUserDedupe, &out.RedirectFallback); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Knobs{}, ErrNotInitialized
		}
		return Knobs{}, err
	}
	return out, nil
}

func (r *Repo) EnsureDefaults(ctx context.Context) error { return r.q.EnsureSystemSettings(ctx) }

func (r *Repo) Get(ctx context.Context) (sqlcgen.SystemSetting, error) {
	settings, err := r.q.GetSystemSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.SystemSetting{}, ErrNotInitialized
	}
	return settings, err
}

func (r *Repo) UpdateAll(ctx context.Context, config Config) (sqlcgen.SystemSetting, error) {
	siteName, storagePath, err := validateIdentity(config.SiteName, config.StoragePath)
	if err != nil {
		return sqlcgen.SystemSetting{}, err
	}
	if !validCodeConfig(config.InvitationLength, config.InvitationIncludeLetters, config.InvitationIncludeNumbers) ||
		!validCodeConfig(config.ShareLength, config.ShareIncludeLetters, config.ShareIncludeNumbers) ||
		config.PickupLength < 1 || config.PickupLength > 64 || (!config.PickupIncludeLetters && !config.PickupIncludeNumbers) {
		return sqlcgen.SystemSetting{}, ErrRandomCodeInvalid
	}
	if config.PickupMaxLifetimeSeconds != nil && *config.PickupMaxLifetimeSeconds <= 0 {
		return sqlcgen.SystemSetting{}, ErrPickupLifetime
	}
	if config.UploadChunkSizeBytes < 1<<20 || config.UploadChunkSizeBytes > 64<<20 {
		return sqlcgen.SystemSetting{}, ErrUploadChunkSize
	}
	if config.UploadTaskChunkConcurrency < 1 || config.UploadTaskChunkConcurrency > 8 ||
		config.UploadUserTaskConcurrency < 1 || config.UploadUserTaskConcurrency > 8 {
		return sqlcgen.SystemSetting{}, ErrUploadConcurrency
	}
	if config.TrashRetentionDays < 1 || config.TrashRetentionDays > 3650 {
		return sqlcgen.SystemSetting{}, ErrTrashRetention
	}
	if config.ShareRetrievalMode != RetrievalProxy && config.ShareRetrievalMode != RetrievalRedirect {
		return sqlcgen.SystemSetting{}, ErrRetrievalMode
	}
	if !ValidStorageChunkSize(config.StorageChunkSizeBytes) {
		return sqlcgen.SystemSetting{}, ErrStorageChunkSize
	}
	// 后加的四项开关先校验（避免主配置已写入而开关无效的半成功状态）。
	knobs := Knobs{
		PickupAllowPermanent: config.PickupAllowPermanent,
		InvitationValidDays:  config.InvitationValidDays,
		UploadMaxFileBytes:   config.UploadMaxFileBytes,
		CrossUserDedupe:      config.CrossUserDedupe,
		RedirectFallback:     config.RedirectFallback,
	}
	if knobs.InvitationValidDays < 0 || knobs.InvitationValidDays > 3650 {
		return sqlcgen.SystemSetting{}, ErrInvitationValidity
	}
	if knobs.UploadMaxFileBytes < 16<<20 || knobs.UploadMaxFileBytes > 1<<40 {
		return sqlcgen.SystemSetting{}, ErrUploadMaxFileBytes
	}
	settings, err := r.q.UpdateAllSystemSettings(ctx, sqlcgen.UpdateAllSystemSettingsParams{
		SiteName: siteName, StoragePath: storagePath,
		InvitationLength: config.InvitationLength, InvitationCaseSensitive: config.InvitationCaseSensitive,
		InvitationIncludeLetters: config.InvitationIncludeLetters, InvitationIncludeNumbers: config.InvitationIncludeNumbers,
		ShareLength: config.ShareLength, ShareCaseSensitive: config.ShareCaseSensitive,
		ShareIncludeLetters: config.ShareIncludeLetters, ShareIncludeNumbers: config.ShareIncludeNumbers,
		PickupLength: config.PickupLength, PickupCaseSensitive: config.PickupCaseSensitive,
		PickupIncludeLetters: config.PickupIncludeLetters, PickupIncludeNumbers: config.PickupIncludeNumbers,
		PickupMaxLifetimeSeconds: nullableInt8(config.PickupMaxLifetimeSeconds),
		LoginTotpEnabled:         config.LoginTOTPEnabled,
		UploadRequiresReview:     config.UploadRequiresReview, CustomShareRequiresReview: config.CustomShareRequiresReview,
		UploadChunkSizeBytes:       config.UploadChunkSizeBytes,
		UploadTaskChunkConcurrency: config.UploadTaskChunkConcurrency,
		UploadUserTaskConcurrency:  config.UploadUserTaskConcurrency,
		TrashRetentionDays:         config.TrashRetentionDays,
		EncryptNewFiles:            config.EncryptNewFiles,
		ShareRetrievalMode:         config.ShareRetrievalMode,
		StorageChunkSizeBytes:      config.StorageChunkSizeBytes,
	})
	if err != nil {
		return sqlcgen.SystemSetting{}, err
	}
	if _, err := r.UpdateKnobs(ctx, knobs); err != nil {
		return sqlcgen.SystemSetting{}, err
	}
	return settings, nil
}

// DefaultStorageChunkSizeBytes 是分片粒度的默认值（强制分片，不可为 0）。
const DefaultStorageChunkSizeBytes = 64 << 20

// ValidStorageChunkSize 校验分片粒度：必须是 4MiB 的整数倍（4MiB~1GiB）。
// 分片是强制项——它与加密块对齐（保证加密对象可随机读取），也是迁移/补加密/
// 重新分片等维护能力的基础形态，因此不再提供"不分片"选项。
func ValidStorageChunkSize(size int32) bool {
	const block = 4 << 20
	return size >= block && size <= 1<<30 && size%block == 0
}

func nullableInt8(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}

func validateIdentity(siteName, storagePath string) (string, string, error) {
	siteName = strings.TrimSpace(siteName)
	if siteName == "" {
		return "", "", ErrSiteNameBlank
	}
	storagePath = strings.TrimSpace(storagePath)
	if !filepath.IsAbs(storagePath) {
		return "", "", ErrStoragePathInvalid
	}
	return siteName, filepath.Clean(storagePath), nil
}

func validCodeConfig(length int16, letters, numbers bool) bool {
	return length >= 4 && length <= 64 && (letters || numbers)
}
