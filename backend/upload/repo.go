// Package upload 持久化按用户隔离的分片上传队列。
package upload

import (
	"context"
	"errors"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/randomtoken"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("upload: 上传任务不存在")
	ErrInvalidState = errors.New("upload: 上传任务状态无效")
)

type Session struct {
	ID             string    `json:"id"`
	OwnerUserID    int64     `json:"-"`
	BatchID        string    `json:"batchId"`
	ParentID       *string   `json:"parentId,omitempty"`
	Filename       string    `json:"filename"`
	TotalSize      int64     `json:"totalSize"`
	ChunkSize      int32     `json:"chunkSize"`
	TotalChunks    int32     `json:"totalChunks"`
	MimeType       string    `json:"mimeType"`
	ExpectedSHA256 string    `json:"sha256"`
	Status         string    `json:"status"`
	ResourceID     *string   `json:"resourceId,omitempty"`
	ErrorMessage   string    `json:"errorMessage,omitempty"`
	ConflictAction string    `json:"conflictAction,omitempty"`
	QueuePosition  int64     `json:"queuePosition"`
	ReceivedChunks []int32   `json:"receivedChunks"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	ExpiresAt      time.Time `json:"expiresAt"`
}

type Repo struct{ pool *pgxpool.Pool }

type Chunk struct {
	Index        int32
	SizeBytes    int32
	Checksum     string
	RelativePath string
}

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) CreateOrResume(ctx context.Context, ownerID int64, batchID string, parentID *string, filename string, totalSize int64, chunkSize, totalChunks int32, mimeType, checksum, conflictAction string) (Session, bool, error) {
	existing, err := r.findActive(ctx, ownerID, parentID, filename, checksum)
	if err == nil {
		if existing.TotalSize != totalSize {
			return Session{}, false, ErrInvalidState
		}
		if existing.ConflictAction != conflictAction {
			if _, err := r.pool.Exec(ctx, `
				UPDATE upload_sessions
				SET conflict_action=$3,updated_at=NOW()
				WHERE id=$1 AND owner_user_id=$2
				  AND status IN ('queued','uploading','paused','completing')`,
				existing.ID, ownerID, conflictAction); err != nil {
				return Session{}, false, err
			}
			existing.ConflictAction = conflictAction
		}
		return existing, true, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Session{}, false, err
	}
	id, err := randomtoken.New(24)
	if err != nil {
		return Session{}, false, err
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO upload_sessions (
			id, owner_user_id, batch_id, parent_id, filename, total_size, chunk_size,
			total_chunks, mime_type, expected_sha256, conflict_action, queue_position
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
			(SELECT COALESCE(MAX(queue_position), 0) + 1024 FROM upload_sessions WHERE owner_user_id=$2))
		RETURNING id, owner_user_id, batch_id, parent_id, filename, total_size, chunk_size,
		          total_chunks, mime_type, expected_sha256, status, resource_id,
		          error_message, conflict_action, queue_position, created_at, updated_at, expires_at`,
		id, ownerID, batchID, parentID, filename, totalSize, chunkSize, totalChunks, mimeType, checksum, conflictAction)
	session, err := scanSession(row)
	if err != nil {
		return Session{}, false, err
	}
	return session, false, nil
}

// DeleteExpired 删除数据库中过期队列记录，并返回待清理的分片目录编号。
func (r *Repo) DeleteExpired(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		DELETE FROM upload_sessions
		WHERE expires_at < NOW() AND status <> 'completed'
		RETURNING id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repo) findActive(ctx context.Context, ownerID int64, parentID *string, filename, checksum string) (Session, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, batch_id, parent_id, filename, total_size, chunk_size,
		       total_chunks, mime_type, expected_sha256, status, resource_id,
		       error_message, conflict_action, queue_position, created_at, updated_at, expires_at
		FROM upload_sessions
		WHERE owner_user_id=$1 AND parent_id IS NOT DISTINCT FROM $2 AND filename=$3
		  AND expected_sha256=$4 AND status IN ('queued','uploading','paused','completing')
		ORDER BY created_at DESC LIMIT 1`, ownerID, parentID, filename, checksum)
	session, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	session.ReceivedChunks, err = r.ListChunkIndexes(ctx, session.ID)
	return session, err
}

func (r *Repo) GetOwned(ctx context.Context, ownerID int64, id string) (Session, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, batch_id, parent_id, filename, total_size, chunk_size,
		       total_chunks, mime_type, expected_sha256, status, resource_id,
		       error_message, conflict_action, queue_position, created_at, updated_at, expires_at
		FROM upload_sessions WHERE id=$1 AND owner_user_id=$2`, id, ownerID)
	session, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	session.ReceivedChunks, err = r.ListChunkIndexes(ctx, session.ID)
	return session, err
}

