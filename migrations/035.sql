-- 035.sql 秒传（按内容哈希复用物理对象）的范围可调
--
-- 背景：秒传按 sha256 + 精确大小复用已存在的物理对象，默认是"全平台"口径——任何
-- 用户只要知道某文件的哈希与大小，就能把自己的资源指向他人的私有对象并下载内容
-- （同时构成"服务器是否存有该文件"的哈希预言机）。这对私有文件是不可接受的越权
-- 读取，但全平台去重确实能显著省存储。
--
-- 因此改为可调：cross_user_dedupe = TRUE（默认，保持既有行为，与 README 的
-- "全平台 SHA-256 秒传"一致）；FALSE 时只复用"自己已有引用"的对象，他人私有
-- 文件无法被领取。管理员可在「系统配置」中随时切换。

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS cross_user_dedupe BOOLEAN NOT NULL DEFAULT TRUE;
