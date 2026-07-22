ALTER TABLE system_settings ADD COLUMN IF NOT EXISTS login_totp_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT, ADD COLUMN IF NOT EXISTS totp_grace_used BOOLEAN NOT NULL DEFAULT FALSE;
CREATE TABLE IF NOT EXISTS login_totp_challenges (
    token_hash CHAR(64) PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS login_totp_challenges_expiry_idx ON login_totp_challenges(expires_at);
ALTER TABLE group_permissions DROP CONSTRAINT IF EXISTS group_permissions_code_valid;
ALTER TABLE group_permissions ADD CONSTRAINT group_permissions_code_valid CHECK (
    permission IN ('login','upload','download','preview','rename','delete_own','share','pickup_share','direct_link','use_login_totp','require_login_totp','view_admin_overview','manage_users','manage_user_groups','manage_permissions','manage_quotas','manage_invitations','review_files','review_shares','read_audit_log','manage_system')
);
