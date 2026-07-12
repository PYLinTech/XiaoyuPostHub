// Package session 提供数据库 token 会话管理。
//
// 设计要点：
//   - 登录成功后生成 32 字节随机 token，base64.RawURLEncoding 后写入 cookie
//   - 数据库只存 token_hash = sha256(token)，明文 token 不入库
//   - 这里使用的 sha256 是"摘要/索引用途"，不是密码哈希（cookie 里的 32 字节随机值
//     本身就不可枚举，sha256 只是把它转成等长字符串并让数据库可以 UNIQUE 索引）
//   - 校验会话：按 token_hash 查表 + expires_at > now()，过期会话直接当作"未登录"
//   - **不**使用 JWT、**不**使用 HMAC 签名 token、**不**引入 SESSION_SECRET
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	sqlcgen "github.com/PYLinTech/XiaoyuPostHub/backend/db/generated"
)

// TTL 是 session 的有效期（7 天）。cookie MaxAge 和数据库 expires_at 都按此值算。
const TTL = 7 * 24 * time.Hour

// Repo 是 user_sessions 表的访问入口。
//
// 所有 token 生成 / 哈希计算 / 过期校验逻辑都集中在这里，
// 调用方（loginHandler / userInfoHandler）只关心"创建会话"和"按 token 拿 user_id"。
type Repo struct {
	db *pgxpool.Pool
}

// NewRepo 构造 Repo。
func NewRepo(db *pgxpool.Pool) *Repo {
	return &Repo{db: db}
}

// NewToken 生成新会话 token。
//
//   - raw：32 字节 crypto/rand 随机数（256 位熵）
//   - token：base64.RawURLEncoding（URL 安全、无 padding）后写入 cookie
//   - tokenHash：sha256(token) 十六进制，写入 user_sessions.token_hash
//
// 返回的 token 是**唯一**的明文凭证，调用方必须立刻写入 cookie；本函数不持久化任何东西。
func NewToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := HashToken(token)

	return token, tokenHash, nil
}

// HashToken 把 token 明文摘要成 hex(sha256(token))，写入数据库 / 用于查询。
//
// 注意：这里的 sha256 不是密码哈希算法——token 本身就是 256 bit 不可枚举随机数，
// sha256 只是把它压缩成 64 字符的 hex 字符串以便数据库存储。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create 给指定 user 创建一条会话记录，返回原始 token + 过期时间。
//
// 流程：
//  1. 生成新 token（明文 + 摘要）
//  2. 计算 expires_at = now() + TTL
//  3. INSERT user_sessions(token_hash=摘要, expires_at=过期时间)
//
// 返回的 token 由调用方负责写入 cookie。摘要形式进数据库，**永不**入库明文。
func (r *Repo) Create(ctx context.Context, userID int64) (string, time.Time, error) {
	token, tokenHash, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(TTL)
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("begin session transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := sqlcgen.New(r.db).WithTx(tx)
	_, err = q.CreateUserSession(ctx, sqlcgen.CreateUserSessionParams{
		UserID:    userID,
		TokenHash: tokenHash,
		ExpiresAt: sqlcTimestamptz(expiresAt),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create user session: %w", err)
	}
	if err := q.TrimUserSessions(ctx, userID); err != nil {
		return "", time.Time{}, fmt.Errorf("trim user sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, fmt.Errorf("commit session transaction: %w", err)
	}

	return token, expiresAt, nil
}

func (r *Repo) DeleteAllByUserID(ctx context.Context, userID int64) error {
	return sqlcgen.New(r.db).DeleteUserSessionsByUserID(ctx, userID)
}

// StartCleanup 每小时清除过期会话和 24 小时未活动的登录失败记录。
func (r *Repo) StartCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			q := sqlcgen.New(r.db)
			if err := q.DeleteExpiredUserSessions(ctx); err != nil {
				log.Printf("清理过期会话失败：%v", err)
			}
			if err := q.DeleteStaleLoginFailures(ctx); err != nil {
				log.Printf("清理登录失败记录失败：%v", err)
			}
		}
	}
}

func (r *Repo) RetryAfter(ctx context.Context, keys ...string) (time.Duration, error) {
	until, err := sqlcgen.New(r.db).GetLoginRetryAfter(ctx, keys)
	if err != nil {
		return 0, err
	}
	if d := time.Until(until.Time); d > 0 {
		return d, nil
	}
	return 0, nil
}

func (r *Repo) RecordFailure(ctx context.Context, keys ...string) (time.Duration, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	q := sqlcgen.New(r.db).WithTx(tx)
	var max time.Duration
	for _, key := range keys {
		until, err := q.RecordLoginFailure(ctx, key)
		if err != nil {
			return 0, err
		}
		d := time.Until(until.Time)
		if d > max {
			max = d
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return max, nil
}

func (r *Repo) ClearFailures(ctx context.Context, key string) error {
	return sqlcgen.New(r.db).ClearLoginFailure(ctx, key)
}

// GetUserIDByToken 按 token 明文查 user_id。
//
//   - 内部算 tokenHash → 查 GetUserSessionByTokenHash
//   - sqlc query 已带 expires_at > now() 过滤，过期 / 不存在的 token 一律返回 pgx.ErrNoRows
func (r *Repo) GetUserIDByToken(ctx context.Context, token string) (int64, error) {
	tokenHash := HashToken(token)

	q := sqlcgen.New(r.db)
	s, err := q.GetUserSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return 0, err
	}

	return s.UserID, nil
}

// DeleteByToken 删一条会话（登出 / 管理员踢人）。
//
// 找不到对应记录也按 no-op 处理，不返回错误（不影响"已登出"的业务结果）。
func (r *Repo) DeleteByToken(ctx context.Context, token string) error {
	tokenHash := HashToken(token)

	q := sqlcgen.New(r.db)
	return q.DeleteUserSessionByTokenHash(ctx, tokenHash)
}

// DeleteExpired 批量清理过期会话。建议由定时任务调用，登录路径不依赖它。
func (r *Repo) DeleteExpired(ctx context.Context) error {
	q := sqlcgen.New(r.db)
	return q.DeleteExpiredUserSessions(ctx)
}

// sqlcTimestamptz 把 time.Time 包成 pgtype.Timestamptz。
//
// 复用项目其他模块的写法（pgx v5 + pgx/v5/pgtype），统一 UTC。
func sqlcTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}
