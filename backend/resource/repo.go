// Package resource 管理用户文件与文件夹元数据。
package resource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	KindFile   = "file"
	KindFolder = "folder"
)

var (
	ErrNotFound      = errors.New("resource: 不存在")
	ErrNotFolder     = errors.New("resource: 父资源不是文件夹")
	ErrInvalidName   = errors.New("resource: 名称不合法")
	ErrOwnerMismatch = errors.New("resource: 不属于当前用户")
	ErrNameConflict  = errors.New("resource: 同名资源冲突")
	// ErrAdminBlocked 表示目标文件已被管理员拉黑，拒绝一切原地改写（如覆盖上传）。
	ErrAdminBlocked = errors.New("resource: 文件已被管理员限制")
)

type Resource struct {
	ID          string  `json:"id"`
	OwnerUserID int64   `json:"-"`
	ParentID    *string `json:"parentId,omitempty"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	// 注意：历史上还有 legacy 列 storage_key（本地文件时代的存储键），随对象模型
	// 重构已彻底停用并在迁移 036 中删除，代码层不再保留该字段。
	BlobID         *string    `json:"-"`
	SizeBytes      int64      `json:"sizeBytes"`
	SHA256Checksum *string    `json:"sha256,omitempty"`
	MimeType       *string    `json:"mimeType,omitempty"`
	ReviewStatus   string     `json:"reviewStatus,omitempty"`
	ReviewReason   string     `json:"reviewReason,omitempty"`
	TrashedAt      *time.Time `json:"trashedAt,omitempty"`
	RestoreBlocked bool       `json:"restoreBlocked,omitempty"`
	AdminBlocked   bool       `json:"adminBlocked,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type TreeEntry struct {
	Resource
	RelativePath string `json:"relativePath"`
}

type Repo struct {
	pool  *pgxpool.Pool
	blobs *blobstore.Service
}

// NewRepo 构造资源仓库。blobs 用于对象引用计数（Attach/Release）；测试场景
// 允许为 nil（资源写入仍可用，但不会维护引用计数）。
func NewRepo(pool *pgxpool.Pool, blobs *blobstore.Service) *Repo {
	return &Repo{pool: pool, blobs: blobs}
}

// HistoryEntry 描述一条追加到 file_moderation_history 的审核/处置事件。
//
// 历史只追加不修改：覆盖上传会归档被替换的旧状态，用户彻底删除会归档文件与
// 审核快照。资源行最终被管理员清理后，历史仍然保留（resource_id 无外键）。
type HistoryEntry struct {
	ResourceID   string
	OwnerUserID  int64
	FileName     string
	SizeBytes    int64
	MimeType     *string
	UploadTaskID string
	Event        string
	Status       string
	Reason       string
	DeleteFile   bool
	Blocked      bool
	ActorType    string
	ActorName    string
	Details      any
}

