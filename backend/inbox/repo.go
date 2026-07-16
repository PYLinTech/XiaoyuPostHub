// Package inbox 提供站内消息的投递、可见性、已读和删除状态管理。
package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Message struct {
	ID           int64           `json:"id"`
	SentAt       time.Time       `json:"sentAt"`
	ReceiverType string          `json:"receiverType"`
	MessageType  string          `json:"messageType"`
	Content      json.RawMessage `json:"content"`
	Read         bool            `json:"read"`
}

type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

func (r *Repo) List(ctx context.Context, userID int64, limit int) ([]Message, int64, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT m.id,m.sent_at,m.receiver_type,m.message_type,m.content,
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
		ORDER BY m.id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Message, 0)
	for rows.Next() {
		var item Message
		if err := rows.Scan(&item.ID, &item.SentAt, &item.ReceiverType, &item.MessageType, &item.Content, &item.Read); err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var unread int64
	err = r.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM messages m
		LEFT JOIN user_message_states state ON state.user_id=$1 AND state.message_id=m.id
		WHERE state.message_id IS NULL
		  AND (m.receiver_type='all'
		    OR (m.receiver_type='user' AND m.receiver_id=$1)
		    OR (m.receiver_type='group' AND EXISTS(
		      SELECT 1 FROM user_group_memberships gm WHERE gm.user_id=$1 AND gm.group_id=m.receiver_id)))`, userID).Scan(&unread)
	return items, unread, err
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
func InsertTx(ctx context.Context, tx pgx.Tx, receiverType string, receiverID *int64, messageType string, content any) (int64, error) {
	body, err := json.Marshal(content)
	if err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO messages(receiver_type,receiver_id,message_type,content) VALUES($1,$2,$3,$4) RETURNING id`, receiverType, receiverID, messageType, body).Scan(&id)
	return id, err
}
