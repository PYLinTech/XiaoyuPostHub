-- 033.sql 动态令牌防重放与邀请码有效期
--
-- 1) users.last_totp_step：记录最近一次成功使用的动态码时间步长（30 秒一步）。
--    同一验证码在其有效窗口（±1 步）内可被再次用于换取新登录会话，属重放缺口；
--    校验时拒绝"步长不大于已用步长"的验证码。
-- 2) invitation_codes.expires_at：邀请码此前没有过期机制，泄露后长期可用；
--    新签发的邀请码默认 90 天有效（历史码为 NULL，不受影响）。

ALTER TABLE users ADD COLUMN IF NOT EXISTS last_totp_step BIGINT NOT NULL DEFAULT 0;

ALTER TABLE invitation_codes ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS invitation_codes_expires_idx
    ON invitation_codes(expires_at)
    WHERE expires_at IS NOT NULL;
