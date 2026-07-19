-- upload_sessions / upload_chunks：按用户隔离的可恢复上传队列。
-- 分片文件位于 storage_path/.tmp/upload-sessions 下，数据库保存权威进度。
CREATE TABLE IF NOT EXISTS upload_sessions (
    id              TEXT        PRIMARY KEY,
    owner_user_id   BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    batch_id        TEXT        NOT NULL DEFAULT '',
    parent_id       TEXT        REFERENCES resources(id) ON DELETE CASCADE,
    filename        TEXT        NOT NULL,
    total_size      BIGINT      NOT NULL,
    chunk_size      INTEGER     NOT NULL,
    total_chunks    INTEGER     NOT NULL,
    mime_type       TEXT        NOT NULL DEFAULT '',
    expected_sha256 CHAR(64)    NOT NULL,
    status          TEXT        NOT NULL DEFAULT 'queued',
    resource_id     TEXT        REFERENCES resources(id) ON DELETE SET NULL,
    error_message   TEXT        NOT NULL DEFAULT '',
    queue_position  BIGINT      NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at      TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '7 days',

    CONSTRAINT upload_sessions_name_not_blank CHECK (BTRIM(filename) <> ''),
    CONSTRAINT upload_sessions_size_valid CHECK (total_size >= 0),
    CONSTRAINT upload_sessions_chunk_size_valid CHECK (chunk_size BETWEEN 1048576 AND 67108864),
    CONSTRAINT upload_sessions_total_chunks_valid CHECK (total_chunks BETWEEN 1 AND 1048576),
    CONSTRAINT upload_sessions_sha_valid CHECK (expected_sha256 ~ '^[0-9a-f]{64}$'),
    CONSTRAINT upload_sessions_status_valid CHECK (status IN ('queued', 'uploading', 'paused', 'completing', 'completed', 'failed', 'canceled'))
);

CREATE INDEX IF NOT EXISTS upload_sessions_owner_updated_idx
    ON upload_sessions(owner_user_id, updated_at DESC);

ALTER TABLE upload_sessions
    ADD COLUMN IF NOT EXISTS queue_position BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS batch_id TEXT NOT NULL DEFAULT '';

UPDATE upload_sessions SET batch_id=id WHERE batch_id='';

WITH ordered AS (
    SELECT id, ROW_NUMBER() OVER (PARTITION BY owner_user_id ORDER BY queue_position, created_at, id) * 1024 AS position
    FROM upload_sessions
)
UPDATE upload_sessions target SET queue_position=ordered.position
FROM ordered WHERE target.id=ordered.id;

CREATE INDEX IF NOT EXISTS upload_sessions_owner_queue_idx
    ON upload_sessions(owner_user_id, queue_position, created_at);

CREATE UNIQUE INDEX IF NOT EXISTS upload_sessions_active_file_unique
    ON upload_sessions(owner_user_id, COALESCE(parent_id, ''), filename, expected_sha256)
    WHERE status IN ('queued', 'uploading', 'paused', 'completing');

CREATE TABLE IF NOT EXISTS upload_chunks (
    session_id      TEXT        NOT NULL REFERENCES upload_sessions(id) ON DELETE CASCADE,
    chunk_index     INTEGER     NOT NULL,
    size_bytes      INTEGER     NOT NULL,
    sha256_checksum CHAR(64)    NOT NULL,
    relative_path   TEXT        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (session_id, chunk_index),

    CONSTRAINT upload_chunks_index_valid CHECK (chunk_index >= 0),
    CONSTRAINT upload_chunks_size_valid CHECK (size_bytes >= 0),
    CONSTRAINT upload_chunks_sha_valid CHECK (sha256_checksum ~ '^[0-9a-f]{64}$'),
    CONSTRAINT upload_chunks_path_safe CHECK (relative_path ~ '^upload-sessions/[A-Za-z0-9_-]+/[0-9]+[.]part$')
);
