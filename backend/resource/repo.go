// Package resource 管理用户文件与文件夹元数据。
package resource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
)

type Resource struct {
	ID             string     `json:"id"`
	OwnerUserID    int64      `json:"-"`
	ParentID       *string    `json:"parentId,omitempty"`
	Kind           string     `json:"kind"`
	Name           string     `json:"name"`
	StorageKey     *string    `json:"-"`
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
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

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
		RETURNING id, owner_user_id, parent_id, kind, name, storage_key,
		          size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
		id, ownerID, parentID, name)
	return scanResource(row)
}

func (r *Repo) CreateFile(
	ctx context.Context,
	ownerID int64,
	parentID *string,
	name, storageKey string,
	sizeBytes int64,
	checksum, mimeType string,
) (Resource, error) {
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
		INSERT INTO resources (
			id, owner_user_id, parent_id, kind, name, storage_key,
			size_bytes, sha256_checksum, mime_type
		) VALUES ($1, $2, $3, 'file', $4, $5, $6, $7, NULLIF($8, ''))
		RETURNING id, owner_user_id, parent_id, kind, name, storage_key,
		          size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
		id, ownerID, parentID, name, storageKey, sizeBytes, checksum, mimeType)
	return scanResource(row)
}

// SaveUploadedFile 原子地写入上传资源及其审核状态。覆盖时返回待清理的旧存储键；
// 任一数据库操作失败都会回滚，避免资源元数据和审核状态不一致。
func (r *Repo) SaveUploadedFile(
	ctx context.Context, ownerID int64, parentID *string, name, storageKey string,
	sizeBytes int64, checksum, mimeType string, overwrite, requiresReview bool,
	uploadTaskID, uploadSessionID string,
) (Resource, *string, error) {
	name, err := ValidateName(name)
	if err != nil {
		return Resource{}, nil, err
	}
	if err := r.validateParent(ctx, ownerID, parentID); err != nil {
		return Resource{}, nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Resource{}, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	newID, err := randomtoken.New(18)
	if err != nil {
		return Resource{}, nil, err
	}
	var id, kind string
	var oldKey pgtype.Text
	existing := true
	err = tx.QueryRow(ctx, `
		SELECT id,kind,storage_key FROM resources
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
		  AND name=$3 AND trashed_at IS NULL
		FOR UPDATE`, ownerID, parentID, name).Scan(&id, &kind, &oldKey)
	if errors.Is(err, pgx.ErrNoRows) {
		id = newID
		existing = false
	} else if err != nil {
		return Resource{}, nil, err
	} else if !overwrite || kind != KindFile {
		return Resource{}, nil, ErrNameConflict
	}

	var row pgx.Row
	if existing {
		row = tx.QueryRow(ctx, `
			UPDATE resources SET storage_key=$4,size_bytes=$5,sha256_checksum=$6,
			       mime_type=NULLIF($7,''),updated_at=NOW(),restore_blocked=FALSE,admin_blocked=FALSE
			WHERE id=$1 AND owner_user_id=$2 AND name=$3
			RETURNING id, owner_user_id, parent_id, kind, name, storage_key,
			          size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
			id, ownerID, name, storageKey, sizeBytes, checksum, mimeType)
	} else {
		row = tx.QueryRow(ctx, `
			INSERT INTO resources (
				id, owner_user_id, parent_id, kind, name, storage_key,
				size_bytes, sha256_checksum, mime_type
			) VALUES ($1, $2, $3, 'file', $4, $5, $6, $7, NULLIF($8, ''))
			RETURNING id, owner_user_id, parent_id, kind, name, storage_key,
			          size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
			id, ownerID, parentID, name, storageKey, sizeBytes, checksum, mimeType)
	}
	item, err := scanResource(row)
	if err != nil {
		return Resource{}, nil, err
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
		return Resource{}, nil, err
	}
	result, err := tx.Exec(ctx, `
		UPDATE upload_sessions
		SET status='completed',resource_id=$3,error_message='',updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2
		  AND status IN ('queued','uploading','paused','completing')`,
		uploadSessionID, ownerID, item.ID)
	if err != nil {
		return Resource{}, nil, err
	}
	if result.RowsAffected() == 0 {
		return Resource{}, nil, fmt.Errorf("resource: 上传任务状态无效")
	}
	if err := tx.Commit(ctx); err != nil {
		return Resource{}, nil, err
	}
	if oldKey.Valid {
		value := oldKey.String
		return item, &value, nil
	}
	return item, nil, nil
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

func (r *Repo) AvailableChildName(ctx context.Context, ownerID int64, parentID *string, name string) (string, error) {
	name, err := ValidateName(name)
	if err != nil {
		return "", err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT name FROM resources
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
		  AND trashed_at IS NULL
		UNION
		SELECT filename FROM upload_sessions
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2
		  AND status IN ('queued','uploading','paused','completing')`,
		ownerID, parentID)
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
		SELECT id, owner_user_id, parent_id, kind, name, storage_key,
		       size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at
		FROM resources WHERE id = $1 AND trashed_at IS NULL`, id)
	res, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return res, err
}

func (r *Repo) GetByIDIncludingTrash(ctx context.Context, id string) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, parent_id, kind, name, storage_key,
		       size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at
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

// FindFileByChecksum 在全平台查找可复用的已落盘文件。
// 调用方只能使用其存储内容创建新的资源记录，不应暴露来源资源的用户和元数据。
func (r *Repo) FindFileByChecksum(ctx context.Context, checksum string, sizeBytes int64) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, parent_id, kind, name, storage_key,
		       size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at
		FROM resources
		WHERE kind='file' AND storage_key IS NOT NULL AND sha256_checksum=$1 AND size_bytes=$2
		ORDER BY created_at DESC LIMIT 1`, checksum, sizeBytes)
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
		RETURNING id, owner_user_id, parent_id, kind, name, storage_key,
		          size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`,
		id, ownerID, name)
	item, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return item, err
}

