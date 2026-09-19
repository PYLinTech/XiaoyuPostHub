-- 024.sql — 物理对象与逻辑资源分离：storage_backends / storage_blobs / blob_parts。
--
-- 背景：为支持加密存储、对象存储、第三方云盘、分片存储与管理员跨存储迁移，
-- 把"物理对象"从资源行中抽离：
--   resources.blob_id ──▶ storage_blobs（可被多个资源共享，引用计数）
--                            └─ blob_parts（分片；未分片时 part_count=1 且无 parts 行）
--
--   - storage_backends 保存存储后端配置（敏感凭据在 .env 中按名称引用）。
--   - storage_blobs.encryption_* 为加密元数据（DEK 由 .env 中的 KEK 包裹后存储）。
--   - 物理删除只发生在管理员清理（ref_count 归零的 orphaned 对象），应用侧不做级联删除。
--
-- 兼容性：全部为新增表/列；resources.storage_key 保留（历史数据与回滚期间继续可读），
-- 新代码写入 blob_id。storage_key 约束放宽为"blob_id 或 storage_key 至少一个存在"。
-- 回填按资源逐行创建 blob（object_ref 沿用原 storage_key），幂等可重复执行。

CREATE TABLE IF NOT EXISTS storage_backends (
    id                     BIGSERIAL   PRIMARY KEY,
    name                   TEXT        NOT NULL UNIQUE,
    kind                   TEXT        NOT NULL,
    settings               JSONB       NOT NULL DEFAULT '{}',
    retrieval_mode         TEXT        NOT NULL DEFAULT 'proxy',
    proxy_realtime_decrypt BOOLEAN     NOT NULL DEFAULT TRUE,
    encrypt_new_files      BOOLEAN,
    supports_presign       BOOLEAN     NOT NULL DEFAULT FALSE,
    is_enabled             BOOLEAN     NOT NULL DEFAULT TRUE,
    is_default             BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT storage_backends_kind_valid
        CHECK (kind IN ('local', 's3', 'pan123')),
    CONSTRAINT storage_backends_retrieval_valid
        CHECK (retrieval_mode IN ('proxy', 'redirect'))
);

-- 同一时间只允许一个默认后端。
CREATE UNIQUE INDEX IF NOT EXISTS storage_backends_single_default_idx
    ON storage_backends(is_default) WHERE is_default;

CREATE TABLE IF NOT EXISTS storage_blobs (
    id                TEXT        PRIMARY KEY,
    size_bytes        BIGINT      NOT NULL DEFAULT 0,
    sha256            CHAR(64)    NOT NULL,
    backend_id        BIGINT      NOT NULL REFERENCES storage_backends(id),
    object_ref        TEXT        NOT NULL,
    encryption_algo   TEXT,
    encryption_key_id TEXT,
    encryption_nonce  BYTEA,
    encryption_dek    BYTEA,
    chunk_size        INTEGER     NOT NULL DEFAULT 0,
    part_count        INTEGER     NOT NULL DEFAULT 1,
    ref_count         BIGINT      NOT NULL DEFAULT 0,
    status            TEXT        NOT NULL DEFAULT 'active',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT storage_blobs_status_valid
        CHECK (status IN ('active', 'orphaned', 'broken', 'migrating')),
    CONSTRAINT storage_blobs_encryption_shape
        CHECK ((encryption_algo IS NULL AND encryption_key_id IS NULL AND encryption_nonce IS NULL AND encryption_dek IS NULL)
            OR (encryption_algo IS NOT NULL AND encryption_key_id IS NOT NULL AND encryption_nonce IS NOT NULL AND encryption_dek IS NOT NULL)),
    CONSTRAINT storage_blobs_shape
        CHECK (size_bytes >= 0 AND part_count >= 1 AND chunk_size >= 0 AND ref_count >= 0)
);

CREATE INDEX IF NOT EXISTS storage_blobs_backend_status_idx
    ON storage_blobs(backend_id, status);

-- 秒传复用与配额统计使用。
CREATE INDEX IF NOT EXISTS storage_blobs_sha256_idx
    ON storage_blobs(sha256, size_bytes, status);

