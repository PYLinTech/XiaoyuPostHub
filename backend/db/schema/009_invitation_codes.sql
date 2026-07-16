-- 一次性邀请码；注册策略保存在 system_settings。
CREATE TABLE IF NOT EXISTS invitation_codes (
    id                  BIGSERIAL   PRIMARY KEY,
    code_hash           CHAR(64)    NOT NULL UNIQUE,
    code_prefix         VARCHAR(16) NOT NULL,
    issued_by_user_id   BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    issued_to_user_id   BIGINT      REFERENCES users(id) ON DELETE CASCADE,
    issued_to_group_id  BIGINT      REFERENCES user_groups(id) ON DELETE CASCADE,
    used_by_user_id     BIGINT      REFERENCES users(id) ON DELETE SET NULL,
    used_at             TIMESTAMPTZ,
    revoked_at          TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT invitation_codes_exactly_one_target CHECK (
        (issued_to_user_id IS NOT NULL AND issued_to_group_id IS NULL)
        OR (issued_to_user_id IS NULL AND issued_to_group_id IS NOT NULL)
    ),
    CONSTRAINT invitation_codes_use_shape CHECK (
        (used_at IS NULL AND used_by_user_id IS NULL)
        OR used_at IS NOT NULL
    )
);

CREATE INDEX IF NOT EXISTS invitation_codes_user_target_idx
    ON invitation_codes(issued_to_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS invitation_codes_group_target_idx
    ON invitation_codes(issued_to_group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS invitation_codes_status_idx
    ON invitation_codes(created_at DESC) WHERE used_at IS NULL AND revoked_at IS NULL;
