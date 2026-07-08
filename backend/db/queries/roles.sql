-- 业务层 role CRUD（不含 seed 流程）
--   - 启动期 bootstrap 直接走普通 CreateRole（先 GetRoleByName 检测存在性），
--     在单事务 + advisory lock 下完成；不依赖 bootstrap.sql 专用 query。
--   - 这里只服务"创建/修改/删除非系统 role"和"给 role 绑 permission"
--   - 系统 role（is_system=true）的 permission 绑定**允许**通过这里修改

-- name: GetRoleByName :one
SELECT id, name, is_system, assignable, description, created_at
FROM roles
WHERE name = $1;

-- name: GetRoleByID :one
SELECT id, name, is_system, assignable, description, created_at
FROM roles
WHERE id = $1;

-- name: ListRoles :many
SELECT id, name, is_system, assignable, description, created_at
FROM roles
ORDER BY id;

-- name: CreateRole :one
INSERT INTO roles (name, is_system, assignable, description)
VALUES ($1, $2, $3, $4)
RETURNING id, name, is_system, assignable, description, created_at;

-- name: UpdateRoleDescription :execrows
-- 系统 role 的 description **允许**通过配置面板修改。
-- name / row 不可改不可删由 SQL CHECK (roles_no_super_admin) + 业务层守卫。
UPDATE roles
SET description = $2
WHERE id = $1;

-- name: UpdateRoleSystemFlags :execrows
-- 启动期修复系统 role 的身份字段：is_system / assignable。
-- 用于 bootstrap 防御性把"漂移"的系统标志位改回正确值。
UPDATE roles
SET is_system = $2, assignable = $3
WHERE name = $1;

-- name: DeleteRole :execrows
DELETE FROM roles
WHERE id = $1 AND is_system = FALSE;

-- name: ListRolePermissions :many
SELECT permission
FROM role_permissions
WHERE role_id = $1
ORDER BY permission;

-- name: AddPermissionToRole :execrows
-- 系统 role 的 permission 绑定允许通过这里修改（不限制 is_system）
INSERT INTO role_permissions (role_id, permission)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: RemovePermissionFromRole :execrows
DELETE FROM role_permissions
WHERE role_id = $1 AND permission = $2;
