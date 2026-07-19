// Package admin 提供管理面板只读统计与审计持久化。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrUserNotFound    = errors.New("用户不存在")
	ErrGroupNotFound   = errors.New("用户组不存在")
	ErrGroupNameExists = errors.New("用户组名称已存在")
	ErrGroupInput      = errors.New("用户组名称格式不正确")
	ErrGroupIsSystem   = errors.New("系统用户组不能修改或删除")
)

type Overview struct {
	UserCount             int64 `json:"userCount"`
	FileCount             int64 `json:"fileCount"`
	FolderCount           int64 `json:"folderCount"`
	StorageUsedBytes      int64 `json:"storageUsedBytes"`
	StorageAvailableBytes int64 `json:"storageAvailableBytes"`
	StorageTotalBytes     int64 `json:"storageTotalBytes"`
	ActiveShareCount      int64 `json:"activeShareCount"`
	ActiveDirectCount     int64 `json:"activeDirectCount"`
	ShareDownloadCount    int64 `json:"shareDownloadCount"`
	ShareTrafficBytes     int64 `json:"shareTrafficBytes"`
}

type UserItem struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	GroupIDs  []int64   `json:"groupIds"`
	Groups    []string  `json:"groups"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"createdAt"`
}

type UserGroupItem struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	IsSystem    bool      `json:"isSystem"`
	CreatedAt   time.Time `json:"createdAt"`
}

type QuotaItem struct {
	ID                    int64   `json:"id"`
	Name                  string  `json:"name"`
	Description           *string `json:"description,omitempty"`
	StorageBytesLimit     *int64  `json:"storageBytesLimit,omitempty"`
	SingleFileBytesLimit  *int64  `json:"singleFileBytesLimit,omitempty"`
	DailyUploadBytesLimit *int64  `json:"dailyUploadBytesLimit,omitempty"`
	DailyUploadCountLimit *int64  `json:"dailyUploadCountLimit,omitempty"`
	ActiveShareCountLimit *int64  `json:"activeShareCountLimit,omitempty"`
	ActiveDirectLinkLimit *int64  `json:"activeDirectLinkLimit,omitempty"`
	IsSystem              bool    `json:"isSystem"`
}

type AccessGroupItem struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	Description    *string  `json:"description,omitempty"`
	IsSystem       bool     `json:"isSystem"`
	QuotaProfileID *int64   `json:"quotaProfileId,omitempty"`
	Priority       int32    `json:"priority"`
	Permissions    []string `json:"permissions"`
}

type AuditItem struct {
	ID          int64           `json:"id"`
	ActorName   string          `json:"actorName"`
	Action      string          `json:"action"`
	TargetType  string          `json:"targetType"`
	TargetLabel string          `json:"targetLabel"`
	Details     json.RawMessage `json:"details"`
	ClientIP    *string         `json:"clientIp,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type ReviewSettings struct {
	UploadRequiresReview      bool `json:"uploadRequiresReview"`
	CustomShareRequiresReview bool `json:"customShareRequiresReview"`
}

type FileReviewItem struct {
	ResourceID   string     `json:"resourceId"`
	TaskID       string     `json:"taskId"`
	Name         string     `json:"name"`
	RelativePath string     `json:"relativePath"`
	SizeBytes    int64      `json:"sizeBytes"`
	MimeType     *string    `json:"mimeType,omitempty"`
	OwnerUserID  int64      `json:"-"`
	OwnerName    string     `json:"ownerName"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason"`
	DeleteFile   bool       `json:"deleteFile"`
	Blocked      bool       `json:"blocked"`
	Exists       bool       `json:"exists"`
	TrashedAt    *time.Time `json:"trashedAt,omitempty"`
	SubmittedAt  time.Time  `json:"submittedAt"`
	ReviewedAt   *time.Time `json:"reviewedAt,omitempty"`
	Reviewer     *string    `json:"reviewer,omitempty"`
}