// SQLExecer 由 pgxpool.Pool 与 pgx.Tx 共同实现，调用方可在事务内或事务外记录历史。
type SQLExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// SQLQuerier 由 pgxpool.Pool 与 pgx.Tx 共同实现，供需要在事务内外复用的查询函数使用。
type SQLQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// RecordModerationHistory 追加一条审核/处置历史。
func RecordModerationHistory(ctx context.Context, exec SQLExecer, e HistoryEntry) error {
	var details []byte
	if e.Details != nil {
		marshaled, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("序列化审核历史详情: %w", err)
		}
		details = marshaled
	}
	_, err := exec.Exec(ctx, `
		INSERT INTO file_moderation_history (
			resource_id, owner_user_id, file_name, size_bytes, mime_type, upload_task_id,
			event, status, reason, delete_file, blocked, actor_type, actor_name, details
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		e.ResourceID, e.OwnerUserID, e.FileName, e.SizeBytes, e.MimeType, e.UploadTaskID,
		e.Event, e.Status, e.Reason, e.DeleteFile, e.Blocked, e.ActorType, e.ActorName, details)
	if err != nil {
		return fmt.Errorf("保存审核历史: %w", err)
	}
	return nil
}

func ValidateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 255 {
		return "", ErrInvalidName
	}
	if strings.ContainsAny(name, "/\\\x00") {
		return "", ErrInvalidName
	}
	return name, nil
}

func (r *Repo) CreateFolder(ctx context.Context, ownerID int64, parentID *string, name string) (Resource, error) {
	name, err := ValidateName(name)
	if err != nil {
		return Resource{}, err
	}
	if err := r.validateParent(ctx, ownerID, parentID); err != nil {
		return Resource{}, err
	}
	id, err := randomtoken.New(18)
	if err != nil {
		return Resource{}, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO resources (id, owner_user_id, parent_id, kind, name)
		VALUES ($1, $2, $3, 'folder', $4)
		RETURNING id, owner_user_id, parent_id, kind, name,
		          size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
		id, ownerID, parentID, name)
	return scanResource(row)
}

// 说明：文件资源统一由 SaveUploadedFile（上传链路）创建并绑定物理对象，
// 旧的 CreateFile（按存储键直接建资源）已随对象模型重构移除。

// SaveUploadedFile 原子地写入上传资源及其审核状态。覆盖时返回待清理的旧存储键；
// 任一数据库操作失败都会回滚，避免资源元数据和审核状态不一致。
func (r *Repo) SaveUploadedFile(
	ctx context.Context, ownerID int64, parentID *string, name, blobID string,
	sizeBytes int64, checksum, mimeType, conflictAction string, requiresReview bool,
	uploadTaskID, uploadSessionID string,
) (Resource, error) {
	overwrite := conflictAction == "overwrite"
	autoRename := conflictAction == "auto_rename"
	name, err := ValidateName(name)
	if err != nil {
		return Resource{}, err
	}
	if err := r.validateParent(ctx, ownerID, parentID); err != nil {
		return Resource{}, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Resource{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	newID, err := randomtoken.New(18)
	if err != nil {
		return Resource{}, err
	}
	var id, kind string
	var oldBlobID *string
	var oldSize int64
	var oldChecksum, oldMimeType *string
	var oldAdminBlocked bool
	var previousStatus, previousReason string
	var previousDeleteFile, previousBlocked bool
	existing := true
	err = tx.QueryRow(ctx, `
		SELECT resource.id,resource.kind,resource.blob_id,resource.size_bytes,
		       resource.sha256_checksum,resource.mime_type,resource.admin_blocked,
		       COALESCE(moderation.status,''),COALESCE(moderation.reason,''),
		       COALESCE(moderation.delete_file,FALSE),COALESCE(moderation.blocked,FALSE)
		FROM resources resource
		LEFT JOIN file_moderations moderation ON moderation.resource_id=resource.id
		WHERE resource.owner_user_id=$1 AND resource.parent_id IS NOT DISTINCT FROM $2
		  AND resource.name=$3 AND resource.trashed_at IS NULL
		FOR UPDATE OF resource`, ownerID, parentID, name).Scan(
		&id, &kind, &oldBlobID, &oldSize, &oldChecksum, &oldMimeType, &oldAdminBlocked,
		&previousStatus, &previousReason, &previousDeleteFile, &previousBlocked)
	if errors.Is(err, pgx.ErrNoRows) {
		id = newID
		existing = false
	} else if err != nil {
		return Resource{}, err
	} else if oldAdminBlocked {
		// 被管理员拉黑的文件不允许通过覆盖上传"洗白"：内容替换会清除拉黑状态、
		// 让违规内容重新可用。用户需改名上传为新文件，由管理员重新审核。
		return Resource{}, ErrAdminBlocked
	} else if autoRename && kind == KindFile {
		// 同名文件已存在且选择"保留两者"：不覆盖旧文件，按新资源插入；名字在
		// 事务内按当时占用情况解析（insert 冲突时再重试），因此不依赖 init 时
		// 冻结的名字，并发同名也能各自拿到唯一名字。
		id = newID
		existing = false
		resolved, resolveErr := availableChildNameQ(ctx, tx, ownerID, parentID, name, uploadSessionID)
		if resolveErr != nil {
			return Resource{}, resolveErr
		}
		name = resolved
	} else if !overwrite || kind != KindFile {
		return Resource{}, ErrNameConflict
	}

	var row pgx.Row
	if existing {
		// 覆盖上传替换了旧内容与旧审核状态：先把被替换的旧快照写入审核历史，
		// 保证管理员能追溯"这个文件曾被什么内容替换过"。
		if err := RecordModerationHistory(ctx, tx, HistoryEntry{
			ResourceID: id, OwnerUserID: ownerID, FileName: name, SizeBytes: oldSize,
			MimeType: oldMimeType, UploadTaskID: uploadTaskID,
			Event: "overwritten", Status: previousStatus, Reason: previousReason,
			DeleteFile: previousDeleteFile, Blocked: previousBlocked,
			ActorType: "user",
			Details: map[string]any{
				"previousSizeBytes": oldSize,
				"previousSHA256":    oldChecksum,
				"previousBlobID":    oldBlobID,
				"newSizeBytes":      sizeBytes,
				"newSHA256":         checksum,
			},
		}); err != nil {
			return Resource{}, err
		}
		row = tx.QueryRow(ctx, `
			UPDATE resources SET blob_id=$4,size_bytes=$5,sha256_checksum=$6,
			       mime_type=NULLIF($7,''),updated_at=NOW()
			WHERE id=$1 AND owner_user_id=$2 AND name=$3
			RETURNING id, owner_user_id, parent_id, kind, name,
			          size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
			id, ownerID, name, blobID, sizeBytes, checksum, mimeType)
	} else {
		// "保留两者"可能因并发重名撞上唯一索引：必须先把保存点建好，冲突后才有
		// 退路。唯一冲突会让整个事务进入 aborted 状态，此后除 ROLLBACK 外的语句
		// 一律报 25P02——没有保存点的"事务内重试"实际只会立刻失败并返回 500。
		if autoRename {
			if _, spErr := tx.Exec(ctx, "SAVEPOINT auto_rename"); spErr != nil {
				return Resource{}, spErr
			}
		}
		row = insertFileResource(ctx, tx, id, ownerID, parentID, name, blobID, sizeBytes, checksum, mimeType)
	}
	item, err := scanResource(row)
	if err != nil && autoRename && isUniqueViolation(err) {
		// 并发"保留两者"：解析出的名字可能刚被其它事务占用，回到保存点后重新解析重试。
		for attempt := 0; attempt < 5; attempt++ {
			if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT auto_rename"); rbErr != nil {
				return Resource{}, rbErr
			}
			resolved, resolveErr := availableChildNameQ(ctx, tx, ownerID, parentID, name, uploadSessionID)
			if resolveErr != nil {
				return Resource{}, resolveErr
			}
			name = resolved
			item, err = scanResource(insertFileResource(ctx, tx, id, ownerID, parentID, name, blobID, sizeBytes, checksum, mimeType))
			if err == nil {
				if _, relErr := tx.Exec(ctx, "RELEASE SAVEPOINT auto_rename"); relErr != nil {
					return Resource{}, relErr
				}
				break
			}
			if !isUniqueViolation(err) {
				return Resource{}, err
			}
		}
	}
	if err != nil {
		return Resource{}, err
	}
	// 每日额度以"上传事件"为准：覆盖上传同样计次计流量；事件不随资源删除
	// （含管理员物理清理）消失，杜绝"删除后重传重置当日额度"。
	usageKind := "upload"
	if existing {
		usageKind = "overwrite"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO upload_usage_events (owner_user_id, resource_id, size_bytes, kind)
		VALUES ($1,$2,$3,$4)`, ownerID, item.ID, sizeBytes, usageKind); err != nil {
		return Resource{}, err
	}
	// 引用计数与资源写入同一事务：新对象 Attach，被替换的旧对象 Release
	// （归零后标记 orphaned，物理文件由管理员清理）。
	if r.blobs != nil {
		if err := r.blobs.Attach(ctx, tx, blobID); err != nil {
			return Resource{}, err
		}
		if existing && oldBlobID != nil {
			if err := r.blobs.ReleaseInTx(ctx, tx, *oldBlobID); err != nil {
				return Resource{}, err
			}
		}
	}
	if requiresReview {
		_, err = tx.Exec(ctx, `
			INSERT INTO file_moderations(
				resource_id,owner_user_id,file_name,size_bytes,mime_type,upload_task_id,
				status,reason,submitted_at,reviewed_at,reviewer_user_id
			) VALUES($1,$2,$3,$4,NULLIF($5,''),COALESCE(NULLIF($6,''),$1),
			         'pending','',NOW(),NULL,NULL)
			ON CONFLICT(resource_id) DO UPDATE SET
				owner_user_id=EXCLUDED.owner_user_id,file_name=EXCLUDED.file_name,
				size_bytes=EXCLUDED.size_bytes,mime_type=EXCLUDED.mime_type,
				upload_task_id=EXCLUDED.upload_task_id,status='pending',reason='',
				delete_file=FALSE,blocked=FALSE,submitted_at=NOW(),
				reviewed_at=NULL,reviewer_user_id=NULL`,
			item.ID, ownerID, name, sizeBytes, mimeType, uploadTaskID)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM file_moderations WHERE resource_id=$1`, item.ID)
	}
	if err != nil {
		return Resource{}, err
	}
	result, err := tx.Exec(ctx, `
		UPDATE upload_sessions
		SET status='completed',resource_id=$3,error_message='',updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2
		  AND status IN ('queued','uploading','paused','completing')`,
		uploadSessionID, ownerID, item.ID)
	if err != nil {
		return Resource{}, err
	}
	if result.RowsAffected() == 0 {
		return Resource{}, fmt.Errorf("resource: 上传任务状态无效")
	}
	if err := tx.Commit(ctx); err != nil {
		return Resource{}, err
	}
	return item, nil
}

