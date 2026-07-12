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
SELECT COALESCE(MAX(locked_until), to_timestamp(0))::timestamptz
FROM login_failures
WHERE failure_key = ANY($1::text[]) AND locked_until > now();

-- name: RecordLoginFailure :one
INSERT INTO login_failures (failure_key, failure_count, locked_until, last_failed_at)
VALUES ($1, 1, NULL, now())
ON CONFLICT (failure_key) DO UPDATE SET
    failure_count = login_failures.failure_count + 1,
    locked_until = now() + CASE
        WHEN login_failures.failure_count + 1 > 10 THEN interval '30 minutes'
        WHEN login_failures.failure_count + 1 >= 6 THEN interval '10 minutes'
        WHEN login_failures.failure_count + 1 >= 3 THEN interval '5 minutes'
        ELSE interval '0 seconds'
    END,
    last_failed_at = now()
RETURNING locked_until;

-- name: ClearLoginFailure :exec
DELETE FROM login_failures WHERE failure_key = $1;

-- name: DeleteStaleLoginFailures :exec
DELETE FROM login_failures WHERE last_failed_at < now() - interval '24 hours';
