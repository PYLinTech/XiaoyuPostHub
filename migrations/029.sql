-- 029.sql — 强制分片存储：分片不再可关闭。
--
-- 背景：分片与加密天然兼容（分片边界与加密块 4MiB 对齐，每片可独立随机读取），
-- 同时是远端后端（对象存储/网盘）与管理员维护能力（迁移/补加密/重新分片）的
-- 基础形态，因此系统不再提供"不分片"选项：
--   * 新上传一律按 storage_chunk_size_bytes 分片（默认 64MiB）；
--   * 存量未分片对象可通过「存储管理 → 重新分片」重写，或保持原样正常读取。
--
-- 兼容性：仅收紧约束（0 与 <4MiB 的旧值统一提升为默认 64MiB）。

ALTER TABLE system_settings DROP CONSTRAINT IF EXISTS system_settings_storage_chunk_valid;

UPDATE system_settings
SET storage_chunk_size_bytes = 67108864
WHERE storage_chunk_size_bytes < 4194304;

ALTER TABLE system_settings
    ALTER COLUMN storage_chunk_size_bytes SET DEFAULT 67108864;

ALTER TABLE system_settings ADD CONSTRAINT system_settings_storage_chunk_valid CHECK (
    storage_chunk_size_bytes >= 4194304
    AND storage_chunk_size_bytes <= 1073741824
    AND storage_chunk_size_bytes % 4194304 = 0
);

COMMENT ON COLUMN system_settings.storage_chunk_size_bytes IS
    '分片存储粒度（字节，强制分片，不可为 0）；必须是加密块 4MiB 的整数倍。';
