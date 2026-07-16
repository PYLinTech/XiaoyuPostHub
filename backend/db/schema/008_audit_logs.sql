-- 管理操作审计。只记录程序内的管理动作，不存密码、会话 token 等敏感值。
CREATE TABLE IF NOT EXISTS audit_logs (
    id            BIGSERIAL   PRIMARY KEY,
    actor_user_id BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    actor_name    TEXT        NOT NULL,
    action        TEXT        NOT NULL,
    target_type   TEXT        NOT NULL,
    target_label  TEXT        NOT NULL DEFAULT '',
    details       JSONB       NOT NULL DEFAULT '{}'::JSONB,
    client_ip     INET,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS audit_logs_created_idx ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS audit_logs_actor_idx ON audit_logs(actor_user_id, created_at DESC);
