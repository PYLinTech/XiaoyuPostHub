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

-- name: GetLoginRetryAfter :one
-- 返回仍处于锁定状态的最晚解锁时间（未锁定时为纪元时间）。
--
-- 两个判定维度各自独立计算，取更晚者：
--   * 账号维度：同一账号 24 小时内的失败次数（防针对单个账号的密码爆破）；
--   * IP 维度：同一出口 IP 24 小时内失败所涉及的“不同账号数”（防密码喷洒）。
--     IP 维度要求至少涉及 3 个账号才计入锁定，避免共享出口（办公室 / 学校 /
--     移动网络）因为个别账号反复输错而导致整个出口无法登录。
WITH dimensions AS (
    SELECT COUNT(*)::BIGINT AS unit_count, MAX(failed_at) AS last_failed_at
    FROM login_failure_events events
    WHERE events.account_key = sqlc.arg(p_account_key)
      AND events.failed_at > now() - interval '24 hours'
    UNION ALL
    SELECT COUNT(DISTINCT account_key)::BIGINT AS unit_count, MAX(failed_at) AS last_failed_at
    FROM login_failure_events events
    WHERE events.client_ip = sqlc.arg(p_client_ip)
      AND events.failed_at > now() - interval '24 hours'
    HAVING COUNT(DISTINCT account_key) >= 3
), locks AS (
    SELECT last_failed_at + CASE
        WHEN unit_count > 10 THEN interval '30 minutes'
        WHEN unit_count >= 6 THEN interval '10 minutes'
        WHEN unit_count >= 3 THEN interval '5 minutes'
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
