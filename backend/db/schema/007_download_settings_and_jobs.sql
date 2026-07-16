-- 分享下载任务：下载次数在任务创建时只扣一次，随机 token 与分享和用户标识无关。
CREATE TABLE IF NOT EXISTS share_download_jobs (
    id                    BIGSERIAL   PRIMARY KEY,
    token_hash            CHAR(64)    NOT NULL UNIQUE,
    share_id              BIGINT      NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    pack_mode             TEXT        NOT NULL,
    delivery_mode         TEXT        NOT NULL,
    artifact_path         TEXT,
    artifact_name         TEXT,
    artifact_content_type TEXT,
    artifact_sha256      CHAR(64),
    artifact_temporary    BOOLEAN     NOT NULL DEFAULT FALSE,
    total_bytes           BIGINT      NOT NULL,
    expires_at            TIMESTAMPTZ NOT NULL,
    used_at               TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT share_download_jobs_pack_mode_valid CHECK (pack_mode IN ('frontend', 'backend')),
    CONSTRAINT share_download_jobs_delivery_mode_valid CHECK (delivery_mode IN ('blob', 'temporary_link')),
    CONSTRAINT share_download_jobs_bytes_non_negative CHECK (total_bytes >= 0),
    CONSTRAINT share_download_jobs_artifact_shape CHECK (
        (pack_mode = 'backend' AND artifact_path IS NOT NULL AND artifact_name IS NOT NULL AND artifact_content_type IS NOT NULL AND artifact_sha256 ~ '^[0-9a-f]{64}$')
        OR
        (pack_mode = 'frontend' AND artifact_path IS NULL AND artifact_name IS NULL AND artifact_content_type IS NULL AND artifact_sha256 IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS share_download_jobs_expiry_idx ON share_download_jobs(expires_at);

ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS root_resource_id;

-- 前端打包时每个文件拥有同一个任务下的一次性子地址；空文件夹只出现在任务清单中。
CREATE TABLE IF NOT EXISTS share_download_job_files (
    job_id          BIGINT      NOT NULL REFERENCES share_download_jobs(id) ON DELETE CASCADE,
    resource_id     TEXT        NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    relative_path   TEXT        NOT NULL,
    used_at         TIMESTAMPTZ,
    PRIMARY KEY (job_id, resource_id)
);
