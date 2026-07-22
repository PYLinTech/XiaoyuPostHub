// Package sharing 持久化分享页和文件直链。
package sharing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound             = errors.New("sharing: 链接不存在")
	ErrLimitReached         = errors.New("sharing: 链接已失效或达到下载限制")
	ErrAdminBlocked         = errors.New("sharing: 分享已被管理员封禁")
	ErrPickupCodesExhausted = errors.New("系统取件码已用尽，请联系系统管理员处理！")
)

type Share struct {
	ID                int64
	TokenValue        *string
	OwnerUserID       int64
	Resource          resource.Resource
	Resources         []resource.Resource
	OwnerUsername     string
	PasswordValue     *string
	ExpiresAt         *time.Time
	ShowOwner         bool
	Description       string
	DescriptionFormat string
	DownloadLimit     *int64
	TrafficLimitBytes *int64
	DownloadCount     int64
	TrafficUsedBytes  int64
	IsActive          bool
	CreatedAt         time.Time
	ShareType         string
	PickupCode        *string
}

type DirectLink struct {
	ID                int64
	TokenValue        *string
	OwnerUserID       int64
	Resource          resource.Resource
	ExpiresAt         *time.Time
	DownloadLimit     *int64
	TrafficLimitBytes *int64
	DownloadCount     int64
	TrafficUsedBytes  int64
	IsActive          bool
	CreatedAt         time.Time
}

type OwnerShareItem struct {
	ID                int64             `json:"id"`
	URL               *string           `json:"url,omitempty"`
	ShareType         string            `json:"shareType"`
	PickupCode        *string           `json:"pickupCode,omitempty"`
	Password          *string           `json:"password,omitempty"`
	Resource          resource.Resource `json:"resource"`
	ExpiresAt         *time.Time        `json:"expiresAt,omitempty"`
	HasPassword       bool              `json:"hasPassword"`
	ShowOwner         bool              `json:"showOwner"`
	Description       string            `json:"description"`
	DescriptionFormat string            `json:"descriptionFormat"`
	DownloadLimit     *int64            `json:"downloadLimit,omitempty"`
	TrafficLimitBytes *int64            `json:"trafficLimitBytes,omitempty"`
	DownloadCount     int64             `json:"downloadCount"`
	TrafficUsedBytes  int64             `json:"trafficUsedBytes"`
	IsActive          bool              `json:"isActive"`
	ReviewStatus      string            `json:"reviewStatus"`
	ReviewReason      string            `json:"reviewReason,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
}

type OwnerDirectLinkItem struct {
	ID                int64             `json:"id"`
	URL               *string           `json:"url,omitempty"`
	Resource          resource.Resource `json:"resource"`
	ExpiresAt         *time.Time        `json:"expiresAt,omitempty"`
	DownloadLimit     *int64            `json:"downloadLimit,omitempty"`
	TrafficLimitBytes *int64            `json:"trafficLimitBytes,omitempty"`
	DownloadCount     int64             `json:"downloadCount"`
	TrafficUsedBytes  int64             `json:"trafficUsedBytes"`
	IsActive          bool              `json:"isActive"`
	CreatedAt         time.Time         `json:"createdAt"`
}

type CreateShareParams struct {
	OwnerUserID       int64
	ResourceIDs       []string
	PasswordValue     *string
	ExpiresAt         *time.Time
	ShowOwner         bool
	Description       string
	DescriptionFormat string
	DownloadLimit     *int64
	TrafficLimitBytes *int64
	ShareType         string
	PickupOptions     randomtoken.CodeOptions
}

type CreateDirectLinkParams struct {
	OwnerUserID       int64
	ResourceID        string
	ExpiresAt         *time.Time
	DownloadLimit     *int64
	TrafficLimitBytes *int64
}

type UpdateShareParams struct {
	OwnerUserID       int64
	ID                int64
	UpdateExpiresAt   bool
	ExpiresAt         *time.Time
	UpdatePassword    bool
	PasswordValue     *string
	ShowOwner         bool
	Description       string
	DescriptionFormat string
	DownloadLimit     *int64
	TrafficLimitBytes *int64
}

type UpdateDirectLinkParams struct {
	OwnerUserID       int64
	ID                int64
	UpdateExpiresAt   bool
	ExpiresAt         *time.Time
	DownloadLimit     *int64
	TrafficLimitBytes *int64
}

type DownloadJobFileParam struct {
	ResourceID   string
	RelativePath string
}

type CreateDownloadJobParams struct {
	ShareID             int64
	PackMode            string
	DeliveryMode        string
	ArtifactPath        *string
	ArtifactName        *string
	ArtifactContentType *string
	ArtifactSHA256      *string
	ArtifactTemporary   bool
	TotalBytes          int64
	ExpiresAt           time.Time
	Files               []DownloadJobFileParam
}

type DownloadArtifact struct {
	JobID       int64
	Path        string
	Name        string
	ContentType string
	SizeBytes   int64
	Temporary   bool
	SHA256      string
}

type DownloadJobFile struct {
	JobID        int64
	RelativePath string
	Resource     resource.Resource
}

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) CountActiveSharesByOwner(ctx context.Context, ownerID int64) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM shares
		WHERE owner_user_id = $1 AND is_active AND NOT admin_blocked AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > NOW())`, ownerID).Scan(&count)
	return count, err
}

