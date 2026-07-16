-- user_sessions：登录会话表
--
-- 设计要点：
--   - 每个成功登录会产生一条 session 记录
--   - 浏览器 cookie 存原始 token（base64.RawURLEncoding 32 字节随机数）
--   - 数据库只存 token_hash = sha256(token)，不存明文 token
--   - expires_at 过期后 GetUserSessionByTokenHash 不再返回该记录
--   - user 删除时通过 ON DELETE CASCADE 自动清理其 session
--
-- 索引：
--   - token_hash UNIQUE：登录时按 cookie 哈希查找
--   - user_id：未来审计/管理界面"踢掉某用户全部 session"用
--   - expires_at：批量清理过期 session 用

CREATE TABLE IF NOT EXISTS user_sessions (
    id          BIGSERIAL    PRIMARY KEY,
    user_id     BIGINT       NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT         NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ  NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_user_sessions_user_id
    ON user_sessions(user_id);

CREATE INDEX IF NOT EXISTS idx_user_sessions_expires_at
    ON user_sessions(expires_at);

-- 一次失败只记录一行，同时保留账号和来源 IP 两个限流维度。旧结构按两个
-- failure_key 写两行高度重复数据，因此直接清除旧的瞬时限流状态后迁移。
DROP TABLE IF EXISTS login_failures;

CREATE TABLE IF NOT EXISTS login_failure_events (
    id          BIGSERIAL   PRIMARY KEY,
    account_key TEXT        NOT NULL,
    client_ip   TEXT        NOT NULL,
    failed_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT login_failure_events_account_not_blank CHECK (BTRIM(account_key) <> ''),
    CONSTRAINT login_failure_events_ip_not_blank CHECK (BTRIM(client_ip) <> '')
);

CREATE INDEX IF NOT EXISTS login_failure_events_account_idx
    ON login_failure_events(account_key, failed_at DESC);
CREATE INDEX IF NOT EXISTS login_failure_events_ip_idx
    ON login_failure_events(client_ip, failed_at DESC);