func (r *Repo) ExistingChildNames(ctx context.Context, ownerID int64, parentID *string, names []string) (map[string]bool, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT name FROM resources
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
		  AND trashed_at IS NULL AND name=ANY($3)
		UNION
		SELECT filename FROM upload_sessions
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
		  AND status IN ('queued','uploading','paused','completing') AND filename=ANY($3)`, ownerID, parentID, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// availableChildNameQ 在给定查询器（连接池或事务）内解析可用文件名：占用集合
// 同时考虑活动资源与进行中的上传会话；excludeSessionID 排除自身会话（避免
// "自己的会话占用原名"导致重挂同一文件被改名）。
func availableChildNameQ(ctx context.Context, q SQLQuerier, ownerID int64, parentID *string, name, excludeSessionID string) (string, error) {
	name, err := ValidateName(name)
	if err != nil {
		return "", err
	}
	rows, err := q.Query(ctx, `
		SELECT name FROM resources
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
		  AND trashed_at IS NULL
		UNION
		SELECT filename FROM upload_sessions
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
		  AND status IN ('queued','uploading','paused','completing')
		  AND ($3 = '' OR id <> $3)`,
		ownerID, parentID, excludeSessionID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	occupied := make(map[string]struct{})
	for rows.Next() {
		var occupiedName string
		if err := rows.Scan(&occupiedName); err != nil {
			return "", err
		}
		occupied[occupiedName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if _, exists := occupied[name]; !exists {
		return name, nil
	}
	dot := strings.LastIndex(name, ".")
	base, ext := name, ""
	if dot > 0 {
		base, ext = name[:dot], name[dot:]
	}
	for index := 1; index < 10000; index++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, index, ext)
		if _, exists := occupied[candidate]; !exists {
			return candidate, nil
		}
	}
	return "", ErrNameConflict
}

func (r *Repo) ExistingChildFileSize(ctx context.Context, ownerID int64, parentID *string, name string) (int64, error) {
	name, err := ValidateName(name)
	if err != nil {
		return 0, err
	}
	var size int64
	err = r.pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT size_bytes FROM resources
			WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
			  AND name=$3 AND kind='file' AND trashed_at IS NULL
		), 0)::BIGINT`, ownerID, parentID, name).Scan(&size)
	return size, err
}

