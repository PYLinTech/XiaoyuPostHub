-- system_settings：程序自身的全部非敏感运行期配置。
-- 配置具有相同生命周期、权限和保存入口，因此只保留一个单例表。
CREATE TABLE IF NOT EXISTS system_settings (
    id                                  SMALLINT    PRIMARY KEY DEFAULT 1,
    site_name                           TEXT        NOT NULL DEFAULT 'XiaoyuPostHub',
    storage_path                        TEXT        NOT NULL DEFAULT '/data/uploads',
    folder_pack_mode                    TEXT        NOT NULL DEFAULT 'backend',
    share_delivery_mode                 TEXT        NOT NULL DEFAULT 'blob',
    registration_requires_invitation    BOOLEAN     NOT NULL DEFAULT FALSE,
    invitation_length                   SMALLINT    NOT NULL DEFAULT 8,
    invitation_case_sensitive           BOOLEAN     NOT NULL DEFAULT FALSE,
    invitation_include_letters          BOOLEAN     NOT NULL DEFAULT TRUE,
    invitation_include_numbers          BOOLEAN     NOT NULL DEFAULT TRUE,
    share_length                        SMALLINT    NOT NULL DEFAULT 6,
    share_case_sensitive                BOOLEAN     NOT NULL DEFAULT FALSE,
    share_include_letters               BOOLEAN     NOT NULL DEFAULT TRUE,
    share_include_numbers               BOOLEAN     NOT NULL DEFAULT TRUE,
    upload_requires_review              BOOLEAN     NOT NULL DEFAULT FALSE,
    custom_share_requires_review        BOOLEAN     NOT NULL DEFAULT FALSE,
    upload_chunk_size_bytes             INTEGER     NOT NULL DEFAULT 8388608,
    upload_task_chunk_concurrency       SMALLINT    NOT NULL DEFAULT 3,
    upload_user_task_concurrency        SMALLINT    NOT NULL DEFAULT 2,
    trash_retention_days                SMALLINT    NOT NULL DEFAULT 30,
    created_at                          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                          TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT system_settings_singleton CHECK (id = 1),
    CONSTRAINT system_settings_site_name_not_blank CHECK (BTRIM(site_name) <> ''),
    CONSTRAINT system_settings_storage_path_absolute CHECK (storage_path ~ '^/'),
    CONSTRAINT system_settings_pack_mode_valid CHECK (folder_pack_mode IN ('frontend', 'backend')),
    CONSTRAINT system_settings_delivery_mode_valid CHECK (share_delivery_mode IN ('blob', 'temporary_link')),
    CONSTRAINT system_settings_invitation_length CHECK (invitation_length BETWEEN 4 AND 64),
    CONSTRAINT system_settings_share_length CHECK (share_length BETWEEN 4 AND 64),
    CONSTRAINT system_settings_invitation_charset CHECK (invitation_include_letters OR invitation_include_numbers),
    CONSTRAINT system_settings_share_charset CHECK (share_include_letters OR share_include_numbers),
    CONSTRAINT system_settings_upload_chunk_size CHECK (upload_chunk_size_bytes BETWEEN 1048576 AND 67108864),
    CONSTRAINT system_settings_upload_task_chunk_concurrency CHECK (upload_task_chunk_concurrency BETWEEN 1 AND 8),
    CONSTRAINT system_settings_upload_user_task_concurrency CHECK (upload_user_task_concurrency BETWEEN 1 AND 8),
    CONSTRAINT system_settings_trash_retention_days CHECK (trash_retention_days BETWEEN 1 AND 3650)
);

-- 兼容已有数据库：先补列，再从旧单例表迁移值。旧表都是运行配置，不含业务记录。
ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS folder_pack_mode TEXT NOT NULL DEFAULT 'backend',
    ADD COLUMN IF NOT EXISTS share_delivery_mode TEXT NOT NULL DEFAULT 'blob',
    ADD COLUMN IF NOT EXISTS registration_requires_invitation BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS invitation_length SMALLINT NOT NULL DEFAULT 8,
    ADD COLUMN IF NOT EXISTS invitation_case_sensitive BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS invitation_include_letters BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS invitation_include_numbers BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS share_length SMALLINT NOT NULL DEFAULT 6,
    ADD COLUMN IF NOT EXISTS share_case_sensitive BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS share_include_letters BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS share_include_numbers BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS upload_requires_review BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS custom_share_requires_review BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS upload_chunk_size_bytes INTEGER NOT NULL DEFAULT 8388608,
    ADD COLUMN IF NOT EXISTS upload_task_chunk_concurrency SMALLINT NOT NULL DEFAULT 3,
    ADD COLUMN IF NOT EXISTS upload_user_task_concurrency SMALLINT NOT NULL DEFAULT 2,
    ADD COLUMN IF NOT EXISTS trash_retention_days SMALLINT NOT NULL DEFAULT 30;

