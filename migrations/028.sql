-- 028.sql — 分片存储配置与分片任务类型。
--
--   storage_chunk_size_bytes  新上传按此大小把对象切成多个物理条目（0 = 不分片）。
--                             取值必须是加密块（4MiB）的整数倍，这样加密后每个
--                             分片仍然独立可随机读取（Range/断点续传/预览可用）。
--   storage_tasks.task_type  新增 'chunk'：把已有对象按指定粒度重新分片。
--
-- 兼容性：新列带默认值；CHECK 约束先删后建（幂等）。

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS storage_chunk_size_bytes INT NOT NULL DEFAULT 0;

ALTER TABLE system_settings DROP CONSTRAINT IF EXISTS system_settings_storage_chunk_valid;
ALTER TABLE system_settings ADD CONSTRAINT system_settings_storage_chunk_valid CHECK (
    storage_chunk_size_bytes = 0
    OR (storage_chunk_size_bytes >= 4194304
        AND storage_chunk_size_bytes <= 1073741824
        AND storage_chunk_size_bytes % 4194304 = 0)
);

COMMENT ON COLUMN system_settings.storage_chunk_size_bytes IS
    '分片存储粒度（字节，0=不分片）；必须是加密块 4MiB 的整数倍。';

ALTER TABLE storage_tasks DROP CONSTRAINT IF EXISTS storage_tasks_task_type_check;
ALTER TABLE storage_tasks ADD CONSTRAINT storage_tasks_task_type_check
    CHECK (task_type IN ('migrate', 'encrypt', 'chunk', 'purge_orphan'));