type ShareReviewItem struct {
	ShareID           int64      `json:"shareId"`
	Token             string     `json:"token"`
	OwnerName         string     `json:"ownerName"`
	OwnerUserID       int64      `json:"-"`
	ResourceName      string     `json:"resourceName"`
	Description       string     `json:"description"`
	DescriptionFormat string     `json:"descriptionFormat"`
	Status            string     `json:"status"`
	Reason            string     `json:"reason"`
	DeleteLink        bool       `json:"deleteLink"`
	Blocked           bool       `json:"blocked"`
	DeletedAt         *time.Time `json:"deletedAt,omitempty"`
	SubmittedAt       time.Time  `json:"submittedAt"`
	ReviewedAt        *time.Time `json:"reviewedAt,omitempty"`
	Reviewer          *string    `json:"reviewer,omitempty"`
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) GetOverview(ctx context.Context, storagePath string) (Overview, error) {
	var out Overview
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM users),
			(SELECT COUNT(*) FROM resources WHERE kind='file' AND trashed_at IS NULL),
			(SELECT COUNT(*) FROM resources WHERE kind='folder' AND trashed_at IS NULL),
			(SELECT COUNT(*) FROM shares WHERE is_active AND (expires_at IS NULL OR expires_at>NOW())),
			(SELECT COUNT(*) FROM direct_links WHERE is_active AND (expires_at IS NULL OR expires_at>NOW())),
			(SELECT COALESCE(SUM(download_count),0)::BIGINT FROM shares),
			(SELECT COALESCE(SUM(traffic_used_bytes),0)::BIGINT FROM shares)`).Scan(
		&out.UserCount, &out.FileCount, &out.FolderCount,
		&out.ActiveShareCount, &out.ActiveDirectCount, &out.ShareDownloadCount, &out.ShareTrafficBytes,
	)
	if err != nil {
		return Overview{}, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(storagePath, &fs); err != nil {
		return Overview{}, err
	}
	blockSize := uint64(fs.Bsize)
	total := uint64(fs.Blocks) * blockSize
	free := uint64(fs.Bfree) * blockSize
	available := uint64(fs.Bavail) * blockSize
	out.StorageTotalBytes = int64(total)
	out.StorageUsedBytes = int64(total - free)
	out.StorageAvailableBytes = int64(available)
	return out, nil
}

func (r *Repo) ListUsers(ctx context.Context) ([]UserItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT u.id,u.username,u.created_at,u.is_disabled,
		       COALESCE(array_agg(g.id ORDER BY g.name) FILTER (WHERE g.id IS NOT NULL),'{}'::BIGINT[]),
		       COALESCE(array_agg(g.name ORDER BY g.name) FILTER (WHERE g.name IS NOT NULL),'{}'::TEXT[])
		FROM users u
		LEFT JOIN user_group_memberships m ON m.user_id=u.id
		LEFT JOIN user_groups g ON g.id=m.group_id
		GROUP BY u.id ORDER BY u.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UserItem, 0)
	for rows.Next() {
		var item UserItem
		if err := rows.Scan(&item.ID, &item.Username, &item.CreatedAt, &item.Disabled, &item.GroupIDs, &item.Groups); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repo) ListUserGroups(ctx context.Context) ([]UserGroupItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id,name,description,is_system,created_at
		FROM user_groups ORDER BY is_system DESC,name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UserGroupItem, 0)
	for rows.Next() {
		var item UserGroupItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.IsSystem, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repo) GetUsername(ctx context.Context, userID int64) (string, error) {
	var username string
	if err := r.pool.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, userID).Scan(&username); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	} else if err != nil {
		return "", err
	}
	return username, nil
}

func (r *Repo) CreateUserGroup(ctx context.Context, name, description string) (UserGroupItem, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	description = strings.TrimSpace(description)
	var item UserGroupItem
	err := r.pool.QueryRow(ctx, `
		INSERT INTO user_groups(name,description,is_system,quota_profile_id)
		VALUES($1,NULLIF($2,''),FALSE,(SELECT id FROM quota_profiles WHERE name='default_user'))
		RETURNING id,name,description,is_system,created_at`, name, description).Scan(
		&item.ID, &item.Name, &item.Description, &item.IsSystem, &item.CreatedAt,
	)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return UserGroupItem{}, ErrGroupNameExists
		case "23514", "22001":
			return UserGroupItem{}, ErrGroupInput
		}
	}
	return item, err
}

func (r *Repo) UpdateUserGroup(ctx context.Context, id int64, name, description string) (UserGroupItem, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	description = strings.TrimSpace(description)
	var item UserGroupItem
	err := r.pool.QueryRow(ctx, `
		UPDATE user_groups
		SET name=$2,description=NULLIF($3,'')
		WHERE id=$1 AND NOT is_system
		RETURNING id,name,description,is_system,created_at`, id, name, description).Scan(
		&item.ID, &item.Name, &item.Description, &item.IsSystem, &item.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var isSystem bool
		lookupErr := r.pool.QueryRow(ctx, `SELECT is_system FROM user_groups WHERE id=$1`, id).Scan(&isSystem)
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return UserGroupItem{}, ErrGroupNotFound
		}
		if lookupErr == nil && isSystem {
			return UserGroupItem{}, ErrGroupIsSystem
		}
		return UserGroupItem{}, lookupErr
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return UserGroupItem{}, ErrGroupNameExists
		case "23514", "22001":
			return UserGroupItem{}, ErrGroupInput
		}
	}
	return item, err
}

func (r *Repo) DeleteUserGroup(ctx context.Context, id int64) (string, error) {
	var name string
	var isSystem bool
	if err := r.pool.QueryRow(ctx, `SELECT name,is_system FROM user_groups WHERE id=$1`, id).Scan(&name, &isSystem); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrGroupNotFound
	} else if err != nil {
		return "", err
	}
	if isSystem {
		return "", ErrGroupIsSystem
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM user_groups WHERE id=$1`, id); err != nil {
		return "", err
	}
	return name, nil
}