-- CREATE TABLE IF NOT EXISTS 不会为旧表补约束；统一重建约束，确保升级后的数据库
-- 与全新安装具有相同的数据边界。
ALTER TABLE system_settings
    DROP CONSTRAINT IF EXISTS system_settings_singleton,
    DROP CONSTRAINT IF EXISTS system_settings_site_name_not_blank,
    DROP CONSTRAINT IF EXISTS system_settings_storage_path_absolute,
    DROP CONSTRAINT IF EXISTS system_settings_pack_mode_valid,
    DROP CONSTRAINT IF EXISTS system_settings_delivery_mode_valid,
    DROP CONSTRAINT IF EXISTS system_settings_invitation_length,
    DROP CONSTRAINT IF EXISTS system_settings_share_length,
    DROP CONSTRAINT IF EXISTS system_settings_invitation_charset,
    DROP CONSTRAINT IF EXISTS system_settings_share_charset,
    DROP CONSTRAINT IF EXISTS system_settings_upload_chunk_size,
    DROP CONSTRAINT IF EXISTS system_settings_upload_task_chunk_concurrency,
    DROP CONSTRAINT IF EXISTS system_settings_upload_user_task_concurrency,
    DROP CONSTRAINT IF EXISTS system_settings_trash_retention_days;

ALTER TABLE system_settings
    ADD CONSTRAINT system_settings_singleton CHECK (id = 1),
    ADD CONSTRAINT system_settings_site_name_not_blank CHECK (BTRIM(site_name) <> ''),
    ADD CONSTRAINT system_settings_storage_path_absolute CHECK (storage_path ~ '^/'),
    ADD CONSTRAINT system_settings_pack_mode_valid CHECK (folder_pack_mode IN ('frontend', 'backend')),
    ADD CONSTRAINT system_settings_delivery_mode_valid CHECK (share_delivery_mode IN ('blob', 'temporary_link')),
    ADD CONSTRAINT system_settings_invitation_length CHECK (invitation_length BETWEEN 4 AND 64),
    ADD CONSTRAINT system_settings_share_length CHECK (share_length BETWEEN 4 AND 64),
    ADD CONSTRAINT system_settings_invitation_charset CHECK (invitation_include_letters OR invitation_include_numbers),
    ADD CONSTRAINT system_settings_share_charset CHECK (share_include_letters OR share_include_numbers),
    ADD CONSTRAINT system_settings_upload_chunk_size CHECK (upload_chunk_size_bytes BETWEEN 1048576 AND 67108864),
    ADD CONSTRAINT system_settings_upload_task_chunk_concurrency CHECK (upload_task_chunk_concurrency BETWEEN 1 AND 8),
    ADD CONSTRAINT system_settings_upload_user_task_concurrency CHECK (upload_user_task_concurrency BETWEEN 1 AND 8),
    ADD CONSTRAINT system_settings_trash_retention_days CHECK (trash_retention_days BETWEEN 1 AND 3650);

INSERT INTO system_settings (id, site_name, storage_path)
VALUES (1, 'XiaoyuPostHub', '/data/uploads')
ON CONFLICT (id) DO NOTHING;

DO $$
BEGIN
    IF to_regclass('download_settings') IS NOT NULL THEN
        UPDATE system_settings target SET
            folder_pack_mode = source.folder_pack_mode,
            share_delivery_mode = source.share_delivery_mode
        FROM download_settings source WHERE target.id=1 AND source.id=1;
    END IF;
    IF to_regclass('registration_settings') IS NOT NULL THEN
        UPDATE system_settings target SET
            registration_requires_invitation = source.registration_requires_code
        FROM registration_settings source WHERE target.id=1 AND source.id=1;
    END IF;
    IF to_regclass('random_code_settings') IS NOT NULL THEN
        UPDATE system_settings target SET
            invitation_length = source.invitation_length,
            invitation_case_sensitive = source.invitation_case_sensitive,
            invitation_include_letters = source.invitation_include_letters,
            invitation_include_numbers = source.invitation_include_numbers,
            share_length = source.share_length,
            share_case_sensitive = source.share_case_sensitive,
            share_include_letters = source.share_include_letters,
            share_include_numbers = source.share_include_numbers
        FROM random_code_settings source WHERE target.id=1 AND source.id=1;
    END IF;
    IF to_regclass('review_settings') IS NOT NULL THEN
        UPDATE system_settings target SET
            upload_requires_review = source.upload_requires_review,
            custom_share_requires_review = source.custom_share_requires_review
        FROM review_settings source WHERE target.id=1 AND source.id=1;
    END IF;
END $$;

DROP TABLE IF EXISTS download_settings;
DROP TABLE IF EXISTS registration_settings;
DROP TABLE IF EXISTS random_code_settings;
DROP TABLE IF EXISTS review_settings;
