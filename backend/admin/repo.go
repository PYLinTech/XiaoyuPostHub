// Package admin 提供管理面板只读统计与审计持久化。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/permission"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
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
	// ErrUserWithoutGroup 表示操作会让某个用户失去全部用户组：其账号将失去
	// 登录权限（无 login），上传/分享也会因配额解析不到而失败。
	ErrUserWithoutGroup = errors.New("每位用户至少需要归属一个用户组")
	// ErrGroupSoleMembers 表示待删除用户组中存在"只属于该组"的成员。
	ErrGroupSoleMembers = errors.New("删除后部分成员将失去全部用户组，请先为他们分配其它用户组")
)

type Overview struct {
	UserCount             int64 `json:"userCount"`
	FileCount             int64 `json:"fileCount"`
	FolderCount           int64 `json:"folderCount"`
	StorageUsedBytes      int64 `json:"storageUsedBytes"`
	StorageAvailableBytes int64 `json:"storageAvailableBytes"`
	StorageTotalBytes     int64 `json:"storageTotalBytes"`
	// StorageDiskAvailable 表示宿主磁盘信息是否读取成功；false 时上面三个字段无意义。
	StorageDiskAvailable bool  `json:"storageDiskAvailable"`
	ActiveShareCount     int64 `json:"activeShareCount"`
	ActiveDirectCount    int64 `json:"activeDirectCount"`
	ShareDownloadCount   int64 `json:"shareDownloadCount"`
	ShareTrafficBytes    int64 `json:"shareTrafficBytes"`
}

type UserItem struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	GroupIDs  []int64   `json:"groupIds"`
	Groups    []string  `json:"groups"`
	Disabled  bool      `json:"disabled"`
	CreatedAt time.Time `json:"createdAt"`
	// UsedBytes 逻辑存储用量（未彻底删除的文件字节数，与用户配额同口径）。
	UsedBytes int64 `json:"usedBytes"`
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
	// StorageBackendID 组内用户新上传的目标后端；nil 表示使用全局默认。
	StorageBackendID *int64 `json:"storageBackendId,omitempty"`
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
	PurgedAt     *time.Time `json:"purgedAt,omitempty"`
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