func (r *Repo) SetUserGroupMembers(ctx context.Context, groupID int64, userIDs []int64, protectedUsername string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var groupName string
	if err := tx.QueryRow(ctx, `SELECT name FROM user_groups WHERE id=$1 FOR UPDATE`, groupID).Scan(&groupName); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrGroupNotFound
	} else if err != nil {
		return "", err
	}
	unique := make(map[int64]struct{}, len(userIDs))
	for _, userID := range userIDs {
		if userID < 1 {
			return "", ErrUserNotFound
		}
		unique[userID] = struct{}{}
	}
	ids := make([]int64, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE id=ANY($1) AND username<>$2`, ids, protectedUsername).Scan(&count); err != nil {
			return "", err
		}
		if count != len(ids) {
			return "", ErrUserNotFound
		}
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM user_group_memberships m
		USING users u
		WHERE m.user_id=u.id AND m.group_id=$1 AND u.username<>$2`, groupID, protectedUsername); err != nil {
		return "", err
	}
	for _, userID := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_group_memberships(user_id,group_id)
			VALUES($1,$2) ON CONFLICT DO NOTHING`, userID, groupID); err != nil {
			return "", err
		}
	}
	return groupName, tx.Commit(ctx)
}

func (r *Repo) SetUserGroups(ctx context.Context, userID int64, groupIDs []int64) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var username string
	if err := tx.QueryRow(ctx, `SELECT username FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&username); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	} else if err != nil {
		return "", err
	}
	unique := make(map[int64]struct{}, len(groupIDs))
	for _, groupID := range groupIDs {
		if groupID < 1 {
			return "", ErrGroupNotFound
		}
		unique[groupID] = struct{}{}
	}
	if len(unique) > 0 {
		ids := make([]int64, 0, len(unique))
		for id := range unique {
			ids = append(ids, id)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM user_groups WHERE id=ANY($1)`, ids).Scan(&count); err != nil {
			return "", err
		}
		if count != len(ids) {
			return "", ErrGroupNotFound
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_group_memberships WHERE user_id=$1`, userID); err != nil {
		return "", err
	}
	for groupID := range unique {
		if _, err := tx.Exec(ctx, `INSERT INTO user_group_memberships(user_id,group_id) VALUES($1,$2)`, userID, groupID); err != nil {
			return "", err
		}
	}
	return username, tx.Commit(ctx)
}