func (r *Repo) GetByID(ctx context.Context, id string) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, parent_id, kind, name,
		       size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at
		FROM resources WHERE id = $1 AND trashed_at IS NULL`, id)
	res, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return res, err
}

func (r *Repo) GetByIDIncludingTrash(ctx context.Context, id string) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, parent_id, kind, name,
		       size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at
		FROM resources WHERE id=$1`, id)
	item, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return item, err
}

func (r *Repo) GetOwned(ctx context.Context, ownerID int64, id string) (Resource, error) {
	res, err := r.GetByID(ctx, id)
	if err != nil {
		return Resource{}, err
	}
	if res.OwnerUserID != ownerID {
		return Resource{}, ErrOwnerMismatch
	}
	return res, nil
}

// insertFileResource 插入一条文件资源行（供新建与"保留两者"复用）。
func insertFileResource(ctx context.Context, tx pgx.Tx, id string, ownerID int64, parentID *string, name, blobID string, sizeBytes int64, checksum, mimeType string) pgx.Row {
	return tx.QueryRow(ctx, `
		INSERT INTO resources (
			id, owner_user_id, parent_id, kind, name, blob_id,
			size_bytes, sha256_checksum, mime_type
		) VALUES ($1, $2, $3, 'file', $4, $5, $6, $7, NULLIF($8, ''))
		RETURNING id, owner_user_id, parent_id, kind, name,
		          size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
		id, ownerID, parentID, name, blobID, sizeBytes, checksum, mimeType)
}

// isUniqueViolation 判断错误是否为唯一约束冲突（23505）。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// FindActiveChildFile 返回活动目录中指定名称的同名文件（含被管理员拉黑的）。
// 上传初始化阶段据此提前拒绝覆盖被限制的文件，避免用户传完全部分片才失败；
// complete 阶段仍由 SaveUploadedFile 兜底校验。
func (r *Repo) FindActiveChildFile(ctx context.Context, ownerID int64, parentID *string, name string) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, parent_id, kind, name,
		       size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at
		FROM resources
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND name=$3 AND trashed_at IS NULL
		LIMIT 1`, ownerID, parentID, name)
	item, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return item, err
}

func (r *Repo) RenameOwned(ctx context.Context, ownerID int64, id, name string) (Resource, error) {
	name, err := ValidateName(name)
	if err != nil {
		return Resource{}, err
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE resources SET name=$3, updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2 AND trashed_at IS NULL AND NOT admin_blocked
		RETURNING id, owner_user_id, parent_id, kind, name,
		          size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
		id, ownerID, name)
	item, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return item, err
}

