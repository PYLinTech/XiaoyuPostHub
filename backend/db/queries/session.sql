-- user_sessions：登录会话管理
--
-- 设计要点：
--   - 创建会话：生成 32 字节随机 token → base64.RawURLEncoding → 写 cookie；
--     这里只持久化 token_hash = sha256(token)，token 明文不出数据库
--   - 校验会话：按 token_hash 查 + 校验 expires_at > now()，
--     过期的会话当作"未登录"处理
--   - 删除会话：登出 / 删除 token 对应记录；批量清理过期会话独立 query

-- name: CreateUserSession :one
INSERT INTO user_sessions (
    user_id,
    token_hash,
    expires_at
) VALUES (
    $1,
    $2,
    $3
)
RETURNING id, user_id, token_hash, expires_at, created_at;

-- name: GetUserSessionByTokenHash :one
SELECT
    id,
    user_id,
    token_hash,
    expires_at,
    created_at
FROM user_sessions
WHERE token_hash = $1
  AND expires_at > now()
LIMIT 1;

-- name: DeleteExpiredUserSessions :exec
DELETE FROM user_sessions
WHERE expires_at <= now();

-- name: DeleteUserSessionByTokenHash :exec
DELETE FROM user_sessions
WHERE token_hash = $1;

-- name: DeleteUserSessionsByUserID :exec
DELETE FROM user_sessions WHERE user_id = $1;

-- name: TrimUserSessions :exec
DELETE FROM user_sessions AS target WHERE target.id IN (
    SELECT retained.id
    FROM user_sessions AS retained
    WHERE retained.user_id = $1
    ORDER BY retained.created_at DESC
    OFFSET 10
);

-- name: GetLoginRetryAfter :one
WITH dimensions AS (
    SELECT COUNT(*)::BIGINT AS failure_count, MAX(failed_at) AS last_failed_at
    FROM login_failure_events events
    WHERE events.account_key = sqlc.arg(p_account_key)
      AND events.failed_at > now() - interval '24 hours'
    UNION ALL
    SELECT COUNT(*)::BIGINT AS failure_count, MAX(failed_at) AS last_failed_at
    FROM login_failure_events events
    WHERE events.client_ip = sqlc.arg(p_client_ip)
      AND events.failed_at > now() - interval '24 hours'
), locks AS (
    SELECT last_failed_at + CASE
        WHEN failure_count > 10 THEN interval '30 minutes'
        WHEN failure_count >= 6 THEN interval '10 minutes'
        WHEN failure_count >= 3 THEN interval '5 minutes'
        ELSE interval '0 seconds'
    END AS locked_until
    FROM dimensions
    WHERE last_failed_at IS NOT NULL
)
SELECT COALESCE(MAX(locked_until), to_timestamp(0))::timestamptz FROM locks;

-- name: RecordLoginFailure :one
INSERT INTO login_failure_events (account_key, client_ip)
VALUES (sqlc.arg(p_account_key), sqlc.arg(p_client_ip))
RETURNING failed_at;

-- name: ClearAccountLoginFailures :exec
DELETE FROM login_failure_events WHERE account_key = sqlc.arg(p_account_key);

-- name: DeleteStaleLoginFailures :exec
DELETE FROM login_failure_events WHERE failed_at < now() - interval '24 hours';
