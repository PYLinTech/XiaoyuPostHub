-- 038.sql — 配额补充「下载方向」限额，并新增未登录访客（guest）用户组。
--
-- 1) quota_profiles 增加下载方向限额（与上传方向对称，NULL = 不限，0 = 不允许）：
--      daily_download_bytes_limit  每日下载流量（字节）
--      daily_download_count_limit  每日下载次数
--    口径按自然日（服务器本地时区）统计「下载者」：登录用户按账号，未登录访问
--    按来源 IP（每 IP 视为一个独立用户），数据来自 download_usage_events。
-- 2) download_usage_events：下载用量事件表。分享页 / 取件码 / 直链 / 文件页每次
--    下载（创建下载计划或直链完整取流）追加一条事件。事件只增不减：资源被删除
--    或物理清理不会让当日额度回落（与 upload_usage_events 同一防刷口径）。
-- 3) 新增系统配额方案 guest 与系统用户组 guest（is_system = TRUE，业务层禁止删除）：
--    未登录用户不匹配任何用户组成员身份，统一按 guest 组绑定的配额方案限流；
--    管理员可在「权限与配额」中为该组改绑其它方案或直接调整 guest 方案数值。
--
-- 兼容性：只增列 / 增表，旧版本二进制可继续运行（新列不被其读取）。

ALTER TABLE quota_profiles
    ADD COLUMN IF NOT EXISTS daily_download_bytes_limit BIGINT,
    ADD COLUMN IF NOT EXISTS daily_download_count_limit BIGINT;

ALTER TABLE quota_profiles DROP CONSTRAINT IF EXISTS quota_profiles_non_negative;
ALTER TABLE quota_profiles
    ADD CONSTRAINT quota_profiles_non_negative
        CHECK (
            (storage_bytes_limit IS NULL OR storage_bytes_limit >= 0)
            AND (single_file_bytes_limit IS NULL OR single_file_bytes_limit >= 0)
            AND (daily_upload_bytes_limit IS NULL OR daily_upload_bytes_limit >= 0)
            AND (daily_upload_count_limit IS NULL OR daily_upload_count_limit >= 0)
            AND (daily_download_bytes_limit IS NULL OR daily_download_bytes_limit >= 0)
            AND (daily_download_count_limit IS NULL OR daily_download_count_limit >= 0)
            AND (active_share_count_limit IS NULL OR active_share_count_limit >= 0)
            AND (active_direct_link_limit IS NULL OR active_direct_link_limit >= 0)
        );

COMMENT ON COLUMN quota_profiles.daily_download_bytes_limit IS
    '每日下载流量上限（字节，NULL = 不限，0 = 不允许下载）；按下载者统计：登录用户按账号，未登录按来源 IP。';
COMMENT ON COLUMN quota_profiles.daily_download_count_limit IS
    '每日下载次数上限（NULL = 不限，0 = 不允许下载）；口径同上。';

CREATE TABLE IF NOT EXISTS download_usage_events (
    id          BIGSERIAL   PRIMARY KEY,
    user_id     BIGINT      REFERENCES users(id) ON DELETE CASCADE,
    client_ip   TEXT,
    source      TEXT        NOT NULL DEFAULT 'download',
    size_bytes  BIGINT      NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT download_usage_events_subject_check
        CHECK (user_id IS NOT NULL OR client_ip IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS download_usage_events_user_time_idx
    ON download_usage_events(user_id, created_at DESC)
    WHERE user_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS download_usage_events_ip_time_idx
    ON download_usage_events(client_ip, created_at DESC)
    WHERE client_ip IS NOT NULL;

COMMENT ON TABLE download_usage_events IS
    '每日下载用量事件（只增不减）：登录用户按 user_id，未登录按 client_ip 计；额度按自然日（服务器本地时区）统计。';

INSERT INTO quota_profiles (name, description, is_system)
VALUES ('guest', '未登录访客配额（按 IP 识别，空值表示不限）', TRUE)
ON CONFLICT (name) DO NOTHING;

INSERT INTO user_groups (name, is_system, description, quota_profile_id, priority)
SELECT 'guest', TRUE, '未登录访客用户组（按 IP 识别为一个用户，不可删除）', qp.id, 0
FROM quota_profiles qp
WHERE qp.name = 'guest'
ON CONFLICT (name) DO NOTHING;