func (r *Repo) ListChildren(ctx context.Context, ownerID int64, parentID *string) ([]Resource, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.owner_user_id, r.parent_id, r.kind, r.name,
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.blob_id, r.trashed_at, r.restore_blocked, r.admin_blocked, r.created_at, r.updated_at,
		       COALESCE(fr.status, 'approved'), COALESCE(fr.reason, '')
		FROM resources r LEFT JOIN file_moderations fr ON fr.resource_id=r.id
		WHERE r.owner_user_id = $1 AND r.parent_id IS NOT DISTINCT FROM $2 AND r.trashed_at IS NULL
		ORDER BY r.kind DESC, r.name`, ownerID, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Resource
	for rows.Next() {
		item, scanErr := scanResourceWithReview(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repo) ListTree(ctx context.Context, rootID string) ([]TreeEntry, error) {
	rows, err := r.pool.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT r.*, r.name::TEXT AS relative_path
			FROM resources r WHERE r.id = $1 AND r.trashed_at IS NULL
			UNION ALL
			SELECT child.*, (tree.relative_path || '/' || child.name)::TEXT
			FROM resources child
			JOIN tree ON child.parent_id = tree.id
			WHERE child.trashed_at IS NULL
		)
		SELECT id, owner_user_id, parent_id, kind, name,
		       size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at,
		       relative_path
		FROM tree ORDER BY relative_path`, rootID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TreeEntry
	for rows.Next() {
		var item TreeEntry
		if err := rows.Scan(
			&item.ID, &item.OwnerUserID, &item.ParentID, &item.Kind, &item.Name,
			&item.SizeBytes, &item.SHA256Checksum, &item.MimeType, &item.BlobID,
			&item.TrashedAt, &item.RestoreBlocked, &item.AdminBlocked, &item.CreatedAt, &item.UpdatedAt, &item.RelativePath,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, nil
}

// ListTrashOwned 只列出当前用户回收站中的顶层项目；文件夹后代随顶层项目一同恢复。
// 用户"彻底删除"（purged_at 非空）的项目对用户不可见，但资源行与物理文件保留
// 供管理员审查与清理。
func (r *Repo) ListTrashOwned(ctx context.Context, ownerID int64) ([]Resource, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.owner_user_id, r.parent_id, r.kind, r.name,
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.blob_id, r.trashed_at, r.restore_blocked, r.admin_blocked, r.created_at, r.updated_at
		FROM resources r
		LEFT JOIN resources parent ON parent.id=r.parent_id
		WHERE r.owner_user_id=$1 AND r.trashed_at IS NOT NULL AND r.purged_at IS NULL
		  AND (r.parent_id IS NULL OR parent.trashed_at IS NULL)
		ORDER BY r.trashed_at DESC, r.name`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Resource, 0)
	for rows.Next() {
		item, scanErr := scanResource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repo) MoveToTrashOwned(ctx context.Context, ownerID int64, id string) error {
	tag, err := r.pool.Exec(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id FROM resources
			WHERE id=$1 AND owner_user_id=$2 AND trashed_at IS NULL AND NOT admin_blocked
			UNION ALL
			SELECT child.id FROM resources child JOIN tree ON child.parent_id=tree.id
			WHERE child.owner_user_id=$2 AND child.trashed_at IS NULL AND child.purged_at IS NULL
		)
		UPDATE resources SET trashed_at=NOW(), updated_at=NOW()
		WHERE id IN (SELECT id FROM tree)`, id, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) RestoreOwned(ctx context.Context, ownerID int64, id string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	// purged_at 非空表示用户已彻底删除：不可再恢复（资源行仍存在，仅管理员可见）。
	var name string
	var parentID *string
	if err := tx.QueryRow(ctx, `
		SELECT root.name, root.parent_id FROM resources root
		LEFT JOIN resources parent ON parent.id=root.parent_id
		WHERE root.id=$1 AND root.owner_user_id=$2 AND root.trashed_at IS NOT NULL
		  AND NOT root.restore_blocked AND root.purged_at IS NULL
		  AND (root.parent_id IS NULL OR parent.trashed_at IS NULL)
		FOR UPDATE OF root`, id, ownerID).Scan(&name, &parentID); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	// 目标位置已有同名条目时自动改名（与"保留两者"一致），避免恢复直接失败。
	// 同名冲突只可能发生在根条目这一层：子树内部随根一起恢复，不会有活动同级。
	restoredName := name
	var occupied bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM resources
			WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND name=$3 AND trashed_at IS NULL
		)`, ownerID, parentID, name).Scan(&occupied); err != nil {
		return "", err
	}
	if occupied {
		resolved, resolveErr := availableChildNameQ(ctx, tx, ownerID, parentID, name, "")
		if resolveErr != nil {
			return "", resolveErr
		}
		restoredName = resolved
	}
	tag, err := tx.Exec(ctx, `
		WITH RECURSIVE tree AS (
			SELECT root.id FROM resources root
			LEFT JOIN resources parent ON parent.id=root.parent_id
			WHERE root.id=$1 AND root.owner_user_id=$2 AND root.trashed_at IS NOT NULL AND NOT root.restore_blocked
			  AND root.purged_at IS NULL
			  AND (root.parent_id IS NULL OR parent.trashed_at IS NULL)
			UNION ALL
			SELECT child.id FROM resources child JOIN tree ON child.parent_id=tree.id
			WHERE child.owner_user_id=$2 AND child.trashed_at IS NOT NULL AND child.purged_at IS NULL
		)
		UPDATE resources SET trashed_at=NULL, name=CASE WHEN id=$1 THEN $3 ELSE name END, updated_at=NOW()
		WHERE id IN (SELECT id FROM tree)`, id, ownerID, restoredName)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return restoredName, nil
}

// SetAdminDisposition 应用审核处置；只有管理员审核造成的删除会在撤销时自动恢复。
// 用户已彻底删除（purged_at 非空）的文件不因撤销处置回到用户目录——用户已表示
// 不再保留该文件，撤销只清除"限制恢复"状态。
func (r *Repo) SetAdminDisposition(ctx context.Context, id string, deleteFile, blocked bool) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE resources SET
			trashed_at=CASE
				WHEN $2 THEN COALESCE(trashed_at,NOW())
				WHEN restore_blocked AND purged_at IS NULL THEN NULL
				ELSE trashed_at
			END,
			restore_blocked=$2,
			admin_blocked=$3,
			updated_at=NOW()
		WHERE id=$1 AND kind='file'
		RETURNING id, owner_user_id, parent_id, kind, name,
		          size_bytes, sha256_checksum, mime_type, blob_id, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`, id, deleteFile, blocked)
	item, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, err
	}
	// 直链治理联动：文件被拉黑/解除时同步封禁/恢复其直链。direct_links.admin_blocked
	// 原本只有读取路径、没有任何写入入口，违规直链无法下架。
	if _, err := r.pool.Exec(ctx, `UPDATE direct_links SET admin_blocked=$2 WHERE resource_id=$1`, id, blocked); err != nil {
		return Resource{}, err
	}
	return item, nil
}

