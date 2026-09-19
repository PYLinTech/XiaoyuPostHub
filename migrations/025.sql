-- 025.sql — 分享下载任务支持统一交付会话（单文件不再落本机制品）。
--
-- 背景：单文件分享的下载改由统一交付接口（/dl/<id>）直接流式输出物理对象，
-- 不再生成本机制品文件；任务行仍然保留（用于预扣额度、区间计数与审计），
-- 但 artifact_path 允许为空。旧的制品模式（文件夹后端打包）继续有效，
-- 其 artifact_path 仍必须存在。

ALTER TABLE share_download_jobs DROP CONSTRAINT IF EXISTS share_download_jobs_artifact_shape;
ALTER TABLE share_download_jobs ADD CONSTRAINT share_download_jobs_artifact_shape CHECK (
    (pack_mode = 'backend'
        AND artifact_name IS NOT NULL
        AND artifact_content_type IS NOT NULL
        AND (artifact_sha256 IS NULL OR artifact_sha256 ~ '^[0-9a-f]{64}$'))
    OR
    (pack_mode = 'frontend'
        AND artifact_path IS NULL
        AND artifact_name IS NULL
        AND artifact_content_type IS NULL
        AND artifact_sha256 IS NULL)
);

COMMENT ON COLUMN share_download_jobs.artifact_path IS
    '后端打包制品在本机的路径；单文件分享走统一交付接口时为空。';