func (r *Repo) GetOverview(ctx context.Context, diskPath string) (Overview, error) {
	var out Overview
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM users),
			(SELECT COUNT(*) FROM resources WHERE kind='file' AND trashed_at IS NULL),
			(SELECT COUNT(*) FROM resources WHERE kind='folder' AND trashed_at IS NULL),
			(SELECT COUNT(*) FROM shares s WHERE s.is_active AND NOT s.admin_blocked AND s.deleted_at IS NULL
			   AND (s.expires_at IS NULL OR s.expires_at>NOW())
			   AND EXISTS (SELECT 1 FROM share_resources sr JOIN resources r ON r.id=sr.resource_id
			               WHERE sr.share_id=s.id AND sr.display_order=0 AND r.trashed_at IS NULL AND r.purged_at IS NULL)),
			(SELECT COUNT(*) FROM direct_links d WHERE d.is_active AND (d.expires_at IS NULL OR d.expires_at>NOW())
			   AND EXISTS (SELECT 1 FROM resources r WHERE r.id=d.resource_id AND r.kind='file'
			               AND r.trashed_at IS NULL AND r.purged_at IS NULL)),
			(SELECT COALESCE(SUM(download_count),0)::BIGINT FROM shares WHERE NOT admin_blocked AND deleted_at IS NULL),
			(SELECT COALESCE(SUM(traffic_used_bytes),0)::BIGINT FROM shares WHERE NOT admin_blocked AND deleted_at IS NULL)`).Scan(
		&out.UserCount, &out.FileCount, &out.FolderCount,
		&out.ActiveShareCount, &out.ActiveDirectCount, &out.ShareDownloadCount, &out.ShareTrafficBytes,
	)
	if err != nil {
		return Overview{}, err
	}
	// 宿主磁盘信息属于附加信息：读取失败（路径不存在、无权限、非常规文件系统）时
	// 只标记不可用，不能让整个概览接口失败——否则磁盘一异常，总览页所有数据都看不了。
	// diskPath 来自部署级配置，不跟随可改的存储路径，避免统计口径被改坏。
	var fs syscall.Statfs_t
	if err := syscall.Statfs(diskPath, &fs); err != nil {
		out.StorageDiskAvailable = false
		return out, nil
	}
	blockSize := uint64(fs.Bsize)
	total := uint64(fs.Blocks) * blockSize
	free := uint64(fs.Bfree) * blockSize
	available := uint64(fs.Bavail) * blockSize
	out.StorageTotalBytes = int64(total)
	out.StorageUsedBytes = int64(total - free)
	out.StorageAvailableBytes = int64(available)
	out.StorageDiskAvailable = true
	return out, nil
}

func (r *Repo) ListUsers(ctx context.Context) ([]UserItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT u.id,u.username,u.created_at,u.is_disabled,
		       COALESCE(array_agg(g.id ORDER BY g.name) FILTER (WHERE g.id IS NOT NULL),'{}'::BIGINT[]),
		       COALESCE(array_agg(g.name ORDER BY g.name) FILTER (WHERE g.name IS NOT NULL),'{}'::TEXT[]),
		       COALESCE(usage.used_bytes,0)
		FROM users u
		LEFT JOIN user_group_memberships m ON m.user_id=u.id
		LEFT JOIN user_groups g ON g.id=m.group_id
		LEFT JOIN LATERAL (
		    SELECT SUM(r.size_bytes) AS used_bytes
		    FROM resources r
		    WHERE r.owner_user_id=u.id AND r.kind='file' AND r.purged_at IS NULL
		) usage ON TRUE
		GROUP BY u.id, usage.used_bytes ORDER BY u.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]UserItem, 0)
	for rows.Next() {
		var item UserItem
		if err := rows.Scan(&item.ID, &item.Username, &item.CreatedAt, &item.Disabled, &item.GroupIDs, &item.Groups, &item.UsedBytes); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetUserGroupIDs 返回用户的用户组编号（用于"操作者权限是否覆盖目标账户"的判定）。
func (r *Repo) GetUserGroupIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := r.pool.Query(ctx, `SELECT group_id FROM user_group_memberships WHERE user_id=$1 ORDER BY group_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0, 2)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
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
	// 与成员/归属接口同一不变量：删除组不能让任何成员失去全部归属，否则其
	// 账号将无法登录（无 login 权限），配额解析也会失败。
	var soleMembers int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM user_group_memberships m
		WHERE m.group_id=$1
		  AND NOT EXISTS (
		      SELECT 1 FROM user_group_memberships other
		      WHERE other.user_id=m.user_id AND other.group_id<>$1
		  )`, id).Scan(&soleMembers); err != nil {
		return "", err
	}
	if soleMembers > 0 {
		return "", ErrGroupSoleMembers
	}
	// 发给该组的消息（receiver_id 无外键）随组删除清理，避免留下不可达行。
	if _, err := r.pool.Exec(ctx, `DELETE FROM messages WHERE receiver_type='group' AND receiver_id=$1`, id); err != nil {
		return "", err
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
	// 记录变更前成员：用于校验"任何用户都不会失去全部用户组"。
	previous, err := tx.Query(ctx, `SELECT user_id FROM user_group_memberships WHERE group_id=$1`, groupID)
	if err != nil {
		return "", err
	}
	previousIDs := make([]int64, 0, 16)
	for previous.Next() {
		var memberID int64
		if err := previous.Scan(&memberID); err != nil {
			previous.Close()
			return "", err
		}
		previousIDs = append(previousIDs, memberID)
	}
	if err := previous.Err(); err != nil {
		previous.Close()
		return "", err
	}
	previous.Close()
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
	// 受影响用户（原成员 ∪ 新成员）不得失去全部归属：否则账号无法登录，
	// 配额解析也会失败。整笔事务回滚。
	affected := append(previousIDs, ids...)
	var orphan int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM unnest($1::bigint[]) AS t(user_id)
		WHERE NOT EXISTS (SELECT 1 FROM user_group_memberships m WHERE m.user_id = t.user_id)`,
		affected).Scan(&orphan); err != nil {
		return "", err
	}
	if orphan > 0 {
		return "", ErrUserWithoutGroup
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
	if len(unique) == 0 {
		// 清空全部归属会让账号失去登录权限（无 login），配额解析也会失败
		// （上传/分享报错），因此要求至少保留一个用户组。
		return "", ErrUserWithoutGroup
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
	if err := tx.QueryRow(ctx, `
		UPDATE users
		SET password_hash=$2,totp_secret=NULL,totp_grace_used=FALSE
		WHERE id=$1 RETURNING username`, userID, passwordHash).Scan(&username); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	} else if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM login_totp_challenges WHERE user_id=$1`, userID); err != nil {
		return "", err
	}
	return username, tx.Commit(ctx)
}

func (r *Repo) ListTOTPPolicyGroups(ctx context.Context) (allowed, required []string, err error) {
	rows, err := r.pool.Query(ctx, `
		SELECT g.name,
		       BOOL_OR(gp.permission=$1) AS allowed,
		       BOOL_OR(gp.permission=$2) AS required
		FROM user_groups g
		JOIN group_permissions gp ON gp.group_id=g.id
		WHERE gp.permission IN ($1,$2)
		GROUP BY g.id,g.name
		ORDER BY g.name`, permission.UseLoginTOTP, permission.RequireLoginTOTP)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var hasAllowed, hasRequired bool
		if err := rows.Scan(&name, &hasAllowed, &hasRequired); err != nil {
			return nil, nil, err
		}
		if hasAllowed || hasRequired {
			allowed = append(allowed, name)
		}
		if hasRequired {
			required = append(required, name)
		}
	}
	return allowed, required, rows.Err()
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
		// 禁用同时作废发给该用户的未使用邀请码：否则其名下邀请码仍可继续用于
		// 注册与加组（"禁用"对外部凭证也应生效）。
		if _, err := tx.Exec(ctx, `UPDATE invitation_codes SET revoked_at=NOW() WHERE issued_to_user_id=$1 AND used_at IS NULL AND revoked_at IS NULL`, userID); err != nil {
			return "", err
		}
	}
	return username, tx.Commit(ctx)
}

// DeleteUser 删除用户并返回其全部物理对象 ID（由调用方降引用计数）。
// 删除用户会随外键级联删除其资源：删除前把全部文件的当前状态归档到审核历史，
// 保证管理员事后仍可追溯；物理对象只减少引用，不直接删除文件。
func (r *Repo) DeleteUser(ctx context.Context, userID int64, actorName string) (string, []string, error) {
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
	// 只收集仍持有引用的行：已彻底删除（purged_at 非空）的行在 purge 时已释放
	// 引用，再次释放会把同一对象多扣一次，导致其它引用者的文件被误判为孤儿。
	rows, err := tx.Query(ctx, `SELECT blob_id FROM resources WHERE owner_user_id=$1 AND blob_id IS NOT NULL AND purged_at IS NULL`, userID)
	if err != nil {
		return "", nil, err
	}
	blobIDs := make([]string, 0)
	for rows.Next() {
		var blobID string
		if err := rows.Scan(&blobID); err != nil {
			rows.Close()
			return "", nil, err
		}
		blobIDs = append(blobIDs, blobID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", nil, err
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `
		INSERT INTO file_moderation_history (
			resource_id, owner_user_id, file_name, size_bytes, mime_type, upload_task_id,
			event, status, reason, delete_file, blocked, actor_type, actor_name, details
		)
		SELECT rr.id, rr.owner_user_id, rr.name, rr.size_bytes, rr.mime_type,
		       COALESCE(NULLIF(s.batch_id,''), s.id, rr.id),
		       'purged', COALESCE(m.status,''), COALESCE(m.reason,''),
		       COALESCE(m.delete_file,FALSE), COALESCE(m.blocked,FALSE),
		       'admin', $2, jsonb_build_object('source', 'user_deleted')
		FROM resources rr
		LEFT JOIN LATERAL (
			SELECT id, batch_id FROM upload_sessions
			WHERE resource_id = rr.id ORDER BY created_at DESC LIMIT 1
		) s ON TRUE
		LEFT JOIN file_moderations m ON m.resource_id = rr.id
		WHERE rr.kind='file' AND rr.owner_user_id=$1`, userID, strings.TrimSpace(actorName)); err != nil {
		return "", nil, err
	}
	// 消息（receiver_id 无外键）与登录失败记录（按用户名键控）随用户清理：
	// 前者会留下永远不可达的行（含邀请码明文），后者会让重建的同名账号继承限流。
	if _, err := tx.Exec(ctx, `DELETE FROM messages WHERE receiver_type='user' AND receiver_id=$1`, userID); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM login_failure_events WHERE account_key = LOWER($1)`, username); err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID); err != nil {
		return "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", nil, err
	}
	return username, blobIDs, nil
}

