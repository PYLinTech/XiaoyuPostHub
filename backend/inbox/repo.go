// Package inbox 提供站内消息的投递、可见性、已读和删除状态管理。
package inbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ID      int64     `json:"id"`
	SentAt  time.Time `json:"sentAt"`
	Title   string    `json:"title"`
	Content string    `json:"content"`
	Tag     string    `json:"tag"`
	Read    bool      `json:"read"`
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) InsertUser(ctx context.Context, userID int64, title, content, tag string) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx, `INSERT INTO messages(receiver_type,receiver_id,title,content,tag)
		VALUES('user',$1,$2,$3,$4) RETURNING id`, userID, title, content, tag).Scan(&id)
	return id, err
}

func (r *Repo) List(ctx context.Context, userID int64, page, pageSize int) ([]Message, int64, int64, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 50 {
		pageSize = 10
	}
	offset := (page - 1) * pageSize
	rows, err := r.pool.Query(ctx, `
		SELECT m.id,m.sent_at,m.title,m.content,m.tag,
		       COALESCE(state.state IN ('read','deleted'), FALSE) AS is_read
		FROM messages m
		LEFT JOIN user_message_states state ON state.user_id=$1 AND state.message_id=m.id
		WHERE COALESCE(state.state,'') <> 'deleted'
		  AND (
		    m.receiver_type='all'
		    OR (m.receiver_type='user' AND m.receiver_id=$1)
		    OR (m.receiver_type='group' AND EXISTS(
		      SELECT 1 FROM user_group_memberships gm WHERE gm.user_id=$1 AND gm.group_id=m.receiver_id
		    ))
		  )
		ORDER BY m.id DESC LIMIT $2 OFFSET $3`, userID, pageSize, offset)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	items := make([]Message, 0)
	for rows.Next() {
		var item Message
		if err := rows.Scan(&item.ID, &item.SentAt, &item.Title, &item.Content, &item.Tag, &item.Read); err != nil {
			return nil, 0, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, err
	}
	rows.Close()
	var total, unread int64
	err = r.pool.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE state.message_id IS NULL)
		FROM messages m
		LEFT JOIN user_message_states state ON state.user_id=$1 AND state.message_id=m.id
		WHERE COALESCE(state.state,'') <> 'deleted'
		  AND (m.receiver_type='all'
		    OR (m.receiver_type='user' AND m.receiver_id=$1)
		    OR (m.receiver_type='group' AND EXISTS(
		      SELECT 1 FROM user_group_memberships gm WHERE gm.user_id=$1 AND gm.group_id=m.receiver_id)))`, userID).Scan(&total, &unread)
	return items, total, unread, err
}

func (r *Repo) MarkRead(ctx context.Context, userID int64, ids []int64) error {
	visible, err := r.visibleIDs(ctx, userID, ids)
	if err != nil || len(visible) == 0 {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO user_message_states(user_id,message_id,state)
		SELECT $1,id,'read' FROM unnest($2::BIGINT[]) AS id
		ON CONFLICT(user_id,message_id) DO UPDATE SET state=
		CASE WHEN user_message_states.state='deleted' THEN 'deleted' ELSE 'read' END`, userID, visible)
	return err
}

func (r *Repo) MarkAllRead(ctx context.Context, userID int64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_message_states(user_id,message_id,state)
		SELECT $1,m.id,'read'
		FROM messages m
		WHERE m.receiver_type='all'
		  OR (m.receiver_type='user' AND m.receiver_id=$1)
		  OR (m.receiver_type='group' AND EXISTS(
		    SELECT 1 FROM user_group_memberships gm WHERE gm.user_id=$1 AND gm.group_id=m.receiver_id))
		ON CONFLICT(user_id,message_id) DO UPDATE SET state=
		CASE WHEN user_message_states.state='deleted' THEN 'deleted' ELSE 'read' END`, userID)
	return err
}

func (r *Repo) Delete(ctx context.Context, userID int64, ids []int64) error {
	visible, err := r.visibleIDs(ctx, userID, ids)
	if err != nil || len(visible) == 0 {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO user_message_states(user_id,message_id,state)
		SELECT $1,id,'deleted' FROM unnest($2::BIGINT[]) AS id
		ON CONFLICT(user_id,message_id) DO UPDATE SET state='deleted'`, userID, visible)
	return err
}

func (r *Repo) DeleteAll(ctx context.Context, userID int64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO user_message_states(user_id,message_id,state)
		SELECT $1,m.id,'deleted'
		FROM messages m
		WHERE m.receiver_type='all'
		  OR (m.receiver_type='user' AND m.receiver_id=$1)
		  OR (m.receiver_type='group' AND EXISTS(
		    SELECT 1 FROM user_group_memberships gm WHERE gm.user_id=$1 AND gm.group_id=m.receiver_id))
		ON CONFLICT(user_id,message_id) DO UPDATE SET state='deleted'`, userID)
	return err
}

func (r *Repo) visibleIDs(ctx context.Context, userID int64, ids []int64) ([]int64, error) {
	if len(ids) == 0 || len(ids) > 200 {
		return nil, fmt.Errorf("消息编号数量无效")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT m.id FROM messages m
		WHERE m.id=ANY($2::BIGINT[]) AND (
		  m.receiver_type='all'
		  OR (m.receiver_type='user' AND m.receiver_id=$1)
		  OR (m.receiver_type='group' AND EXISTS(
		    SELECT 1 FROM user_group_memberships gm WHERE gm.user_id=$1 AND gm.group_id=m.receiver_id)))`, userID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InsertTx 在调用方事务内写入消息，适用于邀请码生成与消息投递原子提交。
func InsertTx(ctx context.Context, tx pgx.Tx, receiverType string, receiverID *int64, title, content, tag string) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO messages(receiver_type,receiver_id,title,content,tag)
		VALUES($1,$2,$3,$4,$5) RETURNING id`, receiverType, receiverID, title, content, tag).Scan(&id)
	return id, err
}
