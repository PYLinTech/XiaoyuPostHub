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

// DeleteCompletedBefore 回收"已完成"上传会话的残留元数据。
//
// 完成路径只清理磁盘分片目录并保留 DB 行（用于幂等重放：客户端重复 complete 能拿回
// 资源）。但会话行与分片行会随每次上传无界增长，因此按保留期定期回收：分片行由
// upload_chunks.session_id 的 ON DELETE CASCADE 一并删除。保留期内的行不受影响，
// 幂等重放与上传面板展示都有充足窗口。
func (r *Repo) DeleteCompletedBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM upload_sessions
		WHERE status = 'completed' AND updated_at < $1`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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

// CancelOwned 取消未完成的上传会话：同一事务内删除分片记录并置为 canceled。
//
// 分片记录必须随取消一起清除：此前只删磁盘目录、保留 upload_chunks 行，导致
// 取消后 resume 会按"已收到分片"跳过重传，complete 因文件缺失永远 409（用户
// 无法自救的死锁）。已完成/正在合并的会话不允许取消（避免把已落库文件"取消"
// 成僵尸会话）。
func (r *Repo) CancelOwned(ctx context.Context, ownerID int64, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM upload_sessions WHERE id=$1 AND owner_user_id=$2 FOR UPDATE`, id, ownerID).Scan(&status); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if status == "completing" || status == "completed" {
		return ErrInvalidState
	}
	if _, err := tx.Exec(ctx, `DELETE FROM upload_chunks WHERE session_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE upload_sessions SET status='canceled', error_message='', updated_at=NOW() WHERE id=$1 AND owner_user_id=$2`, id, ownerID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PendingUploadBytes 返回用户进行中上传会话的声明字节总数与会话数，用于在途
// 预扣：分片上传期间不产生资源、不占配额，若不预扣即可反复建会话写满临时盘。
// excludeSessionID 排除当前会话（合并校验时自身已计入）。
func (r *Repo) PendingUploadBytes(ctx context.Context, ownerID int64, excludeSessionID string) (int64, int, error) {
	var bytes int64
	var sessions int
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(total_size),0)::BIGINT, COUNT(*) FROM upload_sessions
		WHERE owner_user_id=$1 AND status IN ('queued','uploading','paused','completing')
		  AND ($2 = '' OR id <> $2)`, ownerID, excludeSessionID).Scan(&bytes, &sessions)
	return bytes, sessions, err
}

// SetUserActionStatus 用户操作（暂停/恢复）专用状态更新：带条件（CAS）拒绝覆盖
// "合并中/已完成"状态，防止读-写之间的竞态把正在合并的会话改回 queued/paused。
// 失败/合并等系统侧状态仍走 SetStatus（合并失败需要在 completing 上标记 failed）。
// AdminUploadItem 是管理端"在途上传任务"列表项。
type AdminUploadItem struct {
	ID           string    `json:"id"`
	OwnerUserID  int64     `json:"ownerUserId"`
	OwnerName    string    `json:"ownerName"`
	Filename     string    `json:"filename"`
	TotalSize    int64     `json:"totalSize"`
	TotalChunks  int32     `json:"totalChunks"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"errorMessage,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ListAdminUploads 列出进行中的上传会话（按最近更新排序）：这些会话不产生资源、
// 但占用临时盘，管理员据此清理用户放弃的任务（不必等待 7 天过期）。
func (r *Repo) ListAdminUploads(ctx context.Context, limit int) ([]AdminUploadItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.owner_user_id, u.username, s.filename, s.total_size, s.total_chunks,
		       s.status, s.error_message, s.created_at, s.updated_at
		FROM upload_sessions s
		JOIN users u ON u.id = s.owner_user_id
		WHERE s.status IN ('queued','uploading','paused','completing')
		ORDER BY s.updated_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AdminUploadItem, 0)
	for rows.Next() {
		var item AdminUploadItem
		if err := rows.Scan(&item.ID, &item.OwnerUserID, &item.OwnerName, &item.Filename,
			&item.TotalSize, &item.TotalChunks, &item.Status, &item.ErrorMessage,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AdminCancelUpload 管理端取消进行中的上传会话：置为 canceled 并删除分片记录
// （分片目录由调用方清理）。已完成/已结束的会话返回 ErrNotFound。
func (r *Repo) AdminCancelUpload(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE upload_sessions SET status='canceled', error_message='由管理员取消', updated_at=NOW()
		WHERE id=$1 AND status IN ('queued','uploading','paused','completing')`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := r.pool.Exec(ctx, `DELETE FROM upload_chunks WHERE session_id=$1`, id); err != nil {
		return err
	}
	return nil
}

func (r *Repo) SetUserActionStatus(ctx context.Context, ownerID int64, id, status string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE upload_sessions SET status=$3, error_message='', updated_at=NOW()
		WHERE id=$1 AND owner_user_id=$2 AND status NOT IN ('completing','completed')`, id, ownerID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	return nil
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