func (r *Repo) ResetUserPassword(ctx context.Context, userID int64, passwordHash string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var username string
	if err := tx.QueryRow(ctx, `UPDATE users SET password_hash=$2 WHERE id=$1 RETURNING username`, userID, passwordHash).Scan(&username); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	} else if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, userID); err != nil {
		return "", err
	}
	return username, tx.Commit(ctx)
}

func (r *Repo) SetUserDisabled(ctx context.Context, userID int64, disabled bool) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var username string
	if err := tx.QueryRow(ctx, `UPDATE users SET is_disabled=$2 WHERE id=$1 RETURNING username`, userID, disabled).Scan(&username); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	} else if err != nil {
		return "", err
	}
	if disabled {
		if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, userID); err != nil {
			return "", err
		}
	}
	return username, tx.Commit(ctx)
}

func (r *Repo) DeleteUser(ctx context.Context, userID int64) (string, []string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var username string
	if err := tx.QueryRow(ctx, `SELECT username FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&username); errors.Is(err, pgx.ErrNoRows) {
		return "", nil, ErrUserNotFound
	} else if err != nil {
		return "", nil, err
	}
	rows, err := tx.Query(ctx, `SELECT storage_key FROM resources WHERE owner_user_id=$1 AND storage_key IS NOT NULL`, userID)
	if err != nil {
		return "", nil, err
	}
	storageKeys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return "", nil, err
		}
		storageKeys = append(storageKeys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", nil, err
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}
	return username, storageKeys, nil
}

func (r *Repo) ListQuotaProfiles(ctx context.Context) ([]QuotaItem, error) {
	quotaRows, err := r.pool.Query(ctx, `
		SELECT id,name,description,storage_bytes_limit,single_file_bytes_limit,
		       daily_upload_bytes_limit,daily_upload_count_limit,active_share_count_limit,
		       active_direct_link_limit,is_system FROM quota_profiles ORDER BY is_system DESC,name`)
	if err != nil {
		return nil, err
	}
	defer quotaRows.Close()
	quotas := make([]QuotaItem, 0)
	for quotaRows.Next() {
		var item QuotaItem
		if err := quotaRows.Scan(&item.ID, &item.Name, &item.Description, &item.StorageBytesLimit,
			&item.SingleFileBytesLimit, &item.DailyUploadBytesLimit, &item.DailyUploadCountLimit,
			&item.ActiveShareCountLimit, &item.ActiveDirectLinkLimit, &item.IsSystem); err != nil {
			return nil, err
		}
		quotas = append(quotas, item)
	}
	return quotas, quotaRows.Err()
}

func (r *Repo) ListAccessGroups(ctx context.Context) ([]AccessGroupItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT g.id,g.name,g.description,g.is_system,g.quota_profile_id,g.priority,
		       COALESCE(array_agg(gp.permission ORDER BY gp.permission)
		         FILTER (WHERE gp.permission IS NOT NULL),'{}'::TEXT[])
		FROM user_groups g
		LEFT JOIN group_permissions gp ON gp.group_id=g.id
		GROUP BY g.id ORDER BY g.is_system DESC,g.priority DESC,g.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]AccessGroupItem, 0)
	for rows.Next() {
		var item AccessGroupItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &item.IsSystem,
			&item.QuotaProfileID, &item.Priority, &item.Permissions); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repo) SetGroupPermissions(ctx context.Context, groupID int64, codes []string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_groups WHERE id=$1)`, groupID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrGroupNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM group_permissions WHERE group_id=$1`, groupID); err != nil {
		return err
	}
	for _, code := range codes {
		if _, err := tx.Exec(ctx, `INSERT INTO group_permissions(group_id,permission) VALUES($1,$2)`, groupID, code); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repo) ListAudit(ctx context.Context, limit int) ([]AuditItem, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `SELECT id,actor_name,action,target_type,target_label,details,client_ip::TEXT,created_at FROM audit_logs ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditItem, 0)
	for rows.Next() {
		var item AuditItem
		if err := rows.Scan(&item.ID, &item.ActorName, &item.Action, &item.TargetType, &item.TargetLabel, &item.Details, &item.ClientIP, &item.CreatedAt); err != nil {
			return nil, err
		}
		if item.ClientIP != nil {
			normalized := normalizeAuditIP(*item.ClientIP)
			item.ClientIP = &normalized
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repo) WriteAudit(ctx context.Context, actorID int64, actorName, action, targetType, targetLabel string, details any, ip net.IP) error {
	body, err := json.Marshal(details)
	if err != nil {
		return err
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		ip = ipv4
	}
	_, err = r.pool.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,actor_name,action,target_type,target_label,details,client_ip) VALUES($1,$2,$3,$4,$5,$6,$7)`, actorID, actorName, action, targetType, targetLabel, body, ip)
	return err
}

func normalizeAuditIP(value string) string {
	host := strings.TrimSpace(value)
	if slash := strings.IndexByte(host, '/'); slash >= 0 {
		host = host[:slash]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return ipv4.String()
	}
	return ip.String()
}

func (r *Repo) GetReviewSettings(ctx context.Context) (ReviewSettings, error) {
	var out ReviewSettings
	err := r.pool.QueryRow(ctx, `SELECT upload_requires_review,custom_share_requires_review FROM system_settings WHERE id=1`).Scan(
		&out.UploadRequiresReview, &out.CustomShareRequiresReview,
	)
	return out, err
}

func (r *Repo) MarkFilePending(ctx context.Context, resourceID, uploadTaskID string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO file_moderations(resource_id,owner_user_id,file_name,size_bytes,mime_type,upload_task_id,status,reason,submitted_at,reviewed_at,reviewer_user_id)
		SELECT resource.id,resource.owner_user_id,resource.name,resource.size_bytes,resource.mime_type,
		       COALESCE(NULLIF($2,''),resource.id),'pending','',NOW(),NULL,NULL
		FROM resources resource
		WHERE resource.id=$1
		ON CONFLICT(resource_id) DO UPDATE SET
		owner_user_id=EXCLUDED.owner_user_id,file_name=EXCLUDED.file_name,size_bytes=EXCLUDED.size_bytes,
		mime_type=EXCLUDED.mime_type,upload_task_id=EXCLUDED.upload_task_id,status='pending',reason='',
		delete_file=FALSE,blocked=FALSE,submitted_at=NOW(),reviewed_at=NULL,reviewer_user_id=NULL`, resourceID, uploadTaskID)
	return err
}

