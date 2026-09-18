-- 022.sql — 登录动态令牌挑战增加失败计数
--
-- 背景：挑战（login_totp_challenges）在验证码校验失败时会保留，原实现没有
-- 尝试次数上限，攻击者在密码已泄露的前提下可以在 5 分钟有效期内持续暴力
-- 尝试 6 位验证码。增加 failed_attempts 后，达到上限的挑战会被直接删除，
-- 用户需要重新用密码换取新挑战。
ALTER TABLE login_totp_challenges
    ADD COLUMN IF NOT EXISTS failed_attempts INTEGER NOT NULL DEFAULT 0;

-- 老数据（升级前创建的挑战）默认 0，可以直接沿用。
COMMENT ON COLUMN login_totp_challenges.failed_attempts IS
    '该挑战已验证码失败次数；达到应用上限后挑战被删除，用户需重新登录换取新挑战。';
