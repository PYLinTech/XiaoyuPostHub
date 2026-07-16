-- quota_profiles：配额配置
-- 配额方案只允许绑定到用户组，用户表不保存独立配额。
--
-- NULL 限额 = 不限；0 = 不允许；正数 = 限制值。
-- is_system=true 的 profile 不可删（业务层校验），但允许改数值。
CREATE TABLE IF NOT EXISTS quota_profiles (
    id                         BIGSERIAL   PRIMARY KEY,
    name                       TEXT        NOT NULL UNIQUE,
    description                TEXT,
    storage_bytes_limit         BIGINT,
    single_file_bytes_limit     BIGINT,
    daily_upload_bytes_limit    BIGINT,
    daily_upload_count_limit    BIGINT,
    active_share_count_limit    BIGINT,
    active_direct_link_limit    BIGINT,
    is_system                  BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT quota_profiles_name_format
        CHECK (name ~ '^[a-z][a-z0-9_]{1,31}$'),

    CONSTRAINT quota_profiles_non_negative
        CHECK (
            (storage_bytes_limit IS NULL OR storage_bytes_limit >= 0)
            AND (single_file_bytes_limit IS NULL OR single_file_bytes_limit >= 0)
            AND (daily_upload_bytes_limit IS NULL OR daily_upload_bytes_limit >= 0)
            AND (daily_upload_count_limit IS NULL OR daily_upload_count_limit >= 0)
            AND (active_share_count_limit IS NULL OR active_share_count_limit >= 0)
            AND (active_direct_link_limit IS NULL OR active_direct_link_limit >= 0)
        )
);
