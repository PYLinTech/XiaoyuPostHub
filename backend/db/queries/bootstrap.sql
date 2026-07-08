-- =====================================================
-- bootstrap 专用 query
--
-- 原则：
--   - permissions：每次启动更新 description（文案变更不影响权限语义）
--   - system role：业务层 GetRoleByName 检测存在性，缺失时走普通 CreateRole。
--     role 行在 bootstrap 事务内由业务层代码创建/查询；本文件不重复专用 INSERT。
--   - system role 的权限绑定：仅在"该 role 首次创建"时由业务层循环调
--     InsertSystemPermissionForRoleIfMissing 写入。已存在时不重置。
--   - quota profile / user group：只通过本文件的专用 query 插入（ON CONFLICT 幂等）。
-- =====================================================

-- name: UpsertPermissionDefinition :exec
INSERT INTO permissions (code, description)
VALUES ($1, $2)
ON CONFLICT (code)
DO UPDATE SET description = EXCLUDED.description;

-- name: InsertSystemPermissionForRoleIfMissing :execrows
-- 仅在 bootstrap 首次创建 role 后调用：写入该 role 的默认 permission。
-- 启动逻辑：业务层在 GetRoleByName 判定 role 不存在 → CreateRole → 循环本 query。
-- 之后任何时间再调用本 query 都是幂等 no-op（ON CONFLICT DO NOTHING）。
INSERT INTO role_permissions (role_id, permission)
SELECT r.id, $2
FROM roles r
WHERE r.name = $1
ON CONFLICT DO NOTHING;

-- name: InsertQuotaProfileIfMissing :execrows
INSERT INTO quota_profiles (
    name,
    description,
    storage_bytes_limit,
    single_file_bytes_limit,
    daily_upload_bytes_limit,
    daily_upload_count_limit,
    active_share_count_limit,
    active_direct_link_limit,
    is_system
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE)
ON CONFLICT (name) DO NOTHING;

-- name: InsertSystemUserGroupIfMissing :execrows
INSERT INTO user_groups (name, is_system, description, quota_profile_id, priority)
VALUES ($1, TRUE, $2, $3, $4)
ON CONFLICT (name) DO NOTHING;

-- name: InsertDefaultUserGroupRoleBindingIfMissing :execrows
-- 业务层：仅在 default_user group 首次创建时调用，写入 user role 的绑定。
-- 已存在 default_user group 时**不**再调用（避免覆盖后台解绑/调整）。
-- ON CONFLICT DO NOTHING 保证多次调用安全。
INSERT INTO group_roles (group_id, role_id)
SELECT g.id, r.id
FROM user_groups g, roles r
WHERE g.name = $1 AND r.name = $2
ON CONFLICT DO NOTHING;