// CollectUserArtifactPaths 收集用户名下分享的临时 ZIP 制品路径。必须在外键级联
// 删除（DeleteUser）之前调用：任务行删除后制品文件将永久失联（磁盘泄漏）。
func (r *Repo) CollectUserArtifactPaths(ctx context.Context, userID int64) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT j.artifact_path FROM share_download_jobs j
		JOIN shares s ON s.id = j.share_id
		WHERE s.owner_user_id = $1 AND j.artifact_path IS NOT NULL`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make([]string, 0, 4)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
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
		SELECT g.id,g.name,g.description,g.is_system,g.quota_profile_id,g.priority,g.storage_backend_id,
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
			&item.QuotaProfileID, &item.Priority, &item.StorageBackendID, &item.Permissions); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repo) SetGroupPermissions(ctx context.Context, groupID int64, codes []string) error {
	// “强制使用”必然包含“允许使用”，服务端兜底修正异常客户端请求。
	hasRequired, hasAllowed := false, false
	for _, code := range codes {
		hasRequired = hasRequired || code == permission.RequireLoginTOTP
		hasAllowed = hasAllowed || code == permission.UseLoginTOTP
	}
	if hasRequired && !hasAllowed {
		codes = append(codes, permission.UseLoginTOTP)
	}
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

func (r *Repo) MarkSharePending(ctx context.Context, shareID int64) error {
	_, err := r.pool.Exec(ctx, `INSERT INTO share_moderations(share_id,status,reason,submitted_at,reviewed_at,reviewer_user_id)
		VALUES($1,'pending','',NOW(),NULL,NULL) ON CONFLICT(share_id) DO UPDATE SET
		status='pending',reason='',submitted_at=NOW(),reviewed_at=NULL,reviewer_user_id=NULL`, shareID)
	return err
}

// ClearShareReview 清除"待审"记录（审核策略关闭时用户编辑分享后恢复正常）。
// 只删除 pending：被驳回/已通过的处置结论不能被所有者的一次编辑清除——否则
// 等于自助撤销审核结论，且审核证据（reason/delete_link/blocked）被销毁。
func (r *Repo) ClearShareReview(ctx context.Context, shareID int64) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM share_moderations WHERE share_id=$1 AND status='pending'`, shareID)
	return err
}

func (r *Repo) IsFileApproved(ctx context.Context, resourceID string) (bool, error) {
	var approved bool
	err := r.pool.QueryRow(ctx, `SELECT COALESCE((SELECT status='approved' FROM file_moderations WHERE resource_id=$1),TRUE)`, resourceID).Scan(&approved)
	return approved, err
}

