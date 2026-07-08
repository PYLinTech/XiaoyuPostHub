-- users 表
--
-- quota_profile_id：用户专属配额配置。
--   - 不为空：使用该用户的配额（最高优先级）
--   - 为空：使用用户所属用户组中 priority 最高的 quota_profile
--   - 都没有：使用 name='default_user' 的默认 quota profile
--
-- FK 内联在 CREATE TABLE 里：quota_profiles 已在 000_quota_profiles.sql 建好，
-- 这里直接 REFERENCES 即可，不需要后续 ALTER TABLE，schema 真正幂等。
CREATE TABLE IF NOT EXISTS users (
    id                BIGSERIAL    PRIMARY KEY,
    username          VARCHAR(64)  NOT NULL UNIQUE,
    password_hash     TEXT         NOT NULL,
    quota_profile_id  BIGINT       REFERENCES quota_profiles(id) ON DELETE SET NULL,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
