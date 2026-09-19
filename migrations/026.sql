-- 026.sql — 加密存储与交付策略（管理员可控）。
--
-- 三个开关都在 system_settings（非敏感运行期配置）：
--   encrypt_new_files      新上传文件是否加密（默认关闭：存量与新装行为不变，
--                          管理员显式开启后新文件用 AES-256-GCM 分块加密存储）
--   proxy_realtime_decrypt 中转交付加密文件时是否由服务器实时解密（默认开启）；
--                          关闭后分享页/文件页改为"中转密文 + 前端解密"，
--                          直链不受此开关影响（始终服务器解密）
--   share_retrieval_mode   分享页交付方式：proxy=本机中转，redirect=302 直跳
--                          第三方（对象存储/云盘；本机后端不支持时自动降级中转）
--
-- 兼容性：全部为带默认值的新列，旧版本二进制回滚后可继续运行。

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS encrypt_new_files BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS proxy_realtime_decrypt BOOLEAN NOT NULL DEFAULT TRUE;

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS share_retrieval_mode TEXT NOT NULL DEFAULT 'proxy';

ALTER TABLE system_settings DROP CONSTRAINT IF EXISTS system_settings_share_retrieval_mode_valid;
ALTER TABLE system_settings ADD CONSTRAINT system_settings_share_retrieval_mode_valid
    CHECK (share_retrieval_mode IN ('proxy', 'redirect'));

COMMENT ON COLUMN system_settings.encrypt_new_files IS
    '新上传文件是否加密存储（AES-256-GCM 分块，每文件随机 DEK，KEK 来自 .env）。';
COMMENT ON COLUMN system_settings.proxy_realtime_decrypt IS
    '本机中转交付加密文件时是否服务器实时解密；关闭时由前端解密（密钥实时非对称下发）。直链始终服务器解密。';
COMMENT ON COLUMN system_settings.share_retrieval_mode IS
    '分享页交付方式：proxy=本机中转，redirect=302 直跳第三方；需要解密的场景强制中转。';