// DeliverableFileIDs 返回给定资源中"可对外交付"的文件 id 集合。
// 交付可用性 = 未入回收站/未彻底删除 + 未被管理员拉黑 + 审核不处于待审/驳回
// （无审核记录视为通过）。分享清单、文件夹 ZIP 打包、前端打包清单、单文件
// 交付与直链交付共用该口径：处置或内容替换后立即停止对外交付。
func (r *Repo) DeliverableFileIDs(ctx context.Context, resourceIDs []string) (map[string]struct{}, error) {
	deliverable := make(map[string]struct{}, len(resourceIDs))
	if len(resourceIDs) == 0 {
		return deliverable, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT r.id FROM resources r
		LEFT JOIN file_moderations m ON m.resource_id = r.id
		WHERE r.id = ANY($1) AND r.kind='file'
		  AND r.trashed_at IS NULL AND r.purged_at IS NULL
		  AND NOT r.admin_blocked
		  AND COALESCE(m.status, 'approved') = 'approved'`, resourceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		deliverable[id] = struct{}{}
	}
	return deliverable, rows.Err()
}

func (r *Repo) IsShareApproved(ctx context.Context, shareID int64) (bool, error) {
	var approved bool
	err := r.pool.QueryRow(ctx, `SELECT COALESCE((SELECT status='approved' FROM share_moderations WHERE share_id=$1),TRUE)`, shareID).Scan(&approved)
	return approved, err
}

// fileReviewSelect 是「文件审核列表」的共享投影：列表与单条查询复用同一份字段
// 与连接，避免两侧漂移。调用方在其后追加 WHERE/ORDER BY 子句。
//
// 上传会话用 LATERAL 取最新一条：一个资源可能存在多个历史上传会话（覆盖上传），
// 普通 JOIN 会放大行数、让同一资源在列表中重复出现。
const fileReviewSelect = `
	SELECT resource.id,
	       COALESCE(NULLIF(session.batch_id,''),session.id,resource.id),
	       resource.name,resource.name,resource.size_bytes,resource.mime_type,
	       resource.owner_user_id,owner.username,
	       COALESCE(moderation.status,'approved'),COALESCE(moderation.reason,''),
	       COALESCE(moderation.delete_file,FALSE),COALESCE(moderation.blocked,FALSE),TRUE,
	       resource.trashed_at,resource.purged_at,COALESCE(session.created_at,resource.created_at),
	       moderation.reviewed_at,reviewer.username
	FROM resources resource
	JOIN users owner ON owner.id=resource.owner_user_id
	LEFT JOIN LATERAL (
		SELECT id, batch_id, created_at FROM upload_sessions
		WHERE resource_id = resource.id ORDER BY created_at DESC LIMIT 1
	) session ON TRUE
	LEFT JOIN file_moderations moderation ON moderation.resource_id=resource.id
	LEFT JOIN users reviewer ON reviewer.id=moderation.reviewer_user_id`

// scanFileReviewItem 扫描一行文件审核记录（列表两段与单条查询共用同一列序）。
func scanFileReviewItem(row interface{ Scan(dest ...any) error }) (FileReviewItem, error) {
	var item FileReviewItem
	err := row.Scan(&item.ResourceID, &item.TaskID, &item.Name, &item.RelativePath, &item.SizeBytes, &item.MimeType,
		&item.OwnerUserID, &item.OwnerName, &item.Status, &item.Reason, &item.DeleteFile, &item.Blocked,
		&item.Exists, &item.TrashedAt, &item.PurgedAt, &item.SubmittedAt, &item.ReviewedAt, &item.Reviewer)
	return item, err
}

func (r *Repo) ListFileReviews(ctx context.Context) ([]FileReviewItem, error) {
	rows, err := r.pool.Query(ctx, fileReviewSelect+`
		WHERE resource.kind='file'
		ORDER BY COALESCE(session.created_at,resource.created_at) DESC,resource.id`)
	if err != nil {
		return nil, err
	}
	items := make([]FileReviewItem, 0)
	for rows.Next() {
		item, scanErr := scanFileReviewItem(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
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
		       NULL::TIMESTAMPTZ,NULL::TIMESTAMPTZ,moderation.submitted_at,moderation.reviewed_at,reviewer.username
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
		item, scanErr := scanFileReviewItem(deletedRows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, deletedRows.Err()
}

// shareReviewSelect / shareReviewGroupBy 是分享审核查询的共享主体：列表与单条
// 查询复用，避免字段漂移。
const shareReviewSelect = `
	SELECT s.id,s.token_value,u.username,s.owner_user_id,
		COALESCE(MIN(res.name),'多项文件'),s.description,s.description_format,
		COALESCE(sr.status,'approved'),COALESCE(sr.reason,''),COALESCE(sr.delete_link,FALSE),
		COALESCE(sr.blocked,FALSE),s.deleted_at,s.created_at,sr.reviewed_at,reviewer.username
	FROM shares s JOIN users u ON u.id=s.owner_user_id
	LEFT JOIN share_moderations sr ON sr.share_id=s.id
	LEFT JOIN share_resources link ON link.share_id=s.id LEFT JOIN resources res ON res.id=link.resource_id
	LEFT JOIN users reviewer ON reviewer.id=sr.reviewer_user_id`

const shareReviewGroupBy = `
	GROUP BY s.id,u.username,u.id,sr.status,sr.reason,sr.delete_link,sr.blocked,sr.reviewed_at,reviewer.username`

// scanShareReviewItem 扫描一行分享审核记录。
func scanShareReviewItem(row interface{ Scan(dest ...any) error }) (ShareReviewItem, error) {
	var item ShareReviewItem
	err := row.Scan(&item.ShareID, &item.Token, &item.OwnerName, &item.OwnerUserID, &item.ResourceName,
		&item.Description, &item.DescriptionFormat, &item.Status, &item.Reason, &item.DeleteLink,
		&item.Blocked, &item.DeletedAt, &item.SubmittedAt, &item.ReviewedAt, &item.Reviewer)
	return item, err
}

func (r *Repo) ListShareReviews(ctx context.Context) ([]ShareReviewItem, error) {
	rows, err := r.pool.Query(ctx, shareReviewSelect+shareReviewGroupBy+`
	ORDER BY s.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ShareReviewItem, 0)
	for rows.Next() {
		item, scanErr := scanShareReviewItem(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetFileReviewItem 读取单个资源的审核记录：专用查询按主键收窄，不再加载全表。
func (r *Repo) GetFileReviewItem(ctx context.Context, resourceID string) (FileReviewItem, error) {
	item, err := scanFileReviewItem(r.pool.QueryRow(ctx, fileReviewSelect+`
		WHERE resource.kind='file' AND resource.id=$1`, resourceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return FileReviewItem{}, ErrUserNotFound
	}
	if err != nil {
		return FileReviewItem{}, err
	}
	return item, nil
}

// GetShareReviewItem 读取单条分享的审核记录：专用查询按主键收窄，不再加载全表。
func (r *Repo) GetShareReviewItem(ctx context.Context, shareID int64) (ShareReviewItem, error) {
	item, err := scanShareReviewItem(r.pool.QueryRow(ctx,
		shareReviewSelect+`
		WHERE s.id=$1`+shareReviewGroupBy, shareID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ShareReviewItem{}, ErrUserNotFound
	}
	if err != nil {
		return ShareReviewItem{}, err
	}
	return item, nil
}

// SetShareDisposition 写入分享处置（删除链接 / 封禁）。
//
// 注意：$2=false 时**不清空** deleted_at。审核"通过"会带 delete=false 过来，若这里
// 把 deleted_at 置回 NULL，管理员先前用「删除」软删的分享会被审核动作"复活"，构成
// 对删除动作的反向覆盖。需要显式撤销删除时走 AdminUpdateShare 的 unblock（明确语义）。
func (r *Repo) SetShareDisposition(ctx context.Context, shareID int64, deleteLink, blocked bool) error {
	tag, err := r.pool.Exec(ctx, `UPDATE shares SET
		deleted_at=CASE WHEN $2 THEN COALESCE(deleted_at,NOW()) ELSE deleted_at END,
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
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `INSERT INTO file_moderations(
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
		status, strings.TrimSpace(reason), deleteFile, blocked, item.SubmittedAt, reviewerID); err != nil {
		return err
	}
	// 同事务追加审核历史：审核结论与历史必须同时可见。
	var reviewerName string
	_ = tx.QueryRow(ctx, `SELECT username FROM users WHERE id=$1`, reviewerID).Scan(&reviewerName)
	if err := resource.RecordModerationHistory(ctx, tx, resource.HistoryEntry{
		ResourceID: item.ResourceID, OwnerUserID: item.OwnerUserID, FileName: item.Name,
		SizeBytes: item.SizeBytes, MimeType: item.MimeType, UploadTaskID: item.TaskID,
		Event: status, Status: status, Reason: strings.TrimSpace(reason),
		DeleteFile: deleteFile, Blocked: blocked,
		ActorType: "admin", ActorName: reviewerName,
		Details: map[string]any{"exists": item.Exists, "trashed": item.TrashedAt != nil, "purged": item.PurgedAt != nil},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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

// StorageBackend 是存储后端的可管理视图。敏感凭据（123 云盘 client
// secret、对象存储 AK/SK）只存在于 .env，因此此结构不包含任何凭据字段。
type StorageBackend struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Kind      string         `json:"kind"`
	Settings  map[string]any `json:"settings"`
	IsEnabled bool           `json:"isEnabled"`
	IsDefault bool           `json:"isDefault"`
}

// ListStorageBackends 返回全部存储后端（含停用的，供管理界面展示）。
func (r *Repo) ListStorageBackends(ctx context.Context) ([]StorageBackend, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, kind, settings, is_enabled, is_default
		FROM storage_backends ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StorageBackend, 0)
	for rows.Next() {
		var item StorageBackend
		var raw []byte
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &raw, &item.IsEnabled, &item.IsDefault); err != nil {
			return nil, err
		}
		item.Settings = map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &item.Settings)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// SaveStorageBackend 新增（ID=0）或更新存储后端；设置默认后端时保证唯一。
func (r *Repo) SaveStorageBackend(ctx context.Context, item StorageBackend) (StorageBackend, error) {
	settings, err := json.Marshal(item.Settings)
	if err != nil {
		return StorageBackend{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return StorageBackend{}, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	// 默认后端全局唯一（部分唯一索引 storage_backends_single_default_idx）：
	// 必须先清掉其它行的默认标记，再写入本行，否则会触发唯一冲突。
	if item.IsDefault {
		if _, err := tx.Exec(ctx, `
			UPDATE storage_backends SET is_default=FALSE, updated_at=NOW()
			WHERE is_default AND ($1 = 0 OR id <> $1)`, item.ID); err != nil {
			return StorageBackend{}, err
		}
	}
	var saved StorageBackend
	var raw []byte
	if item.ID == 0 {
		err = tx.QueryRow(ctx, `
			INSERT INTO storage_backends (name, kind, settings, is_enabled, is_default)
			VALUES ($1,$2,$3,$4,$5)
			RETURNING id, name, kind, settings, is_enabled, is_default`,
			item.Name, item.Kind, settings, item.IsEnabled, item.IsDefault).
			Scan(&saved.ID, &saved.Name, &saved.Kind, &raw, &saved.IsEnabled, &saved.IsDefault)
	} else {
		err = tx.QueryRow(ctx, `
			UPDATE storage_backends SET name=$2, kind=$3, settings=$4,
				is_enabled=$5, is_default=$6, updated_at=NOW()
			WHERE id=$1
			RETURNING id, name, kind, settings, is_enabled, is_default`,
			item.ID, item.Name, item.Kind, settings, item.IsEnabled, item.IsDefault).
			Scan(&saved.ID, &saved.Name, &saved.Kind, &raw, &saved.IsEnabled, &saved.IsDefault)
	}
	if err != nil {
		return StorageBackend{}, err
	}
	saved.Settings = map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &saved.Settings)
	}
	if err := tx.Commit(ctx); err != nil {
		return StorageBackend{}, err
	}
	return saved, nil
}

// StorageUsageByBackend 返回各后端已登记的对象明文字节总量（含待清理对象）。
// StorageUsageByBackend 汇总各后端的对象总量（含孤儿对象——它们仍占用物理空间，
// 管理界面展示"已用"应包含）。当前为全表聚合；对象规模很大时可改为定期统计。
func (r *Repo) StorageUsageByBackend(ctx context.Context) (map[int64]int64, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT backend_id, COALESCE(SUM(size_bytes),0)::BIGINT
		FROM storage_blobs GROUP BY backend_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var backendID, total int64
		if err := rows.Scan(&backendID, &total); err != nil {
			return nil, err
		}
		out[backendID] = total
	}
	return out, rows.Err()
}

// StorageTask 描述一条存储维护任务及其进度。
type StorageTask struct {
	ID     int64          `json:"id"`
	Type   string         `json:"type"`
	Phase  string         `json:"phase"`
	Params map[string]any `json:"params"`
	// SourceTaskID 指向本执行任务所依据的扫描任务（扫描任务自身为 nil）。
	SourceTaskID *int64     `json:"sourceTaskId,omitempty"`
	Status       string     `json:"status"`
	TotalItems   int32      `json:"totalItems"`
	DoneItems    int32      `json:"doneItems"`
	FailedItems  int32      `json:"failedItems"`
	Error        *string    `json:"error,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

// 维护任务的两个阶段：scan 只把命中对象写入清单、不改动任何数据；apply 按清单逐项处理。
const (
	StorageTaskPhaseScan  = "scan"
	StorageTaskPhaseApply = "apply"
)

// ScanKindToTaskType 把管理端「维护选项」映射为任务类型（扫描与执行共用同一类型，
// 由 phase 区分）。孤立文件复用 purge_orphan：扫描阶段就是找出这些对象。
func ScanKindToTaskType(kind string) (string, bool) {
	switch kind {
	case "orphan":
		return "purge_orphan", true
	case "encrypt", "decrypt", "chunk":
		return kind, true
	}
	return "", false
}

// StorageTaskItemDetail 是物品清单的展示条目（供管理员确认扫描结果）。
type StorageTaskItemDetail struct {
	BlobID    string `json:"blobId"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	BytesDone int64  `json:"bytesDone"`
	SizeBytes int64  `json:"sizeBytes"`
	// Name 是引用该对象的资源名（秒传共享同一对象时取最早创建的一个）；孤儿对象为空。
	Name string `json:"name,omitempty"`
}

// StorageTaskItem 是任务的单个待处理对象（只承载执行所需的最小信息）。
type StorageTaskItem struct {
	BlobID string
	// OldBackendID / OldRefs 是「元数据已替换为新对象、旧物理对象尚未清理成功」
	// 的回溯记录：非空时执行器先按记录补删旧对象（清理成功后置空），避免旧对象
	// 成为无数据库记录的影子数据。
	OldBackendID *int64
	OldRefs      []string
}

// ParamInt64 从任务 params 中读取整数参数：params 可能来自 HTTP 层（int64），
// 也可能来自数据库回读（JSON 反序列化为 float64）。任务创建方与执行方共用。
func ParamInt64(params map[string]any, key string) (int64, bool) {
	switch value := params[key].(type) {
	case float64:
		return int64(value), true
	case int64:
		return value, true
	case int:
		return int64(value), true
	}
	return 0, false
}

// CancelQueuedScopedMigrations 取消某个用户组尚未开始的排队迁移任务，并把其
// 待处理明细标记为 skipped（返回被跳过的明细数量，调用方通常忽略）。管理员
// 反复切换组绑定时，先前排队的任务携带的是创建时的目标后端，若不取消就可能把
// 对象搬到已不再对应该组的后端；正在执行的任务不在此列（其最终结果会被随后
// 创建的新任务纠正）。
func (r *Repo) CancelQueuedScopedMigrations(ctx context.Context, groupID int64) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH canceled AS (
			UPDATE storage_tasks SET status = 'canceled', finished_at = NOW()
			WHERE task_type = 'migrate' AND status = 'queued'
			  AND params->>'scope_group_id' = $1
			RETURNING id
		)
		UPDATE storage_task_items SET status = 'skipped'
		WHERE task_id IN (SELECT id FROM canceled) AND status = 'pending'`,
		strconv.FormatInt(groupID, 10))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// storageTaskExpandSQL 构造「把命中对象写入物品清单」的语句与附加参数。
// 语句里 $1 固定为任务 id，其余占位符按返回的 args 顺序追加。
func storageTaskExpandSQL(taskType string, params map[string]any) (string, []any, error) {
	scope, _ := ParamInt64(params, "scope_backend_id") // 0 = 不限后端
	switch taskType {
	case "migrate":
		// 只搬有引用的对象：无引用的孤儿对象留在原后端由「孤立文件」清理，搬运它们
		// 只会浪费带宽与三方 API 调用。target=新位置；source=原位置（0 表示"除新位置
		// 外的全部"）。加密对象由服务端解密后以新密钥重新加密，因此迁移要求部署已
		// 配置加密密钥。
		target, _ := ParamInt64(params, "target_backend_id")
		if target <= 0 {
			return "", nil, errors.New("admin: 迁移任务缺少目标后端")
		}
		source, _ := ParamInt64(params, "source_backend_id")
		if groupID, ok := ParamInt64(params, "scope_group_id"); ok && groupID > 0 {
			// $1 显式标类型：DISTINCT 下 PostgreSQL 会把未定类型的参数解析为 text，
			// 与 storage_task_items.task_id（bigint）不匹配。
			return `INSERT INTO storage_task_items (task_id, blob_id)
				SELECT DISTINCT $1::bigint, b.id FROM storage_blobs b
				WHERE b.ref_count > 0
				  AND b.backend_id IN (SELECT id FROM storage_backends WHERE id <> $2)
				  AND ($3::bigint = 0 OR b.backend_id = $3)
				  AND EXISTS (
				      SELECT 1 FROM resources r
				      JOIN user_group_memberships m ON m.user_id = r.owner_user_id
				      WHERE r.blob_id = b.id AND m.group_id = $4
				  )`, []any{target, source, groupID}, nil
		}
		// `IN (子查询)` 而非 `<>`：可用 (backend_id, status) 索引定位，
		// `<>` 无法走索引（退化为全表扫描）。
		return `INSERT INTO storage_task_items (task_id, blob_id)
			SELECT $1, id FROM storage_blobs
			WHERE ref_count > 0
			  AND backend_id IN (SELECT id FROM storage_backends WHERE id <> $2)
			  AND ($3::bigint = 0 OR backend_id = $3)`, []any{target, source}, nil
	case "encrypt":
		// 孤儿对象无需补加密。
		return `INSERT INTO storage_task_items (task_id, blob_id)
			SELECT $1, id FROM storage_blobs
			WHERE encryption_algo IS NULL AND ref_count > 0
			  AND ($2::bigint = 0 OR backend_id = $2)`, []any{scope}, nil
	case "decrypt":
		return `INSERT INTO storage_task_items (task_id, blob_id)
			SELECT $1, id FROM storage_blobs
			WHERE encryption_algo IS NOT NULL AND ref_count > 0
			  AND ($2::bigint = 0 OR backend_id = $2)`, []any{scope}, nil
	case "chunk":
		chunkSize, _ := ParamInt64(params, "chunk_size")
		if chunkSize <= 0 {
			return "", nil, errors.New("admin: 分片任务缺少分片大小")
		}
		return `INSERT INTO storage_task_items (task_id, blob_id)
			SELECT $1, id FROM storage_blobs
			WHERE ref_count > 0 AND chunk_size <> $2
			  AND ($3::bigint = 0 OR backend_id = $3)`, []any{chunkSize, scope}, nil
	case "purge_orphan":
		// 只清理已登记为孤儿且超过 1 小时宽限期的对象：避免与正在进行的上传
		// （对象先以 orphaned 落库、Attach 前的窗口）竞争。宽限期按"最近一次状态
		// 变更时间"（updated_at：创建/Attach/释放都会刷新）计，而不是对象创建
		// 时间——否则早就创建、刚刚释放的对象没有任何保护窗。
		return `INSERT INTO storage_task_items (task_id, blob_id)
			SELECT $1, id FROM storage_blobs
			WHERE ref_count <= 0 AND status = 'orphaned'
			  AND updated_at < NOW() - INTERVAL '1 hour'
			  AND ($2::bigint = 0 OR backend_id = $2)`, []any{scope}, nil
	}
	return "", nil, errors.New("admin: 不支持的存储任务类型")
}

// insertStorageTask 是任务创建的公共流程：建任务行 → 写入命中对象清单 → 回填总数。
// scanOnly 为真时任务创建后立即结束（只产出清单，不改动任何数据）。
func (r *Repo) insertStorageTask(ctx context.Context, taskType, phase string, sourceTaskID *int64, params map[string]any, createdBy int64, scanOnly bool) (StorageTask, error) {
	if params == nil {
		params = map[string]any{}
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return StorageTask{}, err
	}
	expand, args, err := storageTaskExpandSQL(taskType, params)
	if err != nil {
		return StorageTask{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return StorageTask{}, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	if taskType == "migrate" {
		// 新位置必须存在且启用：否则任务会在每一项上失败，白白消耗调度与告警。
		target, _ := ParamInt64(params, "target_backend_id")
		var targetEnabled bool
		if err := tx.QueryRow(ctx, `SELECT is_enabled FROM storage_backends WHERE id=$1`, target).Scan(&targetEnabled); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return StorageTask{}, errors.New("admin: 目标存储后端不存在")
			}
			return StorageTask{}, err
		}
		if !targetEnabled {
			return StorageTask{}, errors.New("admin: 目标存储后端已停用")
		}
	}

	var task StorageTask
	var raw []byte
	if err := tx.QueryRow(ctx, `
		INSERT INTO storage_tasks (task_type, phase, source_task_id, params, created_by)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, task_type, phase, source_task_id, params, status, created_at`,
		taskType, phase, sourceTaskID, encoded, createdBy).
		Scan(&task.ID, &task.Type, &task.Phase, &task.SourceTaskID, &raw, &task.Status, &task.CreatedAt); err != nil {
		return StorageTask{}, err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &task.Params)
	}

	tag, err := tx.Exec(ctx, expand, append([]any{task.ID}, args...)...)
	if err != nil {
		return StorageTask{}, err
	}
	task.TotalItems = int32(tag.RowsAffected())
	if scanOnly {
		// 扫描任务只产出清单：立即结束，等管理员确认后再建执行任务。
		if _, err := tx.Exec(ctx, `UPDATE storage_tasks
			SET total_items=$2, status='done', started_at=NOW(), finished_at=NOW()
			WHERE id=$1`, task.ID, task.TotalItems); err != nil {
			return StorageTask{}, err
		}
		task.Status = "done"
	} else if _, err := tx.Exec(ctx, `UPDATE storage_tasks SET total_items=$2 WHERE id=$1`,
		task.ID, task.TotalItems); err != nil {
		return StorageTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StorageTask{}, err
	}
	return task, nil
}

// CreateStorageTask 创建「执行」任务：按类型即时扫描出清单并排队执行。
//
// 只有迁移走这里（管理员在界面选定原位置与新位置后直接发起；用户组切换存储绑定时
// 也会用它补一次增量迁移）。孤立文件/加密/解密/分片一律走 CreateScanTask +
// CreateTaskFromScan，由管理员确认清单后再执行——系统不会自动创建任何清理任务。
func (r *Repo) CreateStorageTask(ctx context.Context, taskType string, params map[string]any, createdBy int64) (StorageTask, error) {
	return r.insertStorageTask(ctx, taskType, StorageTaskPhaseApply, nil, params, createdBy, false)
}

// CreateScanTask 创建「扫描」任务：只把命中对象写入清单，创建后立即结束，不改动
// 任何数据。scopeBackendID 为 0 表示不限后端；chunkSize 仅分片扫描需要。
func (r *Repo) CreateScanTask(ctx context.Context, kind string, scopeBackendID, chunkSize, createdBy int64) (StorageTask, error) {
	taskType, ok := ScanKindToTaskType(kind)
	if !ok {
		return StorageTask{}, errors.New("admin: 不支持的扫描类型")
	}
	params := map[string]any{}
	if scopeBackendID > 0 {
		params["scope_backend_id"] = scopeBackendID
	}
	if taskType == "chunk" {
		if chunkSize <= 0 {
			return StorageTask{}, errors.New("admin: 分片任务缺少分片大小")
		}
		params["chunk_size"] = chunkSize
	}
	return r.insertStorageTask(ctx, taskType, StorageTaskPhaseScan, nil, params, createdBy, true)
}

// CreateTaskFromScan 依据扫描任务的结果清单创建执行任务：清单原样复制，不再重新
// 扫描（即"针对这个任务的结果执行"）。单项执行逻辑自带幂等判断，因此扫描与执行
// 之间数据发生变化也不会误操作（例如重新被引用的对象会被跳过）。
func (r *Repo) CreateTaskFromScan(ctx context.Context, scanTaskID, createdBy int64) (StorageTask, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return StorageTask{}, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	var taskType, phase string
	var raw []byte
	if err := tx.QueryRow(ctx, `
		SELECT task_type, phase, params FROM storage_tasks WHERE id=$1 FOR UPDATE`,
		scanTaskID).Scan(&taskType, &phase, &raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StorageTask{}, errors.New("admin: 扫描任务不存在")
		}
		return StorageTask{}, err
	}
	if phase != StorageTaskPhaseScan {
		return StorageTask{}, errors.New("admin: 只能按扫描任务的结果执行")
	}
	if taskType == "migrate" {
		return StorageTask{}, errors.New("admin: 迁移不经过扫描阶段，请直接选择原位置与新位置执行")
	}
	params := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &params)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return StorageTask{}, err
	}

	var task StorageTask
	var taskRaw []byte
	if err := tx.QueryRow(ctx, `
		INSERT INTO storage_tasks (task_type, phase, source_task_id, params, created_by)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, task_type, phase, source_task_id, params, status, created_at`,
		taskType, StorageTaskPhaseApply, scanTaskID, encoded, createdBy).
		Scan(&task.ID, &task.Type, &task.Phase, &task.SourceTaskID, &taskRaw, &task.Status, &task.CreatedAt); err != nil {
		return StorageTask{}, err
	}
	if len(taskRaw) > 0 {
		_ = json.Unmarshal(taskRaw, &task.Params)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO storage_task_items (task_id, blob_id)
		SELECT $1, blob_id FROM storage_task_items WHERE task_id=$2`, task.ID, scanTaskID)
	if err != nil {
		return StorageTask{}, err
	}
	task.TotalItems = int32(tag.RowsAffected())
	if task.TotalItems == 0 {
		return StorageTask{}, errors.New("admin: 该扫描结果为空，没有可执行的对象")
	}
	if _, err := tx.Exec(ctx, `UPDATE storage_tasks SET total_items=$2 WHERE id=$1`,
		task.ID, task.TotalItems); err != nil {
		return StorageTask{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return StorageTask{}, err
	}
	return task, nil
}

// ListStorageTaskItems 返回任务的对象清单（供管理员确认扫描结果/排查失败项）。
func (r *Repo) ListStorageTaskItems(ctx context.Context, taskID int64, limit int) ([]StorageTaskItemDetail, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT i.blob_id, i.status, COALESCE(i.error,''), i.bytes_done,
		       COALESCE(b.size_bytes,0), COALESCE(n.name,'')
		FROM storage_task_items i
		LEFT JOIN storage_blobs b ON b.id = i.blob_id
		LEFT JOIN LATERAL (
		    SELECT r.name FROM resources r WHERE r.blob_id = i.blob_id
		    ORDER BY r.created_at LIMIT 1
		) n ON TRUE
		WHERE i.task_id=$1
		ORDER BY i.status, i.blob_id
		LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StorageTaskItemDetail, 0)
	for rows.Next() {
		var item StorageTaskItemDetail
		if err := rows.Scan(&item.BlobID, &item.Status, &item.Error,
			&item.BytesDone, &item.SizeBytes, &item.Name); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ListStorageTasks 返回最近的任务（倒序）。
func (r *Repo) ListStorageTasks(ctx context.Context, limit int) ([]StorageTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, task_type, phase, source_task_id, params, status, total_items,
		       done_items, failed_items, error, created_at, started_at, finished_at
		FROM storage_tasks ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StorageTask, 0)
	for rows.Next() {
		var task StorageTask
		var raw []byte
		if err := rows.Scan(&task.ID, &task.Type, &task.Phase, &task.SourceTaskID, &raw, &task.Status,
			&task.TotalItems, &task.DoneItems, &task.FailedItems,
			&task.Error, &task.CreatedAt, &task.StartedAt, &task.FinishedAt); err != nil {
			return nil, err
		}
		task.Params = map[string]any{}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &task.Params)
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// ClaimQueuedStorageTask 原子领取一个排队中的「执行」任务并置为执行中；无任务时
// 返回 nil。扫描任务创建后即为已完成状态，不会被领取；这里仍显式限定 phase，
// 避免将来状态机调整时误执行扫描任务（扫描绝不能改动数据）。
func (r *Repo) ClaimQueuedStorageTask(ctx context.Context) (*StorageTask, error) {
	var task StorageTask
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		UPDATE storage_tasks SET status='running', started_at=NOW()
		WHERE id = (
			SELECT id FROM storage_tasks WHERE status='queued' AND phase='apply'
			ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED
		)
		RETURNING id, task_type, phase, params, status, total_items, created_at`).
		Scan(&task.ID, &task.Type, &task.Phase, &raw, &task.Status, &task.TotalItems, &task.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	task.Params = map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &task.Params)
	}
	return &task, nil
}

// FinishStorageTask 结束任务并写入最终状态。
func (r *Repo) FinishStorageTask(ctx context.Context, id int64, status, message string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE storage_tasks SET status=$2, error=NULLIF($3,''), finished_at=NOW()
		WHERE id=$1`, id, status, message)
	return err
}

// FailInterruptedStorageTasks 在进程启动时把上次中断的执行中任务标记为失败
// （迁移与补加密是幂等操作，管理员可重新发起）。
func (r *Repo) FailInterruptedStorageTasks(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE storage_tasks SET status='failed', error='服务重启导致任务中断，请重新发起', finished_at=NOW()
		WHERE status='running'`)
	return err
}

// ListPendingStorageTaskItems 取一批待处理对象（顺序执行，避免对后端造成压力）。
func (r *Repo) ListPendingStorageTaskItems(ctx context.Context, taskID int64, limit int) ([]StorageTaskItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT blob_id, old_backend_id, old_object_refs FROM storage_task_items
		WHERE task_id=$1 AND status='pending' ORDER BY blob_id LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]StorageTaskItem, 0)
	for rows.Next() {
		var item StorageTaskItem
		if err := rows.Scan(&item.BlobID, &item.OldBackendID, &item.OldRefs); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkStorageTaskItemOldRefs 在替换对象元数据之前记录旧物理对象的定位符。
// 替换成功后若旧对象删除失败，重跑该任务项时会按此记录补删——元数据一旦指向
// 新对象，旧对象就再无其它记录可查。
func (r *Repo) MarkStorageTaskItemOldRefs(ctx context.Context, taskID int64, blobID string, backendID int64, refs []string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE storage_task_items SET old_backend_id=$3, old_object_refs=$4, updated_at=NOW()
		WHERE task_id=$1 AND blob_id=$2`, taskID, blobID, backendID, refs)
	return err
}

// ClearStorageTaskItemOldRefs 在旧对象清理成功后清除回溯记录（幂等）。
func (r *Repo) ClearStorageTaskItemOldRefs(ctx context.Context, taskID int64, blobID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE storage_task_items SET old_backend_id=NULL, old_object_refs=NULL, updated_at=NOW()
		WHERE task_id=$1 AND blob_id=$2`, taskID, blobID)
	return err
}

// MarkStorageTaskItem 记录单项处理结果，并同步增减任务计数。
func (r *Repo) MarkStorageTaskItem(ctx context.Context, taskID int64, blobID, status, message string, bytes int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	tag, err := tx.Exec(ctx, `
		UPDATE storage_task_items SET status=$3, error=NULLIF($4,''), bytes_done=$5, updated_at=NOW()
		WHERE task_id=$1 AND blob_id=$2 AND status='pending'`,
		taskID, blobID, status, message, bytes)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	increment := `done_items = done_items + 1`
	if status == "failed" {
		increment = `failed_items = failed_items + 1`
	}
	if _, err := tx.Exec(ctx, `UPDATE storage_tasks SET `+increment+` WHERE id=$1`, taskID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// StorageBackendHasObjects 判断该后端是否已经存放过对象（用于禁止改存储类型）。
func (r *Repo) StorageBackendHasObjects(ctx context.Context, backendID int64) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM storage_blobs WHERE backend_id=$1)`, backendID).Scan(&exists)
	return exists, err
}