func (r *Repo) MarkSharePending(ctx context.Context, shareID int64) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO share_moderations(share_id,status,reason,submitted_at,reviewed_at,reviewer_user_id)
		VALUES($1,'pending','',NOW(),NULL,NULL) ON CONFLICT(share_id) DO UPDATE SET
		status='pending',reason='',submitted_at=NOW(),reviewed_at=NULL,reviewer_user_id=NULL`, shareID)
	return err
}

func (r *Repo) ClearShareReview(ctx context.Context, shareID int64) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM share_moderations WHERE share_id=$1`, shareID)
	return err
}

func (r *Repo) IsFileApproved(ctx context.Context, resourceID string) (bool, error) {
	var approved bool
	err := r.pool.QueryRow(ctx, `SELECT COALESCE((SELECT status='approved' FROM file_moderations WHERE resource_id=$1),TRUE)`, resourceID).Scan(&approved)
	return approved, err
}

func (r *Repo) IsShareApproved(ctx context.Context, shareID int64) (bool, error) {
	var approved bool
	err := r.pool.QueryRow(ctx, `SELECT COALESCE((SELECT status='approved' FROM share_moderations WHERE share_id=$1),TRUE)`, shareID).Scan(&approved)
	return approved, err
}

func (r *Repo) ListFileReviews(ctx context.Context) ([]FileReviewItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT resource.id,
		       COALESCE(NULLIF(session.batch_id,''),session.id,resource.id),
		       resource.name,resource.name,resource.size_bytes,resource.mime_type,
		       resource.owner_user_id,owner.username,
		       COALESCE(moderation.status,'approved'),COALESCE(moderation.reason,''),
		       COALESCE(moderation.delete_file,FALSE),COALESCE(moderation.blocked,FALSE),TRUE,
		       resource.trashed_at,COALESCE(session.created_at,resource.created_at),
		       moderation.reviewed_at,reviewer.username
		FROM resources resource
		JOIN users owner ON owner.id=resource.owner_user_id
		LEFT JOIN upload_sessions session ON session.resource_id=resource.id
		LEFT JOIN file_moderations moderation ON moderation.resource_id=resource.id
		LEFT JOIN users reviewer ON reviewer.id=moderation.reviewer_user_id
		WHERE resource.kind='file'
		ORDER BY COALESCE(session.created_at,resource.created_at) DESC,resource.id`)
	if err != nil {
		return nil, err
	}
	items := make([]FileReviewItem, 0)
	for rows.Next() {
		var item FileReviewItem
		if err := rows.Scan(&item.ResourceID, &item.TaskID, &item.Name, &item.RelativePath, &item.SizeBytes, &item.MimeType,
			&item.OwnerUserID, &item.OwnerName, &item.Status, &item.Reason, &item.DeleteFile, &item.Blocked,
			&item.Exists, &item.TrashedAt, &item.SubmittedAt, &item.ReviewedAt, &item.Reviewer); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	deletedRows, err := r.pool.Query(ctx, `
		SELECT moderation.resource_id,COALESCE(NULLIF(moderation.upload_task_id,''),moderation.resource_id),
		       moderation.file_name,moderation.file_name,moderation.size_bytes,moderation.mime_type,
		       COALESCE(moderation.owner_user_id,0),COALESCE(owner.username,'已删除用户'),
		       moderation.status,moderation.reason,moderation.delete_file,moderation.blocked,FALSE,
		       NULL::TIMESTAMPTZ,moderation.submitted_at,moderation.reviewed_at,reviewer.username
		FROM file_moderations moderation
		LEFT JOIN resources resource ON resource.id=moderation.resource_id
		LEFT JOIN users owner ON owner.id=moderation.owner_user_id
		LEFT JOIN users reviewer ON reviewer.id=moderation.reviewer_user_id
		WHERE resource.id IS NULL
		ORDER BY moderation.submitted_at DESC,moderation.resource_id`)
	if err != nil {
		return nil, err
	}
	defer deletedRows.Close()
	for deletedRows.Next() {
		var item FileReviewItem
		if err := deletedRows.Scan(&item.ResourceID, &item.TaskID, &item.Name, &item.RelativePath, &item.SizeBytes, &item.MimeType,
			&item.OwnerUserID, &item.OwnerName, &item.Status, &item.Reason, &item.DeleteFile, &item.Blocked,
			&item.Exists, &item.TrashedAt, &item.SubmittedAt, &item.ReviewedAt, &item.Reviewer); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, deletedRows.Err()
}

func (r *Repo) ListShareReviews(ctx context.Context) ([]ShareReviewItem, error) {
	rows, err := r.pool.Query(ctx, `SELECT s.id,s.token_value,u.username,s.owner_user_id,
		COALESCE(MIN(res.name),'多项文件'),s.description,s.description_format,
		COALESCE(sr.status,'approved'),COALESCE(sr.reason,''),COALESCE(sr.delete_link,FALSE),
		COALESCE(sr.blocked,FALSE),s.deleted_at,s.created_at,sr.reviewed_at,reviewer.username
		FROM shares s JOIN users u ON u.id=s.owner_user_id
		LEFT JOIN share_moderations sr ON sr.share_id=s.id
		LEFT JOIN share_resources link ON link.share_id=s.id LEFT JOIN resources res ON res.id=link.resource_id
		LEFT JOIN users reviewer ON reviewer.id=sr.reviewer_user_id
		GROUP BY s.id,u.username,u.id,sr.status,sr.reason,sr.delete_link,sr.blocked,sr.reviewed_at,reviewer.username
		ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShareReviewItem, 0)
	for rows.Next() {
		var item ShareReviewItem
		if err := rows.Scan(&item.ShareID, &item.Token, &item.OwnerName, &item.OwnerUserID, &item.ResourceName,
			&item.Description, &item.DescriptionFormat, &item.Status, &item.Reason, &item.DeleteLink,
			&item.Blocked, &item.DeletedAt, &item.SubmittedAt, &item.ReviewedAt, &item.Reviewer); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repo) GetFileReviewItem(ctx context.Context, resourceID string) (FileReviewItem, error) {
	items, err := r.ListFileReviews(ctx)
	if err != nil {
		return FileReviewItem{}, err
	}
	for _, item := range items {
		if item.ResourceID == resourceID {
			return item, nil
		}
	}
	return FileReviewItem{}, ErrUserNotFound
}

