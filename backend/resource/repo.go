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
)

type Resource struct {
	ID             string    `json:"id"`
	OwnerUserID    int64     `json:"-"`
	ParentID       *string   `json:"parentId,omitempty"`
	Kind           string    `json:"kind"`
	Name           string    `json:"name"`
	StorageKey     *string   `json:"-"`
	SizeBytes      int64     `json:"sizeBytes"`
	SHA256Checksum *string   `json:"sha256,omitempty"`
	MimeType       *string   `json:"mimeType,omitempty"`
	ReviewStatus   string    `json:"reviewStatus,omitempty"`
	ReviewReason   string    `json:"reviewReason,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
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
		          size_bytes, sha256_checksum, mime_type, created_at, updated_at`,
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
		          size_bytes, sha256_checksum, mime_type, created_at, updated_at`,
		id, ownerID, parentID, name, storageKey, sizeBytes, checksum, mimeType)
	return scanResource(row)
}

func (r *Repo) GetByID(ctx context.Context, id string) (Resource, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, parent_id, kind, name, storage_key,
		       size_bytes, sha256_checksum, mime_type, created_at, updated_at
		FROM resources WHERE id = $1`, id)
	res, err := scanResource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Resource{}, ErrNotFound
	}
	return res, err
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

func (r *Repo) RenameOwned(ctx context.Context, ownerID int64, id, name string) (Resource, error) {
	name, err := ValidateName(name)
	if err != nil {
		return Resource{}, err
	}
	row := r.pool.QueryRow(ctx, `
		UPDATE resources SET name=$3, updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2
		RETURNING id, owner_user_id, parent_id, kind, name, storage_key,
		          size_bytes, sha256_checksum, mime_type, created_at, updated_at`,
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
		       r.size_bytes, r.sha256_checksum, r.mime_type, r.created_at, r.updated_at,
		       COALESCE(fr.status, 'approved'), COALESCE(fr.reason, '')
		FROM resources r LEFT JOIN file_reviews fr ON fr.resource_id=r.id
		WHERE r.owner_user_id = $1 AND r.parent_id IS NOT DISTINCT FROM $2
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
			FROM resources r WHERE r.id = $1
			UNION ALL
			SELECT child.*, (tree.relative_path || '/' || child.name)::TEXT
			FROM resources child
			JOIN tree ON child.parent_id = tree.id
		)
		SELECT id, owner_user_id, parent_id, kind, name, storage_key,
		       size_bytes, sha256_checksum, mime_type, created_at, updated_at,
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
			&item.CreatedAt, &item.UpdatedAt, &item.RelativePath,
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
		&item.CreatedAt, &item.UpdatedAt,
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
		&item.CreatedAt, &item.UpdatedAt, &item.ReviewStatus, &item.ReviewReason,
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