-- 分片存储：blob_parts 保存每片的定位符与大小；未分片对象不产生行。
CREATE TABLE IF NOT EXISTS blob_parts (
    blob_id     TEXT        NOT NULL REFERENCES storage_blobs(id) ON DELETE CASCADE,
    part_index  INTEGER     NOT NULL,
    size_bytes  BIGINT      NOT NULL,
    object_ref  TEXT        NOT NULL,
    sha256      CHAR(64),
    PRIMARY KEY (blob_id, part_index),

    CONSTRAINT blob_parts_index_valid CHECK (part_index >= 0),
    CONSTRAINT blob_parts_size_valid CHECK (size_bytes > 0)
);

ALTER TABLE resources
    ADD COLUMN IF NOT EXISTS blob_id TEXT REFERENCES storage_blobs(id);

CREATE INDEX IF NOT EXISTS resources_blob_idx
    ON resources(blob_id) WHERE blob_id IS NOT NULL;

-- 文件资源以 blob_id（新模型）或 storage_key（历史数据）为准；文件夹继续保持无存储引用。
ALTER TABLE resources DROP CONSTRAINT IF EXISTS resources_file_shape;
ALTER TABLE resources ADD CONSTRAINT resources_file_shape CHECK (
    (kind = 'file' AND sha256_checksum IS NOT NULL AND (storage_key IS NOT NULL OR blob_id IS NOT NULL))
    OR
    (kind = 'folder' AND storage_key IS NULL AND blob_id IS NULL AND sha256_checksum IS NULL AND size_bytes = 0)
);

-- 初始化本地后端（读取 system_settings.storage_path 作为根目录）。
INSERT INTO storage_backends (name, kind, settings, supports_presign, is_default)
VALUES ('local', 'local', '{}', FALSE, TRUE)
ON CONFLICT (name) DO NOTHING;

-- 回填：为每个历史文件资源创建一个本机 blob（object_ref 沿用原 storage_key）。
INSERT INTO storage_blobs (id, size_bytes, sha256, backend_id, object_ref, ref_count, status)
SELECT 'blob-' || r.id, r.size_bytes, r.sha256_checksum, b.id, r.storage_key, 1, 'active'
FROM resources r
CROSS JOIN storage_backends b
WHERE r.kind = 'file'
  AND r.storage_key IS NOT NULL
  AND r.blob_id IS NULL
  AND b.name = 'local'
ON CONFLICT (id) DO NOTHING;

UPDATE resources r
SET blob_id = 'blob-' || r.id
WHERE r.kind = 'file'
  AND r.blob_id IS NULL
  AND EXISTS (SELECT 1 FROM storage_blobs b WHERE b.id = 'blob-' || r.id);

COMMENT ON TABLE storage_backends IS
    '存储后端配置（本机/对象存储/第三方云盘）。敏感凭据不落库，settings 仅保存非敏感参数，按 name 引用 .env 中的密钥。';
COMMENT ON COLUMN storage_backends.retrieval_mode IS
    '分享页交付方式：proxy=本机中转，redirect=302 直跳第三方。直链固定走本机中转。';
COMMENT ON COLUMN storage_backends.proxy_realtime_decrypt IS
    '本机中转是否由服务器实时解密；关闭时加密内容由前端解密（302 模式下该开关不生效）。';
COMMENT ON TABLE storage_blobs IS
    '物理对象：多个资源可引用同一对象（引用计数）；用户侧删除只改资源行，物理清理仅由管理员执行。';
COMMENT ON COLUMN storage_blobs.object_ref IS
    '后端内的定位符：本机=相对存储路径，对象存储=S3 key，第三方云盘=文件 ID。';
COMMENT ON COLUMN storage_blobs.encryption_dek IS
    '用 .env 中 KEK 包裹后的每文件数据密钥；明文密钥只存在于内存，KEK 丢失即数据不可恢复。';
COMMENT ON COLUMN storage_blobs.chunk_size IS
    '分片存储的每片明文字节数；0 表示未分片（单对象）。分片边界与加密分块对齐。';
COMMENT ON TABLE blob_parts IS
    '分片对象的每片元数据；part_count>1 时交付按片读取（Range 请求映射到对应片）。';