// DeleteAdminTrashedFile 由管理员物理清理受限回收站中的文件，是物理删除的唯一入口。
// 删除前先把当前状态追加到审核历史：资源行被删除后，管理员审查页仍可通过
// file_moderations 与历史记录追溯该文件。
func (r *Repo) DeleteAdminTrashedFile(ctx context.Context, id string) (Resource, error) {
	item, err := r.GetByIDIncludingTrash(ctx, id)
	if err != nil || item.Kind != KindFile || item.TrashedAt == nil || !item.RestoreBlocked {
		return Resource{}, ErrNotFound
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Resource{}, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if err := RecordModerationHistory(ctx, tx, HistoryEntry{
		ResourceID: item.ID, OwnerUserID: item.OwnerUserID, FileName: item.Name,
		SizeBytes: item.SizeBytes, MimeType: item.MimeType,
		Event: "purged", DeleteFile: item.RestoreBlocked, Blocked: item.AdminBlocked,
		ActorType: "admin", Details: map[string]any{"source": "admin_review_trash"},
	}); err != nil {
		return Resource{}, err
	}
	// 删除与"取回 purged_at"必须在同一条语句里完成：先 DELETE 再按同一 id 查询
	// 时行已不存在，必然 ErrNoRows，会让整个事务回滚——管理员"永久删除"过去
	// 从不生效（且静默）。用 RETURNING 读取删除前的快照，既正确又少一次往返。
	//
	// 之所以要判断 purged_at：已被用户"彻底删除"的行在 purge 时已释放过引用，
	// 重复释放会把同一对象多扣一次，可能让其它引用者的文件被误判为孤儿并物理删除。
	var alreadyPurged bool
	err = tx.QueryRow(ctx, `DELETE FROM resources
		WHERE id=$1 AND kind='file' AND trashed_at IS NOT NULL AND restore_blocked
		RETURNING purged_at IS NOT NULL`, id).Scan(&alreadyPurged)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	if err != nil {
		return Resource{}, err
	}
	// 物理对象只减引用不删文件：管理员清理工具处理 orphaned 对象。
	if r.blobs != nil && item.BlobID != nil && !alreadyPurged {
		if err := r.blobs.ReleaseInTx(ctx, tx, *item.BlobID); err != nil {
			return Resource{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Resource{}, err
	}
	return item, nil
}

// PurgeTrashedOwned 把用户回收站中的顶层项目（含整棵子树）标记为"彻底删除"。
//
// 设计：purged_at 一旦写入，用户不可见、不可恢复；资源行与 purged 历史保留
// （管理员审查页仍可追溯），并在同一事务内释放对象引用——物理文件由孤儿清理
// 任务回收，不再无限期占用磁盘（此前只标记不释放，普通用户彻底删除的对象
// 永远无法回收）。每个文件在被标记前写入一条 purged 历史，记录删除时的审核状态。
func (r *Repo) PurgeTrashedOwned(ctx context.Context, ownerID int64, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id FROM resources
			WHERE id=$1 AND owner_user_id=$2 AND trashed_at IS NOT NULL AND purged_at IS NULL
			  AND NOT restore_blocked AND NOT admin_blocked
			UNION ALL
			SELECT child.id FROM resources child JOIN tree ON child.parent_id=tree.id
			WHERE child.owner_user_id=$2 AND child.purged_at IS NULL
			  AND NOT child.restore_blocked AND NOT child.admin_blocked
		)
		INSERT INTO file_moderation_history (
			resource_id, owner_user_id, file_name, size_bytes, mime_type, upload_task_id,
			event, status, reason, delete_file, blocked, actor_type, actor_name, details
		)
		SELECT r.id, r.owner_user_id, r.name, r.size_bytes, r.mime_type,
		       COALESCE(NULLIF(s.batch_id,''), s.id, r.id),
		       'purged', COALESCE(m.status,''), COALESCE(m.reason,''),
		       COALESCE(m.delete_file,FALSE), COALESCE(m.blocked,FALSE),
		       'user', '', jsonb_build_object('source', 'trash_item')
		FROM resources r
		LEFT JOIN LATERAL (
			SELECT id, batch_id FROM upload_sessions
			WHERE resource_id = r.id ORDER BY created_at DESC LIMIT 1
		) s ON TRUE
		LEFT JOIN file_moderations m ON m.resource_id = r.id
		WHERE r.kind='file' AND r.id IN (SELECT id FROM tree)`, id, ownerID); err != nil {
		return err
	}

	// 标记彻底删除并释放对象引用（同一语句）：UPDATE 自带 purged_at IS NULL
	// 条件，并发清空回收站/到期清理时不会对同一行重复扣减引用。同一对象被多个
	// 资源复用时按引用份数扣减；引用归零后转为 orphaned，由孤儿清理任务回收。
	var purgedCount int64
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE tree AS (
			SELECT id FROM resources
			WHERE id=$1 AND owner_user_id=$2 AND trashed_at IS NOT NULL AND purged_at IS NULL
			  AND NOT restore_blocked AND NOT admin_blocked
			UNION ALL
			SELECT child.id FROM resources child JOIN tree ON child.parent_id=tree.id
			WHERE child.owner_user_id=$2 AND child.purged_at IS NULL
			  AND NOT child.restore_blocked AND NOT child.admin_blocked
		),
		updated AS (
			UPDATE resources SET purged_at=NOW(), updated_at=NOW()
			WHERE id IN (SELECT id FROM tree) AND purged_at IS NULL
			RETURNING blob_id
		),
		refs AS (
			SELECT blob_id, COUNT(*) AS refs FROM updated WHERE blob_id IS NOT NULL GROUP BY blob_id
		),
		released AS (
			UPDATE storage_blobs b SET
				ref_count = GREATEST(b.ref_count - refs.refs, 0),
				status = CASE WHEN b.ref_count - refs.refs <= 0 THEN 'orphaned' ELSE b.status END,
				updated_at = NOW()
			FROM refs WHERE b.id = refs.blob_id
			RETURNING 1
		)
		SELECT COUNT(*) FROM updated`, id, ownerID).Scan(&purgedCount); err != nil {
		return err
	}
	if purgedCount == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// PurgeAllTrashOwned 把当前用户回收站中的全部内容标记为"彻底删除"。
// 语义与 PurgeTrashedOwned 相同，仅批量执行。
func (r *Repo) PurgeAllTrashOwned(ctx context.Context, ownerID int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO file_moderation_history (
			resource_id, owner_user_id, file_name, size_bytes, mime_type, upload_task_id,
			event, status, reason, delete_file, blocked, actor_type, actor_name, details
		)
		SELECT r.id, r.owner_user_id, r.name, r.size_bytes, r.mime_type,
		       COALESCE(NULLIF(s.batch_id,''), s.id, r.id),
		       'purged', COALESCE(m.status,''), COALESCE(m.reason,''),
		       COALESCE(m.delete_file,FALSE), COALESCE(m.blocked,FALSE),
		       'user', '', jsonb_build_object('source', 'empty_trash')
		FROM resources r
		LEFT JOIN LATERAL (
			SELECT id, batch_id FROM upload_sessions
			WHERE resource_id = r.id ORDER BY created_at DESC LIMIT 1
		) s ON TRUE
		LEFT JOIN file_moderations m ON m.resource_id = r.id
		WHERE r.kind='file' AND r.owner_user_id=$1 AND r.trashed_at IS NOT NULL AND r.purged_at IS NULL
		  AND NOT r.restore_blocked AND NOT r.admin_blocked`, ownerID); err != nil {
		return err
	}

	// 标记并与释放引用放在同一语句：并发到期清理不会对同一行重复扣减引用。
	// 管理员受限文件（restore_blocked/admin_blocked）不在此列：其物理清理由管理员
	// 在审核回收站中执行，用户不能通过"清空回收站"绕过。
	if _, err := tx.Exec(ctx, `
		WITH updated AS (
			UPDATE resources SET purged_at=NOW(), updated_at=NOW()
			WHERE owner_user_id=$1 AND trashed_at IS NOT NULL AND purged_at IS NULL
			  AND NOT restore_blocked AND NOT admin_blocked
			RETURNING blob_id
		),
		refs AS (
			SELECT blob_id, COUNT(*) AS refs FROM updated WHERE blob_id IS NOT NULL GROUP BY blob_id
		),
		released AS (
			UPDATE storage_blobs b SET
				ref_count = GREATEST(b.ref_count - refs.refs, 0),
				status = CASE WHEN b.ref_count - refs.refs <= 0 THEN 'orphaned' ELSE b.status END,
				updated_at = NOW()
			FROM refs WHERE b.id = refs.blob_id
			RETURNING 1
		)
		SELECT 1`, ownerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkTrashExpiredPurged 由后台任务调用：把超过保留期限的回收站内容标记为
// "彻底删除"（用户不可见、不可恢复），并释放对象引用（物理文件由孤儿清理任务
// 回收）。返回本次标记的资源数量。
func (r *Repo) MarkTrashExpiredPurged(ctx context.Context, before time.Time) (int64, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	if _, err := tx.Exec(ctx, `
		INSERT INTO file_moderation_history (
			resource_id, owner_user_id, file_name, size_bytes, mime_type, upload_task_id,
			event, status, reason, delete_file, blocked, actor_type, actor_name, details
		)
		SELECT r.id, r.owner_user_id, r.name, r.size_bytes, r.mime_type,
		       COALESCE(NULLIF(s.batch_id,''), s.id, r.id),
		       'purged', COALESCE(m.status,''), COALESCE(m.reason,''),
		       COALESCE(m.delete_file,FALSE), COALESCE(m.blocked,FALSE),
		       'system', '', jsonb_build_object('source', 'trash_expired')
		FROM resources r
		LEFT JOIN LATERAL (
			SELECT id, batch_id FROM upload_sessions
			WHERE resource_id = r.id ORDER BY created_at DESC LIMIT 1
		) s ON TRUE
		LEFT JOIN file_moderations m ON m.resource_id = r.id
		WHERE r.kind='file' AND r.trashed_at IS NOT NULL AND r.trashed_at < $1 AND r.purged_at IS NULL
		  AND NOT r.restore_blocked AND NOT r.admin_blocked`, before); err != nil {
		return 0, err
	}

	// 标记与释放引用放在同一语句（并发清空回收站不会重复扣减引用），让物理
	// 文件可被孤儿清理任务回收。
	var purgedCount int64
	if err := tx.QueryRow(ctx, `
		WITH updated AS (
			UPDATE resources SET purged_at=NOW(), updated_at=NOW()
			WHERE trashed_at IS NOT NULL AND trashed_at < $1 AND purged_at IS NULL
			  AND NOT restore_blocked AND NOT admin_blocked
			RETURNING blob_id
		),
		refs AS (
			SELECT blob_id, COUNT(*) AS refs FROM updated WHERE blob_id IS NOT NULL GROUP BY blob_id
		),
		released AS (
			UPDATE storage_blobs b SET
				ref_count = GREATEST(b.ref_count - refs.refs, 0),
				status = CASE WHEN b.ref_count - refs.refs <= 0 THEN 'orphaned' ELSE b.status END,
				updated_at = NOW()
			FROM refs WHERE b.id = refs.blob_id
			RETURNING 1
		)
		SELECT COUNT(*) FROM updated`, before).Scan(&purgedCount); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return purgedCount, nil
}

// TotalFileBytesByOwner 统计占用用户配额的字节数：回收站中的文件**仍占配额**
// （用户可恢复，空间并未归还）；清空回收站或保留期到期（写入 purged_at）后
// 释放，同时释放对象引用、由孤儿清理任务回收物理文件。
func (r *Repo) TotalFileBytesByOwner(ctx context.Context, ownerID int64) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(size_bytes), 0)::BIGINT
		FROM resources WHERE owner_user_id = $1 AND kind = 'file' AND purged_at IS NULL`, ownerID).Scan(&total)
	return total, err
}

// UploadUsageSince 统计自 since（含）起的上传次数与字节数（每日额度口径）。
// 数据来自 upload_usage_events：每次上传完成（含覆盖上传）追加一条事件，因此
// 覆盖上传同样计次计流量，且管理员物理清理资源不会让当日额度回落（防刷）。
// "自然日"口径由调用方传入本地时区的当天零点（见 startOfLocalDay）。
func (r *Repo) UploadUsageSince(ctx context.Context, ownerID int64, since time.Time) (int64, int64, error) {
	var count, bytes int64
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)::BIGINT
		FROM upload_usage_events
		WHERE owner_user_id = $1 AND created_at >= $2`, ownerID, since).Scan(&count, &bytes)
	return count, bytes, err
}

func (r *Repo) validateParent(ctx context.Context, ownerID int64, parentID *string) error {
	if parentID == nil || *parentID == "" {
		return nil
	}
	parent, err := r.GetByID(ctx, *parentID)
	if err != nil {
		return err
	}
	if parent.OwnerUserID != ownerID {
		return ErrOwnerMismatch
	}
	if parent.Kind != KindFolder {
		return ErrNotFolder
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanResource(row rowScanner) (Resource, error) {
	var item Resource
	if err := row.Scan(
		&item.ID, &item.OwnerUserID, &item.ParentID, &item.Kind, &item.Name,
		&item.SizeBytes, &item.SHA256Checksum, &item.MimeType, &item.BlobID,
		&item.TrashedAt, &item.RestoreBlocked, &item.AdminBlocked, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return Resource{}, err
	}
	return item, nil
}

func scanResourceWithReview(row rowScanner) (Resource, error) {
	var item Resource
	if err := row.Scan(
		&item.ID, &item.OwnerUserID, &item.ParentID, &item.Kind, &item.Name,
		&item.SizeBytes, &item.SHA256Checksum, &item.MimeType, &item.BlobID,
		&item.TrashedAt, &item.RestoreBlocked, &item.AdminBlocked, &item.CreatedAt, &item.UpdatedAt, &item.ReviewStatus, &item.ReviewReason,
	); err != nil {
		return Resource{}, err
	}
	return item, nil
}

// 说明：物理对象定位统一由 blobstore 的 object_ref 管理；本地文件时代的
// storage_key 列已在迁移 036 中删除。
