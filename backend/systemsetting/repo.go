// Package systemsetting 管理程序自身的全部非敏感运行期配置。
package systemsetting

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
	"github.com/jackc/pgx/v5"
)

const (
	DefaultSiteName    = "XiaoyuPostHub"
	DefaultStoragePath = "/data/uploads"
	PackBackend        = "backend"
	PackFrontend       = "frontend"
	DeliveryBlob       = "blob"
	DeliveryTemporary  = "temporary_link"
)

var (
	ErrNotInitialized     = errors.New("systemsetting: 尚未初始化")
	ErrSiteNameBlank      = errors.New("systemsetting: 站点名称不能为空")
	ErrStoragePathInvalid = errors.New("systemsetting: 文件存储路径必须是绝对路径")
	ErrRandomCodeInvalid  = errors.New("systemsetting: 随机码配置无效")
	ErrUploadChunkSize    = errors.New("systemsetting: 分片大小必须在 1M 到 64M 之间")
	ErrUploadConcurrency  = errors.New("systemsetting: 上传并发数必须在 1 到 8 之间")
	ErrTrashRetention     = errors.New("systemsetting: 回收期限必须在 1 到 3650 天之间")
	ErrDownloadMode       = errors.New("systemsetting: 下载策略无效")
)

type Config struct {
	SiteName                   string
	StoragePath                string
	FolderPackMode             string
	ShareDeliveryMode          string
	InvitationLength           int16
	InvitationCaseSensitive    bool
	InvitationIncludeLetters   bool
	InvitationIncludeNumbers   bool
	ShareLength                int16
	ShareCaseSensitive         bool
	ShareIncludeLetters        bool
	ShareIncludeNumbers        bool
	UploadRequiresReview       bool
	CustomShareRequiresReview  bool
	UploadChunkSizeBytes       int32
	UploadTaskChunkConcurrency int16
	UploadUserTaskConcurrency  int16
	TrashRetentionDays         int16
}

type Repo struct{ q *sqlcgen.Queries }

func NewRepo(q *sqlcgen.Queries) *Repo { return &Repo{q: q} }

func (r *Repo) EnsureDefaults(ctx context.Context) error { return r.q.EnsureSystemSettings(ctx) }

func (r *Repo) Get(ctx context.Context) (sqlcgen.SystemSetting, error) {
	settings, err := r.q.GetSystemSettings(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.SystemSetting{}, ErrNotInitialized
	}
	return settings, err
}

func (r *Repo) Update(ctx context.Context, siteName, storagePath string) (sqlcgen.SystemSetting, error) {
	siteName, storagePath, err := validateIdentity(siteName, storagePath)
	if err != nil {
		return sqlcgen.SystemSetting{}, err
	}
	settings, err := r.q.UpdateSystemIdentity(ctx, sqlcgen.UpdateSystemIdentityParams{
		SiteName: siteName, StoragePath: storagePath,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.SystemSetting{}, fmt.Errorf("%w: 请先初始化默认配置", ErrNotInitialized)
	}
	return settings, err
}

func (r *Repo) UpdateAll(ctx context.Context, config Config) (sqlcgen.SystemSetting, error) {
	siteName, storagePath, err := validateIdentity(config.SiteName, config.StoragePath)
	if err != nil {
		return sqlcgen.SystemSetting{}, err
	}
	if !ValidDownloadMode(config.FolderPackMode, config.ShareDeliveryMode) {
		return sqlcgen.SystemSetting{}, ErrDownloadMode
	}
	if !validCodeConfig(config.InvitationLength, config.InvitationIncludeLetters, config.InvitationIncludeNumbers) ||
		!validCodeConfig(config.ShareLength, config.ShareIncludeLetters, config.ShareIncludeNumbers) {
		return sqlcgen.SystemSetting{}, ErrRandomCodeInvalid
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
	return r.q.UpdateAllSystemSettings(ctx, sqlcgen.UpdateAllSystemSettingsParams{
		SiteName: siteName, StoragePath: storagePath,
		FolderPackMode: config.FolderPackMode, ShareDeliveryMode: config.ShareDeliveryMode,
		InvitationLength: config.InvitationLength, InvitationCaseSensitive: config.InvitationCaseSensitive,
		InvitationIncludeLetters: config.InvitationIncludeLetters, InvitationIncludeNumbers: config.InvitationIncludeNumbers,
		ShareLength: config.ShareLength, ShareCaseSensitive: config.ShareCaseSensitive,
		ShareIncludeLetters: config.ShareIncludeLetters, ShareIncludeNumbers: config.ShareIncludeNumbers,
		UploadRequiresReview: config.UploadRequiresReview, CustomShareRequiresReview: config.CustomShareRequiresReview,
		UploadChunkSizeBytes:       config.UploadChunkSizeBytes,
		UploadTaskChunkConcurrency: config.UploadTaskChunkConcurrency,
		UploadUserTaskConcurrency:  config.UploadUserTaskConcurrency,
		TrashRetentionDays:         config.TrashRetentionDays,
	})
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

func ValidDownloadMode(packMode, deliveryMode string) bool {
	return (packMode == PackBackend || packMode == PackFrontend) &&
		(deliveryMode == DeliveryBlob || deliveryMode == DeliveryTemporary)
}

func validCodeConfig(length int16, letters, numbers bool) bool {
	return length >= 4 && length <= 64 && (letters || numbers)
}
