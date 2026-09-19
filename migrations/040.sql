-- 040.sql — 分享消费侧接入用户组权限，并为 guest 用户组授予默认预览/下载权限。
--
-- 背景：此前分享 / 取件 / 直链的「消费动作」（在线预览、下载）完全不校验权限：
--   * 登录用户组的 preview / download 在分享页不生效，属于权限绕过点；
--   * guest 系统用户组（038 预置）没有任何 group_permissions，组权限配置形同
--     虚设，匿名流量只受 guest 配额方案约束。
--
-- 本次起「能否预览 / 下载」由消费者身份决定（与下载配额的主体口径一致）：
--   * 已登录：按账号所属用户组当前被授予的 preview / download；
--   * 未登录：按 guest 用户组当前被授予的 preview / download。
--
-- 本迁移为 guest 组补齐默认的 preview + download，保证升级后匿名分享行为与
-- 升级前一致；管理员可在「权限与配额」中取消勾选来关闭匿名预览 / 下载。
-- 服务端在 guest 组或配额方案缺失、读取失败时按 fail-closed 拒绝（503），
-- 不再静默放行。
--
-- 幂等：仅补齐缺失的权限行，不重置管理员已有的配置（迁移只执行一次，
-- 启动期由 bootstrap 兜底新建场景）。旧版本二进制不读取组权限的消费侧语义，
-- 该迁移对回滚安全。

INSERT INTO group_permissions (group_id, permission)
SELECT g.id, defaults.permission
FROM user_groups g
CROSS JOIN (VALUES ('preview'), ('download')) AS defaults(permission)
WHERE g.name = 'guest'
ON CONFLICT (group_id, permission) DO NOTHING;
