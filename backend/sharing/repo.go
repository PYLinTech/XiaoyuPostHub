// Package sharing 持久化分享页和文件直链。
package sharing

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("sharing: 链接不存在")
	ErrLimitReached = errors.New("sharing: 链接已失效或达到下载限制")
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
		WHERE owner_user_id = $1 AND is_active AND (expires_at IS NULL OR expires_at > NOW())`, ownerID).Scan(&count)
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
	var count int64
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+`
		WHERE owner_user_id=$1 AND id=ANY($2) AND NOT is_active
		AND (expires_at IS NULL OR expires_at > NOW())`, ownerID, ids).Scan(&count)
	return count, err
}

func (r *Repo) ListSharesByOwner(ctx context.Context, ownerID int64) ([]OwnerShareItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.id,s.token_value,s.password_value,s.expires_at,s.password_value IS NOT NULL,s.show_owner,
		       s.description,s.description_format,
		       s.download_limit,s.traffic_limit_bytes,s.download_count,s.traffic_used_bytes,
		       s.is_active,COALESCE(review.status,'approved'),COALESCE(review.reason,''),s.created_at,
		       (SELECT COUNT(*) FROM share_resources sr WHERE sr.share_id=s.id),
		       r.id,r.owner_user_id,r.parent_id,r.kind,r.name,r.storage_key,
		       r.size_bytes,r.sha256_checksum,r.mime_type,r.created_at,r.updated_at
		FROM shares s
		JOIN share_resources primary_link ON primary_link.share_id=s.id AND primary_link.display_order=0
		JOIN resources r ON r.id=primary_link.resource_id
		LEFT JOIN share_reviews review ON review.share_id=s.id
		WHERE s.owner_user_id=$1 ORDER BY s.id DESC LIMIT 500`, ownerID)
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
			&item.ID, &tokenValue, &item.Password, &item.ExpiresAt, &item.HasPassword, &item.ShowOwner,
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
		if tokenValue != nil {
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
		WHERE d.owner_user_id=$1 AND r.kind='file' ORDER BY d.id DESC LIMIT 500`, ownerID)
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
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO shares (
			token_value, owner_user_id, password_value, expires_at,
			show_owner, description, description_format, download_limit, traffic_limit_bytes
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id`, token, p.OwnerUserID, p.PasswordValue, p.ExpiresAt,
		p.ShowOwner, p.Description, p.DescriptionFormat, p.DownloadLimit, p.TrafficLimitBytes).Scan(&id)
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
	return created, token, err
}

func (r *Repo) GetShareByToken(ctx context.Context, token string) (Share, error) {
	return r.getShare(ctx, `s.token_value = $1`, token)
}

func (r *Repo) ReserveShareDownload(ctx context.Context, id, bytes int64) (bool, error) {
	var returnedID int64
	err := r.pool.QueryRow(ctx, `
		UPDATE shares
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
	query += ` WHERE id=$1 AND owner_user_id=$2`
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
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM `+table+` WHERE owner_user_id=$1 AND id=ANY($2)`, ownerID, ids).Scan(&count); err != nil {
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
	return r.getDirectLink(ctx, `d.token_value = $1`, token)
}

func (r *Repo) ReserveDirectDownload(ctx context.Context, id, bytes int64) (bool, error) {
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

// CreateDownloadJob 在同一事务内预留一次分享下载配额并创建短时下载任务。
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
	err = tx.QueryRow(ctx, `
		UPDATE shares
		SET download_count = download_count + 1,
		    traffic_used_bytes = traffic_used_bytes + $2
		WHERE id = $1 AND is_active
		  AND (expires_at IS NULL OR expires_at > NOW())
		  AND (download_limit IS NULL OR download_count + 1 <= download_limit)
		  AND (traffic_limit_bytes IS NULL OR traffic_used_bytes + $2 <= traffic_limit_bytes)
		RETURNING id`, p.ShareID, p.TotalBytes).Scan(&shareID)
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
		UPDATE share_download_jobs
		SET used_at = NOW()
		WHERE token_hash = $1 AND pack_mode = 'backend'
		  AND used_at IS NULL AND expires_at > NOW()
		RETURNING id, artifact_path, artifact_name, artifact_content_type,
		          total_bytes, artifact_temporary, artifact_sha256`, randomtoken.Hash(token)).Scan(
		&item.JobID, &item.Path, &item.Name, &item.ContentType, &item.SizeBytes, &item.Temporary, &item.SHA256,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DownloadArtifact{}, ErrNotFound
	}
	return item, err
}

func (r *Repo) ClaimDownloadJobFile(ctx context.Context, token, resourceID string) (DownloadJobFile, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE share_download_job_files f
		SET used_at = NOW()
		FROM share_download_jobs j, resources r
		WHERE f.job_id = j.id AND f.resource_id = r.id
		  AND j.token_hash = $1 AND j.pack_mode = 'frontend'
		  AND j.expires_at > NOW() AND f.used_at IS NULL AND f.resource_id = $2
		RETURNING j.id, f.relative_path,
		          r.id, r.owner_user_id, r.parent_id, r.kind, r.name, r.storage_key,
		          r.size_bytes, r.sha256_checksum, r.mime_type, r.created_at, r.updated_at`,
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

func (r *Repo) DeleteDownloadJob(ctx context.Context, id int64) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM share_download_jobs WHERE id = $1`, id)
	return err
}

// StartDownloadJobCleanup 清除无人领取且已经过期的临时 ZIP。
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
		WHERE `+predicate, arg)
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
		WHERE sr.share_id=$1 ORDER BY sr.display_order`, shareID)
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
		WHERE `+predicate, arg)
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
