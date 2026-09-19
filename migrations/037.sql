-- 037.sql — 交付策略精简：前端接收定稿。
--
--   * 删除三项旧配置：
--       folder_pack_mode       文件夹固定「前端打包」，服务端不再生成分享 ZIP；
--       share_delivery_mode    分享页固定「前端接收」，不再有一次性临时链接；
--       proxy_realtime_decrypt 解密一律下放前端（浏览器带临时公钥时下发密文 +
--                              密钥信封），只有请求方无法前端解密（无公钥，如
--                              HTTP 部署或命令行工具）时才由服务器实时解密兜底；
--   * share_retrieval_mode 默认值改为 redirect（302 优先，可切回中转优先）；
--   * 新增 redirect_fallback：302 不可用（后端无直链/前端无公钥）时是否自动降级
--     本机中转，默认开启。该列由 systemsetting.Knobs 用原始 SQL 读写，不依赖
--     sqlc 重新生成。
--   * share_download_jobs 删除制品与打包相关列：单文件不再落制品、文件夹固定前端
--     打包，服务端不再生成分享 ZIP。
--
-- 兼容性：删除的列不再被任何代码读取；旧版本二进制回滚后会缺列，属于预期内的
-- 不可逆迁移（交付模型同步精简）。

ALTER TABLE system_settings DROP CONSTRAINT IF EXISTS system_settings_pack_mode_valid;
ALTER TABLE system_settings DROP CONSTRAINT IF EXISTS system_settings_delivery_mode_valid;
ALTER TABLE system_settings DROP COLUMN IF EXISTS folder_pack_mode;
ALTER TABLE system_settings DROP COLUMN IF EXISTS share_delivery_mode;
ALTER TABLE system_settings DROP COLUMN IF EXISTS proxy_realtime_decrypt;

ALTER TABLE system_settings ALTER COLUMN share_retrieval_mode SET DEFAULT 'redirect';
-- 存量部署统一按新默认执行（管理员仍可在面板切回「中转优先」）。
UPDATE system_settings SET share_retrieval_mode = 'redirect' WHERE id = 1;

COMMENT ON COLUMN system_settings.share_retrieval_mode IS
    '分享取数方式：redirect=302 优先（默认，浏览器直连第三方），proxy=本机中转优先；'
    '302 不可用时的行为由 redirect_fallback 决定。';

ALTER TABLE share_download_jobs DROP CONSTRAINT IF EXISTS share_download_jobs_pack_mode_valid;
ALTER TABLE share_download_jobs DROP CONSTRAINT IF EXISTS share_download_jobs_delivery_mode_valid;
ALTER TABLE share_download_jobs DROP CONSTRAINT IF EXISTS share_download_jobs_artifact_shape;
ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS pack_mode;
ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS delivery_mode;
ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS artifact_path;
ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS artifact_name;
ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS artifact_content_type;
ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS artifact_sha256;
ALTER TABLE share_download_jobs DROP COLUMN IF EXISTS artifact_temporary;

COMMENT ON TABLE share_download_jobs IS
    '短时下载任务：一次下载操作对应一个任务，任务内所有文件完整取流后计数一次。';