func (r *Repo) GetShareReviewItem(ctx context.Context, shareID int64) (ShareReviewItem, error) {
	items, err := r.ListShareReviews(ctx)
	if err != nil {
		return ShareReviewItem{}, err
	}
	for _, item := range items {
		if item.ShareID == shareID {
			return item, nil
		}
	}
	return ShareReviewItem{}, ErrUserNotFound
}

func (r *Repo) SetShareDisposition(ctx context.Context, shareID int64, deleteLink, blocked bool) error {
	tag, err := r.pool.Exec(ctx, `UPDATE shares SET
		deleted_at=CASE WHEN $2 THEN COALESCE(deleted_at,NOW()) ELSE NULL END,
		admin_blocked=$3
		WHERE id=$1`, shareID, deleteLink, blocked)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return err
}

func (r *Repo) ReviewFile(ctx context.Context, item FileReviewItem, status, reason string, deleteFile, blocked bool, reviewerID int64) error {
	if status != "approved" && status != "rejected" {
		return errors.New("审核状态无效")
	}
	_, err := r.pool.Exec(ctx, `INSERT INTO file_moderations(
		resource_id,owner_user_id,file_name,size_bytes,mime_type,upload_task_id,status,reason,
		delete_file,blocked,submitted_at,reviewed_at,reviewer_user_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NOW(),$12)
		ON CONFLICT(resource_id) DO UPDATE SET status=EXCLUDED.status,reason=EXCLUDED.reason,
		delete_file=EXCLUDED.delete_file,blocked=EXCLUDED.blocked,reviewed_at=NOW(),reviewer_user_id=EXCLUDED.reviewer_user_id,
		owner_user_id=COALESCE(file_moderations.owner_user_id,EXCLUDED.owner_user_id),
		file_name=CASE WHEN file_moderations.file_name='' THEN EXCLUDED.file_name ELSE file_moderations.file_name END,
		size_bytes=CASE WHEN file_moderations.size_bytes=0 THEN EXCLUDED.size_bytes ELSE file_moderations.size_bytes END,
		mime_type=COALESCE(file_moderations.mime_type,EXCLUDED.mime_type),
		upload_task_id=CASE WHEN file_moderations.upload_task_id='' THEN EXCLUDED.upload_task_id ELSE file_moderations.upload_task_id END`,
		item.ResourceID, item.OwnerUserID, item.Name, item.SizeBytes, item.MimeType, item.TaskID,
		status, strings.TrimSpace(reason), deleteFile, blocked, item.SubmittedAt, reviewerID)
	return err
}

func (r *Repo) ReviewShare(ctx context.Context, shareID int64, status, reason string, deleteLink, blocked bool, reviewerID int64) error {
	if status != "approved" && status != "rejected" {
		return errors.New("审核状态无效")
	}
	tag, err := r.pool.Exec(ctx, `INSERT INTO share_moderations(share_id,status,reason,delete_link,blocked,submitted_at,reviewed_at,reviewer_user_id)
		SELECT id,$2,$3,$4,$5,created_at,NOW(),$6 FROM shares WHERE id=$1
		ON CONFLICT(share_id) DO UPDATE SET status=EXCLUDED.status,reason=EXCLUDED.reason,
		delete_link=EXCLUDED.delete_link,blocked=EXCLUDED.blocked,reviewed_at=NOW(),reviewer_user_id=EXCLUDED.reviewer_user_id`,
		shareID, status, strings.TrimSpace(reason), deleteLink, blocked, reviewerID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return err
}