func (r *Repo) ListChildren(ctx context.Context, ownerID int64, parentID *string) ([]Resource, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.owner_user_id, r.parent_id, r.kind, r.name, r.storage_key,
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.trashed_at, r.restore_blocked, r.admin_blocked, r.created_at, r.updated_at,
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
		SELECT id, owner_user_id, parent_id, kind, name, storage_key,
		       size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at,
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
			&item.StorageKey, &item.SizeBytes, &item.SHA256Checksum, &item.MimeType,
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

// ListTrashOwned 只列出当前用户回收站中的顶层项目；文件夹后代随顶层项目一同恢复或删除。
func (r *Repo) ListTrashOwned(ctx context.Context, ownerID int64) ([]Resource, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.owner_user_id, r.parent_id, r.kind, r.name, r.storage_key,
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.trashed_at, r.restore_blocked, r.admin_blocked, r.created_at, r.updated_at
		FROM resources r
		LEFT JOIN resources parent ON parent.id=r.parent_id
		WHERE r.owner_user_id=$1 AND r.trashed_at IS NOT NULL
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
			WHERE child.owner_user_id=$2
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

func (r *Repo) RestoreOwned(ctx context.Context, ownerID int64, id string) error {
	tag, err := r.pool.Exec(ctx, `
		WITH RECURSIVE tree AS (
			SELECT root.id FROM resources root
			LEFT JOIN resources parent ON parent.id=root.parent_id
			WHERE root.id=$1 AND root.owner_user_id=$2 AND root.trashed_at IS NOT NULL AND NOT root.restore_blocked
			  AND (root.parent_id IS NULL OR parent.trashed_at IS NULL)
			UNION ALL
			SELECT child.id FROM resources child JOIN tree ON child.parent_id=tree.id
			WHERE child.owner_user_id=$2 AND child.trashed_at IS NOT NULL
		)
		UPDATE resources SET trashed_at=NULL, updated_at=NOW()
		WHERE id IN (SELECT id FROM tree)`, id, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAdminDisposition 应用审核处置；只有管理员审核造成的删除会在撤销时自动恢复。
func (r *Repo) SetAdminDisposition(ctx context.Context, id string, deleteFile, blocked bool) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE resources SET
			trashed_at=CASE
				WHEN $2 THEN COALESCE(trashed_at,NOW())
				WHEN restore_blocked THEN NULL
				ELSE trashed_at
			END,
			restore_blocked=$2,
			admin_blocked=$3,
			updated_at=NOW()
		WHERE id=$1 AND kind='file'
		RETURNING id, owner_user_id, parent_id, kind, name, storage_key,
		          size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at`, id, deleteFile, blocked)
	item, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return item, err
}

func (r *Repo) DeleteAdminTrashedFile(ctx context.Context, id string) (Resource, error) {
	item, err := r.GetByIDIncludingTrash(ctx, id)
	if err != nil || item.Kind != KindFile || item.TrashedAt == nil || !item.RestoreBlocked {
		return Resource{}, ErrNotFound
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM resources WHERE id=$1 AND kind='file' AND trashed_at IS NOT NULL AND restore_blocked`, id)
	if err != nil {
		return Resource{}, err
	}
	if tag.RowsAffected() == 0 {
		return Resource{}, ErrNotFound
	}
	return item, nil
}

// DeleteTrashedOwned 永久删除一个回收站项目并返回需要清理的物理文件。
func (r *Repo) DeleteTrashedOwned(ctx context.Context, ownerID int64, id string) ([]TreeEntry, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM resources root
			LEFT JOIN resources parent ON parent.id=root.parent_id
			WHERE root.id=$1 AND root.owner_user_id=$2 AND root.trashed_at IS NOT NULL
			  AND (root.parent_id IS NULL OR parent.trashed_at IS NULL)
		)`, id, ownerID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNotFound
	}
	rows, err := tx.Query(ctx, `
		WITH RECURSIVE tree AS (
			SELECT r.*, r.name::TEXT AS relative_path FROM resources r WHERE r.id=$1
			UNION ALL
			SELECT child.*, (tree.relative_path || '/' || child.name)::TEXT
			FROM resources child JOIN tree ON child.parent_id=tree.id
		)
		SELECT id, owner_user_id, parent_id, kind, name, storage_key,
		       size_bytes, sha256_checksum, mime_type, trashed_at, restore_blocked, admin_blocked, created_at, updated_at, relative_path
		FROM tree ORDER BY relative_path`, id)
	if err != nil {
		return nil, err
	}
	tree := make([]TreeEntry, 0)
	for rows.Next() {
		var item TreeEntry
		if err := rows.Scan(&item.ID, &item.OwnerUserID, &item.ParentID, &item.Kind, &item.Name,
			&item.StorageKey, &item.SizeBytes, &item.SHA256Checksum, &item.MimeType,
			&item.TrashedAt, &item.RestoreBlocked, &item.AdminBlocked, &item.CreatedAt, &item.UpdatedAt, &item.RelativePath); err != nil {
			rows.Close()
			return nil, err
		}
		tree = append(tree, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM resources WHERE id=$1 AND owner_user_id=$2 AND trashed_at IS NOT NULL`, id, ownerID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return tree, nil
}

func (r *Repo) EmptyTrashOwned(ctx context.Context, ownerID int64) ([]string, error) {
	return r.deleteTrashWhere(ctx, `owner_user_id=$1 AND trashed_at IS NOT NULL`, ownerID)
}

func (r *Repo) DeleteTrashExpiredBefore(ctx context.Context, before time.Time) ([]string, error) {
	return r.deleteTrashWhere(ctx, `trashed_at IS NOT NULL AND trashed_at < $1`, before)
}

func (r *Repo) deleteTrashWhere(ctx context.Context, condition string, argument any) ([]string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	rows, err := tx.Query(ctx, `SELECT storage_key FROM resources WHERE `+condition+` AND kind='file' FOR UPDATE`, argument)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM resources WHERE `+condition, argument); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}

// DeleteOwned 删除用户自己的一个文件或整棵文件夹，并返回删除前的资源树，
// 供文件存储层清理对应的物理文件。
func (r *Repo) DeleteOwned(ctx context.Context, ownerID int64, id string) ([]TreeEntry, error) {
	item, err := r.GetOwned(ctx, ownerID, id)
	if err != nil {
		return nil, err
	}
	var tree []TreeEntry
	if item.Kind == KindFolder {
		tree, err = r.ListTree(ctx, item.ID)
		if err != nil {
			return nil, err
		}
	} else {
		tree = []TreeEntry{{Resource: item, RelativePath: item.Name}}
	}
	result, err := r.pool.Exec(ctx, `DELETE FROM resources WHERE id=$1 AND owner_user_id=$2`, id, ownerID)
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return tree, nil
}

func (r *Repo) TotalFileBytesByOwner(ctx context.Context, ownerID int64) (int64, error) {
	var total int64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(size_bytes), 0)::BIGINT
		FROM resources WHERE owner_user_id = $1 AND kind = 'file'`, ownerID).Scan(&total)
	return total, err
}

func (r *Repo) UploadUsageSince(ctx context.Context, ownerID int64, since time.Time) (int64, int64, error) {
	var count, bytes int64
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size_bytes), 0)::BIGINT
		FROM resources
		WHERE owner_user_id = $1 AND kind = 'file' AND created_at >= $2`, ownerID, since).Scan(&count, &bytes)
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
		&item.StorageKey, &item.SizeBytes, &item.SHA256Checksum, &item.MimeType,
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
		&item.StorageKey, &item.SizeBytes, &item.SHA256Checksum, &item.MimeType,
		&item.TrashedAt, &item.RestoreBlocked, &item.AdminBlocked, &item.CreatedAt, &item.UpdatedAt, &item.ReviewStatus, &item.ReviewReason,
	); err != nil {
		return Resource{}, err
	}
	return item, nil
}

func StorageKey() (string, error) {
	token, err := randomtoken.New(24)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", token[:2], token), nil
}
