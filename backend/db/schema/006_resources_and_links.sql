-- resources：用户文件与文件夹元数据。
-- 文件内容位于 system_settings.storage_path 下，数据库仅保存随机 storage_key。
CREATE TABLE IF NOT EXISTS resources (
    id              TEXT        PRIMARY KEY,
    owner_user_id   BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_id       TEXT        REFERENCES resources(id) ON DELETE CASCADE,
    kind            TEXT        NOT NULL,
    name            TEXT        NOT NULL,
    storage_key     TEXT        UNIQUE,
    size_bytes      BIGINT      NOT NULL DEFAULT 0,
    sha256_checksum CHAR(64),
    mime_type       TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT resources_kind_valid CHECK (kind IN ('file', 'folder')),
    CONSTRAINT resources_name_not_blank CHECK (BTRIM(name) <> ''),
    CONSTRAINT resources_size_non_negative CHECK (size_bytes >= 0),
    CONSTRAINT resources_file_shape CHECK (
        (kind = 'file' AND storage_key IS NOT NULL AND sha256_checksum ~ '^[0-9a-f]{64}$')
        OR
        (kind = 'folder' AND storage_key IS NULL AND sha256_checksum IS NULL AND size_bytes = 0)
    )
);

CREATE INDEX IF NOT EXISTS resources_owner_parent_idx
    ON resources(owner_user_id, parent_id, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS resources_sibling_name_unique
    ON resources(owner_user_id, COALESCE(parent_id, ''), name);

-- shares：带页面信息和可选密码的分享。
-- URL 使用 256-bit 随机 token，不包含 user id。token_value 同时用于公开查询和
-- 所有者复制，不再保存同一随机值的重复摘要。
CREATE TABLE IF NOT EXISTS shares (
    id                    BIGSERIAL   PRIMARY KEY,
    token_value           TEXT        NOT NULL UNIQUE,
    owner_user_id         BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    password_value        TEXT,
    expires_at            TIMESTAMPTZ,
    show_owner            BOOLEAN     NOT NULL DEFAULT FALSE,
    description           TEXT        NOT NULL DEFAULT '',
    description_format    TEXT        NOT NULL DEFAULT 'markdown',
    download_limit        BIGINT,
    traffic_limit_bytes   BIGINT,
    download_count        BIGINT      NOT NULL DEFAULT 0,
    traffic_used_bytes    BIGINT      NOT NULL DEFAULT 0,
    is_active             BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT shares_description_format_valid CHECK (description_format IN ('markdown', 'html')),
    CONSTRAINT shares_limits_non_negative CHECK (
        (download_limit IS NULL OR download_limit >= 0)
        AND (traffic_limit_bytes IS NULL OR traffic_limit_bytes >= 0)
        AND download_count >= 0
        AND traffic_used_bytes >= 0
    )
);

CREATE INDEX IF NOT EXISTS shares_owner_idx ON shares(owner_user_id, created_at DESC);

-- direct_links：无分享页面、无密码的随机文件直链。
CREATE TABLE IF NOT EXISTS direct_links (
    id                    BIGSERIAL   PRIMARY KEY,
    token_value           TEXT        NOT NULL UNIQUE,
    owner_user_id         BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    resource_id           TEXT        NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    expires_at            TIMESTAMPTZ,
    download_limit        BIGINT,
    traffic_limit_bytes   BIGINT,
    download_count        BIGINT      NOT NULL DEFAULT 0,
    traffic_used_bytes    BIGINT      NOT NULL DEFAULT 0,
    is_active             BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT direct_links_limits_non_negative CHECK (
        (download_limit IS NULL OR download_limit >= 0)
        AND (traffic_limit_bytes IS NULL OR traffic_limit_bytes >= 0)
        AND download_count >= 0
        AND traffic_used_bytes >= 0
    )
);

CREATE INDEX IF NOT EXISTS direct_links_owner_idx ON direct_links(owner_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS direct_links_resource_idx ON direct_links(resource_id);

ALTER TABLE shares DROP COLUMN IF EXISTS token_hash;
ALTER TABLE shares DROP COLUMN IF EXISTS password_hash;
ALTER TABLE direct_links DROP COLUMN IF EXISTS token_hash;
