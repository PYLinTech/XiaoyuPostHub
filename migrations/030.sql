-- 030.sql — 用户组绑定存储后端。
--
-- 语义：组内用户的新上传写入该组绑定的存储后端；未绑定（NULL）时使用全局
-- 默认后端。多组用户按 priority 最高的「已绑定」组解析（与配额方案的选择
-- 规则一致）。管理员切换绑定后，由管理端自动创建迁移任务，把该组成员的
-- 存量对象从原后端搬到新后端（加密对象由服务端解密后以新密钥重新加密，
-- 因此需要部署已配置加密密钥）。
--
-- 删除存储后端时绑定自动置空（回退全局默认）：后端行删除意味着该后端不再
-- 可用，此时把组悬空指向已删行比回退默认更危险。

ALTER TABLE user_groups
    ADD COLUMN IF NOT EXISTS storage_backend_id BIGINT NULL
        REFERENCES storage_backends(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS user_groups_storage_backend_idx
    ON user_groups(storage_backend_id);

COMMENT ON COLUMN user_groups.storage_backend_id IS
    '组内用户新上传的目标存储后端；NULL（或后端被停用）表示使用全局默认后端。切换绑定时管理端自动创建迁移任务。';