func (r *Repo) CountActiveDirectLinksByOwner(ctx context.Context, ownerID int64) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM direct_links
		WHERE owner_user_id = $1 AND is_active AND (expires_at IS NULL OR expires_at > NOW())`, ownerID).Scan(&count)
	return count, err
}

func (r *Repo) CountSharesToEnableByOwner(ctx context.Context, ownerID int64, ids []int64) (int64, error) {
	return r.countLinksToEnableByOwner(ctx, "shares", ownerID, ids)
}

func (r *Repo) CountDirectLinksToEnableByOwner(ctx context.Context, ownerID int64, ids []int64) (int64, error) {
	return r.countLinksToEnableByOwner(ctx, "direct_links", ownerID, ids)
}

func (r *Repo) countLinksToEnableByOwner(ctx context.Context, table string, ownerID int64, ids []int64) (int64, error) {
	condition := ""
	if table == "shares" {
		condition = " AND NOT admin_blocked AND deleted_at IS NULL"
	}
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+`
		WHERE owner_user_id=$1 AND id=ANY($2) AND NOT is_active
		AND (expires_at IS NULL OR expires_at > NOW())`+condition, ownerID, ids).Scan(&count)
	return count, err
}

func (r *Repo) ListSharesByOwner(ctx context.Context, ownerID int64) ([]OwnerShareItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.id,s.token_value,s.share_type,s.pickup_code,s.password_value,s.expires_at,s.password_value IS NOT NULL,s.show_owner,
		       s.description,s.description_format,
		       s.download_limit,s.traffic_limit_bytes,s.download_count,s.traffic_used_bytes,
		       s.is_active,COALESCE(review.status,'approved'),COALESCE(review.reason,''),s.created_at,
		       (SELECT COUNT(*) FROM share_resources sr JOIN resources counted ON counted.id=sr.resource_id WHERE sr.share_id=s.id AND counted.trashed_at IS NULL),
		       r.id,r.owner_user_id,r.parent_id,r.kind,r.name,r.storage_key,
		       r.size_bytes,r.sha256_checksum,r.mime_type,r.created_at,r.updated_at
		FROM shares s
		JOIN share_resources primary_link ON primary_link.share_id=s.id AND primary_link.display_order=0
		JOIN resources r ON r.id=primary_link.resource_id
		LEFT JOIN share_moderations review ON review.share_id=s.id
		WHERE s.owner_user_id=$1 AND s.deleted_at IS NULL AND r.trashed_at IS NULL ORDER BY s.id DESC LIMIT 500`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OwnerShareItem, 0)
	for rows.Next() {
		var item OwnerShareItem
		var tokenValue *string
		var resourceCount int
		if err := rows.Scan(
			&item.ID, &tokenValue, &item.ShareType, &item.PickupCode, &item.Password, &item.ExpiresAt, &item.HasPassword, &item.ShowOwner,
			&item.Description, &item.DescriptionFormat,
			&item.DownloadLimit, &item.TrafficLimitBytes, &item.DownloadCount,
			&item.TrafficUsedBytes, &item.IsActive, &item.ReviewStatus, &item.ReviewReason,
			&item.CreatedAt, &resourceCount,
			&item.Resource.ID, &item.Resource.OwnerUserID, &item.Resource.ParentID,
			&item.Resource.Kind, &item.Resource.Name, &item.Resource.StorageKey,
			&item.Resource.SizeBytes, &item.Resource.SHA256Checksum, &item.Resource.MimeType,
			&item.Resource.CreatedAt, &item.Resource.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if tokenValue != nil && item.ShareType == "link" {
			url := "/s/" + *tokenValue
			item.URL = &url
		}
		if resourceCount > 1 {
			item.Resource.Kind = resource.KindFolder
			item.Resource.Name = fmt.Sprintf("%d 项内容", resourceCount)
			item.Resource.SizeBytes = 0
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repo) ListDirectLinksByOwner(ctx context.Context, ownerID int64) ([]OwnerDirectLinkItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT d.id,d.token_value,d.expires_at,d.download_limit,d.traffic_limit_bytes,
		       d.download_count,d.traffic_used_bytes,d.is_active,d.created_at,
		       r.id,r.owner_user_id,r.parent_id,r.kind,r.name,r.storage_key,
		       r.size_bytes,r.sha256_checksum,r.mime_type,r.created_at,r.updated_at
		FROM direct_links d JOIN resources r ON r.id=d.resource_id
		WHERE d.owner_user_id=$1 AND r.kind='file' AND r.trashed_at IS NULL ORDER BY d.id DESC LIMIT 500`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]OwnerDirectLinkItem, 0)
	for rows.Next() {
		var item OwnerDirectLinkItem
		var tokenValue *string
		if err := rows.Scan(
			&item.ID, &tokenValue, &item.ExpiresAt, &item.DownloadLimit, &item.TrafficLimitBytes,
			&item.DownloadCount, &item.TrafficUsedBytes, &item.IsActive, &item.CreatedAt,
			&item.Resource.ID, &item.Resource.OwnerUserID, &item.Resource.ParentID,
			&item.Resource.Kind, &item.Resource.Name, &item.Resource.StorageKey,
			&item.Resource.SizeBytes, &item.Resource.SHA256Checksum, &item.Resource.MimeType,
			&item.Resource.CreatedAt, &item.Resource.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if tokenValue != nil {
			url := "/d/" + *tokenValue
			item.URL = &url
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repo) CreateShare(ctx context.Context, p CreateShareParams) (Share, string, error) {
	if len(p.ResourceIDs) == 0 {
		return Share{}, "", ErrNotFound
	}
	token, err := randomtoken.New(32)
	if err != nil {
		return Share{}, "", err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Share{}, "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	shareType := p.ShareType
	if shareType == "" {
		shareType = "link"
	}
	var id int64
	identifier := token
	if shareType == "pickup" {
		for attempt := 0; attempt < 256; attempt++ {
			code, codeErr := randomtoken.NewCode(p.PickupOptions)
			if codeErr != nil {
				return Share{}, "", codeErr
			}
			err = tx.QueryRow(ctx, `INSERT INTO shares (
				token_value,owner_user_id,password_value,expires_at,show_owner,description,
				description_format,download_limit,traffic_limit_bytes,share_type,pickup_code,pickup_case_sensitive
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pickup',$10,$11)
			ON CONFLICT (pickup_code) WHERE pickup_code IS NOT NULL DO NOTHING RETURNING id`,
				token, p.OwnerUserID, p.PasswordValue, p.ExpiresAt, p.ShowOwner, p.Description,
				p.DescriptionFormat, p.DownloadLimit, p.TrafficLimitBytes, code, p.PickupOptions.CaseSensitive).Scan(&id)
			if err == nil {
				identifier = code
				break
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return Share{}, "", err
			}
		}
		if id == 0 {
			return Share{}, "", ErrPickupCodesExhausted
		}
	} else {
		err = tx.QueryRow(ctx, `INSERT INTO shares (
			token_value,owner_user_id,password_value,expires_at,show_owner,description,
			description_format,download_limit,traffic_limit_bytes,share_type
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'link') RETURNING id`, token, p.OwnerUserID,
			p.PasswordValue, p.ExpiresAt, p.ShowOwner, p.Description, p.DescriptionFormat,
			p.DownloadLimit, p.TrafficLimitBytes).Scan(&id)
	}
	if err != nil {
		return Share{}, "", err
	}
	for index, resourceID := range p.ResourceIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO share_resources(share_id,resource_id,display_order) VALUES($1,$2,$3)`, id, resourceID, index); err != nil {
			return Share{}, "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Share{}, "", err
	}
	created, err := r.getShareByID(ctx, id)
	return created, identifier, err
}

func (r *Repo) GetShareByPickupCode(ctx context.Context, code string) (Share, error) {
	code = strings.TrimSpace(code)
	return r.getShare(ctx, `s.share_type='pickup' AND NOT s.admin_blocked AND s.deleted_at IS NULL AND (s.pickup_code=$1 OR (NOT s.pickup_case_sensitive AND s.pickup_code=UPPER($1)))`, code)
}

func (r *Repo) GetShareByToken(ctx context.Context, token string) (Share, error) {
	var blocked, deleted bool
	err := r.pool.QueryRow(ctx, `SELECT admin_blocked,deleted_at IS NOT NULL FROM shares WHERE token_value=$1`, token).Scan(&blocked, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || deleted {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	if blocked {
		return Share{}, ErrAdminBlocked
	}
	return r.getShare(ctx, `s.token_value = $1`, token)
}

func (r *Repo) CreateDirectLink(ctx context.Context, p CreateDirectLinkParams) (DirectLink, string, error) {
	token, err := randomtoken.New(32)
	if err != nil {
		return DirectLink{}, "", err
	}
	var id int64
	err = r.pool.QueryRow(ctx, `
		INSERT INTO direct_links (
			token_value, owner_user_id, resource_id, expires_at,
			download_limit, traffic_limit_bytes
		) VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id`, token, p.OwnerUserID, p.ResourceID, p.ExpiresAt,
		p.DownloadLimit, p.TrafficLimitBytes).Scan(&id)
	if err != nil {
		return DirectLink{}, "", err
	}
	created, err := r.getDirectLinkByID(ctx, id)
	return created, token, err
}

func (r *Repo) UpdateShareByOwner(ctx context.Context, p UpdateShareParams) error {
	query := `UPDATE shares SET
		expires_at=CASE WHEN $3 THEN $4 ELSE expires_at END,
		show_owner=$5,description=$6,description_format=$7,
		download_limit=$8,traffic_limit_bytes=$9`
	args := []any{p.ID, p.OwnerUserID, p.UpdateExpiresAt, p.ExpiresAt, p.ShowOwner,
		p.Description, p.DescriptionFormat, p.DownloadLimit, p.TrafficLimitBytes}
	if p.UpdatePassword {
		query += `,password_value=$10`
		args = append(args, p.PasswordValue)
	}
	query += ` WHERE id=$1 AND owner_user_id=$2 AND NOT admin_blocked AND deleted_at IS NULL`
	result, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) UpdateDirectLinkByOwner(ctx context.Context, p UpdateDirectLinkParams) error {
	result, err := r.pool.Exec(ctx, `UPDATE direct_links SET
		expires_at=CASE WHEN $3 THEN $4 ELSE expires_at END,
		download_limit=$5,traffic_limit_bytes=$6
		WHERE id=$1 AND owner_user_id=$2`, p.ID, p.OwnerUserID, p.UpdateExpiresAt,
		p.ExpiresAt, p.DownloadLimit, p.TrafficLimitBytes)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) BatchSharesByOwner(ctx context.Context, ownerID int64, ids []int64, action string) error {
	return r.batchLinksByOwner(ctx, "shares", ownerID, ids, action)
}

func (r *Repo) BatchDirectLinksByOwner(ctx context.Context, ownerID int64, ids []int64, action string) error {
	return r.batchLinksByOwner(ctx, "direct_links", ownerID, ids, action)
}

func (r *Repo) batchLinksByOwner(ctx context.Context, table string, ownerID int64, ids []int64, action string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var count int
	condition := ""
	if table == "shares" {
		condition = " AND NOT admin_blocked AND deleted_at IS NULL"
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE owner_user_id=$1 AND id=ANY($2)`+condition, ownerID, ids).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return ErrNotFound
	}
	var artifactPaths []string
	if table == "shares" && action == "delete" {
		rows, err := tx.Query(ctx, `SELECT j.artifact_path FROM share_download_jobs j
			JOIN shares s ON s.id=j.share_id
			WHERE s.owner_user_id=$1 AND s.id=ANY($2) AND j.artifact_temporary AND j.artifact_path IS NOT NULL`, ownerID, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				rows.Close()
				return err
			}
			artifactPaths = append(artifactPaths, path)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	var command string
	switch action {
	case "enable":
		command = `UPDATE ` + table + ` SET is_active=TRUE WHERE owner_user_id=$1 AND id=ANY($2)`
	case "disable":
		command = `UPDATE ` + table + ` SET is_active=FALSE WHERE owner_user_id=$1 AND id=ANY($2)`
	case "delete":
		command = `DELETE FROM ` + table + ` WHERE owner_user_id=$1 AND id=ANY($2)`
	default:
		return errors.New("sharing: 不支持的批量操作")
	}
	if _, err := tx.Exec(ctx, command, ownerID, ids); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	for _, path := range artifactPaths {
		_ = os.Remove(path)
	}
	return nil
}

func (r *Repo) GetDirectLinkByToken(ctx context.Context, token string) (DirectLink, error) {
	var blocked, deleted bool
	err := r.pool.QueryRow(ctx, `SELECT admin_blocked,deleted_at IS NOT NULL FROM direct_links WHERE token_value=$1`, token).Scan(&blocked, &deleted)
	if errors.Is(err, pgx.ErrNoRows) || deleted {
		return DirectLink{}, ErrNotFound
	}
	if err != nil {
		return DirectLink{}, err
	}
	if blocked {
		return DirectLink{}, ErrAdminBlocked
	}
	return r.getDirectLink(ctx, `d.token_value = $1`, token)
}

func (r *Repo) CompleteDirectDownload(ctx context.Context, id, bytes int64) (bool, error) {
	var returnedID int64
	err := r.pool.QueryRow(ctx, `
		UPDATE direct_links
		SET download_count = download_count + 1,
		    traffic_used_bytes = traffic_used_bytes + $2
		WHERE id = $1
		  AND is_active
		  AND (expires_at IS NULL OR expires_at > NOW())
		  AND (download_limit IS NULL OR download_count + 1 <= download_limit)
		  AND (traffic_limit_bytes IS NULL OR traffic_used_bytes + $2 <= traffic_limit_bytes)
		RETURNING id`, id, bytes).Scan(&returnedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CreateDownloadJob 只创建短时下载任务；下载次数在任务完整取流后提交。
func (r *Repo) CreateDownloadJob(ctx context.Context, p CreateDownloadJobParams) (string, error) {
	token, err := randomtoken.New(32)
	if err != nil {
		return "", err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var shareID int64
	err = tx.QueryRow(ctx, `SELECT id FROM shares
		WHERE id=$1 AND is_active AND NOT admin_blocked AND deleted_at IS NULL
		  AND (expires_at IS NULL OR expires_at > NOW())`, p.ShareID).Scan(&shareID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrLimitReached
	}
	if err != nil {
		return "", err
	}

	var jobID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO share_download_jobs (
			token_hash, share_id, pack_mode, delivery_mode,
			artifact_path, artifact_name, artifact_content_type, artifact_sha256, artifact_temporary,
			total_bytes, expires_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id`, randomtoken.Hash(token), p.ShareID,
		p.PackMode, p.DeliveryMode, p.ArtifactPath, p.ArtifactName,
		p.ArtifactContentType, p.ArtifactSHA256, p.ArtifactTemporary, p.TotalBytes, p.ExpiresAt).Scan(&jobID)
	if err != nil {
		return "", err
	}
	for _, file := range p.Files {
		if _, err := tx.Exec(ctx, `
			INSERT INTO share_download_job_files (job_id, resource_id, relative_path)
			VALUES ($1,$2,$3)`, jobID, file.ResourceID, file.RelativePath); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return token, nil
}

func (r *Repo) ClaimDownloadArtifact(ctx context.Context, token string) (DownloadArtifact, error) {
	var item DownloadArtifact
	err := r.pool.QueryRow(ctx, `
		SELECT id, artifact_path, artifact_name, artifact_content_type,
		       total_bytes, artifact_temporary, artifact_sha256
		FROM share_download_jobs
		WHERE token_hash = $1 AND pack_mode = 'backend' AND expires_at > NOW()`, randomtoken.Hash(token)).Scan(
		&item.JobID, &item.Path, &item.Name, &item.ContentType, &item.SizeBytes, &item.Temporary, &item.SHA256,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DownloadArtifact{}, ErrNotFound
	}
	return item, err
}

func (r *Repo) ClaimDownloadJobFile(ctx context.Context, token, resourceID string) (DownloadJobFile, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT j.id, f.relative_path,
		       r.id, r.owner_user_id, r.parent_id, r.kind, r.name, r.storage_key,
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.created_at, r.updated_at
		FROM share_download_job_files f
		JOIN share_download_jobs j ON j.id=f.job_id
		JOIN resources r ON r.id=f.resource_id
		WHERE f.job_id = j.id AND f.resource_id = r.id
		  AND j.token_hash = $1 AND j.pack_mode = 'frontend'
		  AND j.expires_at > NOW() AND f.resource_id = $2`,
		randomtoken.Hash(token), resourceID)
	var item DownloadJobFile
	err := row.Scan(
		&item.JobID, &item.RelativePath,
		&item.Resource.ID, &item.Resource.OwnerUserID, &item.Resource.ParentID,
		&item.Resource.Kind, &item.Resource.Name, &item.Resource.StorageKey,
		&item.Resource.SizeBytes, &item.Resource.SHA256Checksum, &item.Resource.MimeType,
		&item.Resource.CreatedAt, &item.Resource.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DownloadJobFile{}, ErrNotFound
	}
	return item, err
}

// ReserveDownloadJob 在首次取流前占用下载资格，但不增加已完成次数。
// 并发任务会计入占用，防止多个任务同时越过次数或流量上限。
func (r *Repo) ReserveDownloadJob(ctx context.Context, jobID int64) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var shareID, totalBytes int64
	var reservedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT share_id,total_bytes,reserved_at FROM share_download_jobs
		WHERE id=$1 AND expires_at>NOW() FOR UPDATE`, jobID).Scan(&shareID, &totalBytes, &reservedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if reservedAt == nil {
		// 分开锁定分享行与策略查询，确保等待并发事务后使用新的 READ COMMITTED
		// 快照统计其他已占用任务，不允许两个新任务同时穿透最后一次额度。
		var lockedShareID int64
		if err := tx.QueryRow(ctx, `SELECT id FROM shares WHERE id=$1 FOR UPDATE`, shareID).Scan(&lockedShareID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT s.is_active AND NOT s.admin_blocked AND s.deleted_at IS NULL
			AND (s.expires_at IS NULL OR s.expires_at>NOW())
			AND (s.download_limit IS NULL OR s.download_count + (
				SELECT COUNT(*) FROM share_download_jobs pending
				WHERE pending.share_id=s.id AND pending.reserved_at IS NOT NULL
				  AND pending.completed_at IS NULL AND pending.expires_at>NOW()
			) + 1 <= s.download_limit)
			AND (s.traffic_limit_bytes IS NULL OR s.traffic_used_bytes + COALESCE((
				SELECT SUM(pending.total_bytes) FROM share_download_jobs pending
				WHERE pending.share_id=s.id AND pending.reserved_at IS NOT NULL
				  AND pending.completed_at IS NULL AND pending.expires_at>NOW()
			),0) + $2 <= s.traffic_limit_bytes)
			FROM shares s WHERE s.id=$1`, shareID, totalBytes).Scan(&allowed)
		if errors.Is(err, pgx.ErrNoRows) || !allowed {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// 创建后的 5 分钟是“开始下载”窗口；一旦开始，给大文件与慢速连接
		// 24 小时完成分片，避免下载途中任务自然过期而永远无法提交计数。
		if _, err := tx.Exec(ctx, `UPDATE share_download_jobs
			SET reserved_at=NOW(),expires_at=GREATEST(expires_at,NOW()+INTERVAL '24 hours') WHERE id=$1`, jobID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RecordDownloadRange 记录一次已完整写给客户端的字节区间。只有任务内每个对象均
// 从 0 到末字节无缺口覆盖时，才原子增加一次下载次数和完整文件流量。
func (r *Repo) RecordDownloadRange(ctx context.Context, jobID int64, objectKey string, start, end int64) (bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var shareID, totalBytes int64
	var completedAt *time.Time
	var packMode string
	err = tx.QueryRow(ctx, `SELECT share_id,total_bytes,pack_mode,completed_at FROM share_download_jobs
		WHERE id=$1 AND reserved_at IS NOT NULL AND expires_at>NOW() FOR UPDATE`, jobID).Scan(&shareID, &totalBytes, &packMode, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if completedAt != nil {
		return true, tx.Commit(ctx)
	}
	if end >= start {
		// 在同一个 job 行锁内合并重叠或相邻区间，避免并行分片与重试持续堆积记录。
		rows, queryErr := tx.Query(ctx, `SELECT range_start,range_end FROM share_download_job_ranges
			WHERE job_id=$1 AND object_key=$2 AND range_start <= $4+1 AND range_end+1 >= $3`, jobID, objectKey, start, end)
		if queryErr != nil {
			return false, queryErr
		}
		for rows.Next() {
			var existingStart, existingEnd int64
			if err := rows.Scan(&existingStart, &existingEnd); err != nil {
				rows.Close()
				return false, err
			}
			if existingStart < start {
				start = existingStart
			}
			if existingEnd > end {
				end = existingEnd
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
		if _, err = tx.Exec(ctx, `DELETE FROM share_download_job_ranges
			WHERE job_id=$1 AND object_key=$2 AND range_start <= $4+1 AND range_end+1 >= $3`, jobID, objectKey, start, end); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO share_download_job_ranges(job_id,object_key,range_start,range_end)
			VALUES($1,$2,$3,$4)`, jobID, objectKey, start, end); err != nil {
			return false, err
		}
	}

	expected := map[string]int64{"artifact": totalBytes}
	if packMode == "frontend" {
		expected = make(map[string]int64)
		rows, queryErr := tx.Query(ctx, `SELECT f.resource_id,r.size_bytes FROM share_download_job_files f
			JOIN resources r ON r.id=f.resource_id WHERE f.job_id=$1`, jobID)
		if queryErr != nil {
			return false, queryErr
		}
		for rows.Next() {
			var key string
			var size int64
			if err := rows.Scan(&key, &size); err != nil {
				rows.Close()
				return false, err
			}
			expected[key] = size
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		rows.Close()
	}
	ranges, err := tx.Query(ctx, `SELECT object_key,range_start,range_end FROM share_download_job_ranges WHERE job_id=$1 ORDER BY object_key,range_start,range_end`, jobID)
	if err != nil {
		return false, err
	}
	covered := make(map[string]int64)
	for ranges.Next() {
		var key string
		var from, to int64
		if err := ranges.Scan(&key, &from, &to); err != nil {
			ranges.Close()
			return false, err
		}
		if from <= covered[key] {
			if to+1 > covered[key] {
				covered[key] = to + 1
			}
		}
	}
	if err := ranges.Err(); err != nil {
		ranges.Close()
		return false, err
	}
	ranges.Close()
	for key, size := range expected {
		if size > 0 && covered[key] < size {
			return false, tx.Commit(ctx)
		}
	}
	result, err := tx.Exec(ctx, `UPDATE shares SET download_count=download_count+1,traffic_used_bytes=traffic_used_bytes+$2
		WHERE id=$1 AND is_active AND NOT admin_blocked AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at>NOW())`, shareID, totalBytes)
	if err != nil {
		return false, err
	}
	if result.RowsAffected() == 0 {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `UPDATE share_download_jobs SET completed_at=NOW() WHERE id=$1`, jobID); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// StartDownloadJobCleanup 清除已经过期的临时 ZIP 和任务进度。
func (r *Repo) StartDownloadJobCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rows, err := r.pool.Query(ctx, `
				DELETE FROM share_download_jobs
				WHERE expires_at <= NOW()
				RETURNING artifact_path, artifact_temporary`)
			if err != nil {
				log.Printf("清理过期下载任务失败：%v", err)
				continue
			}
			for rows.Next() {
				var path *string
				var temporary bool
				if err := rows.Scan(&path, &temporary); err == nil && temporary && path != nil {
					_ = os.Remove(*path)
				}
			}
			rows.Close()
		}
	}
}

func (r *Repo) getShareByID(ctx context.Context, id int64) (Share, error) {
	return r.getShare(ctx, `s.id = $1`, id)
}

func (r *Repo) getShare(ctx context.Context, predicate string, arg any) (Share, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT s.id, s.token_value, s.owner_user_id, s.password_value, s.expires_at,
		       s.show_owner, s.description, s.description_format,
		       s.download_limit, s.traffic_limit_bytes, s.download_count,
		       s.traffic_used_bytes, s.is_active, s.created_at,
		       u.username,
		       r.id, r.owner_user_id, r.parent_id, r.kind, r.name, r.storage_key,
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.created_at, r.updated_at
		FROM shares s
		JOIN users u ON u.id = s.owner_user_id
		JOIN share_resources primary_link ON primary_link.share_id=s.id AND primary_link.display_order=0
		JOIN resources r ON r.id = primary_link.resource_id
		WHERE (`+predicate+`) AND r.trashed_at IS NULL`, arg)
	var item Share
	err := row.Scan(
		&item.ID, &item.TokenValue, &item.OwnerUserID, &item.PasswordValue, &item.ExpiresAt,
		&item.ShowOwner, &item.Description, &item.DescriptionFormat,
		&item.DownloadLimit, &item.TrafficLimitBytes, &item.DownloadCount,
		&item.TrafficUsedBytes, &item.IsActive, &item.CreatedAt,
		&item.OwnerUsername,
		&item.Resource.ID, &item.Resource.OwnerUserID, &item.Resource.ParentID,
		&item.Resource.Kind, &item.Resource.Name, &item.Resource.StorageKey,
		&item.Resource.SizeBytes, &item.Resource.SHA256Checksum, &item.Resource.MimeType,
		&item.Resource.CreatedAt, &item.Resource.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Share{}, ErrNotFound
	}
	if err != nil {
		return Share{}, err
	}
	item.Resources, err = r.listShareResources(ctx, item.ID)
	if err != nil {
		return Share{}, err
	}
	return item, nil
}

func (r *Repo) listShareResources(ctx context.Context, shareID int64) ([]resource.Resource, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id,r.owner_user_id,r.parent_id,r.kind,r.name,r.storage_key,
		       r.size_bytes,r.sha256_checksum,r.mime_type,r.created_at,r.updated_at
		FROM share_resources sr JOIN resources r ON r.id=sr.resource_id
		WHERE sr.share_id=$1 AND r.trashed_at IS NULL ORDER BY sr.display_order`, shareID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]resource.Resource, 0)
	for rows.Next() {
		var item resource.Resource
		if err := rows.Scan(&item.ID, &item.OwnerUserID, &item.ParentID, &item.Kind, &item.Name,
			&item.StorageKey, &item.SizeBytes, &item.SHA256Checksum, &item.MimeType,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repo) getDirectLinkByID(ctx context.Context, id int64) (DirectLink, error) {
	return r.getDirectLink(ctx, `d.id = $1`, id)
}

func (r *Repo) getDirectLink(ctx context.Context, predicate string, arg any) (DirectLink, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT d.id, d.token_value, d.owner_user_id, d.expires_at,
		       d.download_limit, d.traffic_limit_bytes, d.download_count,
		       d.traffic_used_bytes, d.is_active, d.created_at,
		       r.id, r.owner_user_id, r.parent_id, r.kind, r.name, r.storage_key,
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.created_at, r.updated_at
		FROM direct_links d
		JOIN resources r ON r.id = d.resource_id
		WHERE (`+predicate+`) AND r.trashed_at IS NULL`, arg)
	var item DirectLink
	err := row.Scan(
		&item.ID, &item.TokenValue, &item.OwnerUserID, &item.ExpiresAt,
		&item.DownloadLimit, &item.TrafficLimitBytes, &item.DownloadCount,
		&item.TrafficUsedBytes, &item.IsActive, &item.CreatedAt,
		&item.Resource.ID, &item.Resource.OwnerUserID, &item.Resource.ParentID,
		&item.Resource.Kind, &item.Resource.Name, &item.Resource.StorageKey,
		&item.Resource.SizeBytes, &item.Resource.SHA256Checksum, &item.Resource.MimeType,
		&item.Resource.CreatedAt, &item.Resource.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DirectLink{}, ErrNotFound
	}
	return item, err
}