func (r *Repo) ListOwned(ctx context.Context, ownerID int64) ([]Session, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, owner_user_id, batch_id, parent_id, filename, total_size, chunk_size,
		       total_chunks, mime_type, expected_sha256, status, resource_id,
		       error_message, conflict_action, queue_position, created_at, updated_at, expires_at
		FROM upload_sessions
		WHERE owner_user_id=$1 AND status NOT IN ('completed','canceled')
		ORDER BY queue_position, created_at LIMIT 100`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Session, 0)
	for rows.Next() {
		item, scanErr := scanSession(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		item.ReceivedChunks, scanErr = r.ListChunkIndexes(ctx, item.ID)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repo) ListIDsOwned(ctx context.Context, ownerID int64) ([]string, error) {
	rows, err := r.pool.Query(ctx, `SELECT id FROM upload_sessions WHERE owner_user_id=$1`, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repo) ListChunkIndexes(ctx context.Context, sessionID string) ([]int32, error) {
	rows, err := r.pool.Query(ctx, `SELECT chunk_index FROM upload_chunks WHERE session_id=$1 ORDER BY chunk_index`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexes := make([]int32, 0)
	for rows.Next() {
		var index int32
		if err := rows.Scan(&index); err != nil {
			return nil, err
		}
		indexes = append(indexes, index)
	}
	return indexes, rows.Err()
}

func (r *Repo) ListChunks(ctx context.Context, ownerID int64, sessionID string) ([]Chunk, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT c.chunk_index, c.size_bytes, c.sha256_checksum, c.relative_path
		FROM upload_chunks c JOIN upload_sessions s ON s.id=c.session_id
		WHERE c.session_id=$1 AND s.owner_user_id=$2 ORDER BY c.chunk_index`, sessionID, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chunks := make([]Chunk, 0)
	for rows.Next() {
		var chunk Chunk
		if err := rows.Scan(&chunk.Index, &chunk.SizeBytes, &chunk.Checksum, &chunk.RelativePath); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func (r *Repo) RecordChunk(ctx context.Context, ownerID int64, sessionID string, index, size int32, checksum, relativePath string) error {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO upload_chunks (session_id, chunk_index, size_bytes, sha256_checksum, relative_path)
		SELECT id, $3, $4, $5, $6 FROM upload_sessions
		WHERE id=$1 AND owner_user_id=$2 AND status IN ('queued','uploading') AND $3 < total_chunks
		ON CONFLICT (session_id, chunk_index) DO UPDATE SET
			size_bytes=EXCLUDED.size_bytes, sha256_checksum=EXCLUDED.sha256_checksum,
			relative_path=EXCLUDED.relative_path, created_at=NOW()`,
		sessionID, ownerID, index, size, checksum, relativePath)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	_, err = r.pool.Exec(ctx, `UPDATE upload_sessions SET status='uploading', error_message='', updated_at=NOW(), expires_at=NOW()+INTERVAL '7 days' WHERE id=$1 AND owner_user_id=$2`, sessionID, ownerID)
	return err
}

func (r *Repo) SetStatus(ctx context.Context, ownerID int64, id, status, message string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE upload_sessions SET status=$3, error_message=$4, updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2`, id, ownerID, status, message)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) MoveOwned(ctx context.Context, ownerID int64, id string, offset int) error {
	if offset != -1 && offset != 1 {
		return ErrInvalidState
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	rows, err := tx.Query(ctx, `
		SELECT id FROM upload_sessions
		WHERE owner_user_id=$1 AND status NOT IN ('completed','canceled')
		ORDER BY queue_position, created_at, id FOR UPDATE`, ownerID)
	if err != nil {
		return err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, itemID)
	}
	rows.Close()
	index := -1
	for current, itemID := range ids {
		if itemID == id {
			index = current
			break
		}
	}
	target := index + offset
	if index < 0 {
		return ErrNotFound
	}
	if target < 0 || target >= len(ids) {
		return tx.Commit(ctx)
	}
	ids[index], ids[target] = ids[target], ids[index]
	for position, itemID := range ids {
		if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET queue_position=$3, updated_at=NOW() WHERE id=$1 AND owner_user_id=$2`, itemID, ownerID, int64(position+1)*1024); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ClaimCompleting 保证同一任务只有一个合并者。
func (r *Repo) ClaimCompleting(ctx context.Context, ownerID int64, id string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE upload_sessions SET status='completing', error_message='', updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2
		  AND status IN ('queued','uploading','paused','failed')`, id, ownerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
}

// RecoverInterrupted 在服务启动时把上次进程中断的合并任务转为可重试状态。
func (r *Repo) RecoverInterrupted(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE upload_sessions
		SET status='failed', error_message='服务中断，请继续上传', updated_at=NOW()
		WHERE status='completing'`)
	return err
}

func (r *Repo) MarkCompleted(ctx context.Context, ownerID int64, id, resourceID string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE upload_sessions SET status='completed', resource_id=$3, error_message='', updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2`, id, ownerID, resourceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type scanner interface{ Scan(...any) error }

func scanSession(row scanner) (Session, error) {
	var item Session
	var parentID, resourceID pgtype.Text
	var createdAt, updatedAt, expiresAt pgtype.Timestamptz
	err := row.Scan(&item.ID, &item.OwnerUserID, &item.BatchID, &parentID, &item.Filename, &item.TotalSize,
		&item.ChunkSize, &item.TotalChunks, &item.MimeType, &item.ExpectedSHA256,
		&item.Status, &resourceID, &item.ErrorMessage, &item.ConflictAction, &item.QueuePosition, &createdAt, &updatedAt, &expiresAt)
	if err != nil {
		return Session{}, err
	}
	if parentID.Valid {
		item.ParentID = &parentID.String
	}
	if resourceID.Valid {
		item.ResourceID = &resourceID.String
	}
	item.CreatedAt = createdAt.Time
	item.UpdatedAt = updatedAt.Time
	item.ExpiresAt = expiresAt.Time
	item.ReceivedChunks = make([]int32, 0)
	return item, nil
}
