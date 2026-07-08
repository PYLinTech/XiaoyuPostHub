-- name: ListRolesByUser :many
-- 返回指定 user 的所有 role id（按 role_id 排序）
SELECT role_id
FROM user_roles
WHERE user_id = $1
ORDER BY role_id;

-- name: ListPermissionsByUser :many
-- 直接 role 绑定的 permission（不含用户组继承和覆盖）
SELECT DISTINCT rp.permission
FROM user_roles ur
JOIN role_permissions rp ON rp.role_id = ur.role_id
WHERE ur.user_id = $1
ORDER BY rp.permission;

-- name: AssignRoleToUser :execrows
INSERT INTO user_roles (user_id, role_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: UnassignRoleFromUser :execrows
DELETE FROM user_roles
WHERE user_id = $1 AND role_id = $2;

-- name: UnassignAllRolesFromUser :execrows
-- 清空 user 的所有 role 关联（用于升级为超管时清残留）
DELETE FROM user_roles
WHERE user_id = $1;

-- name: ClearAllPermissionOverridesByUserID :execrows
-- 清空 user 的所有 permission override（用于升级为超管时清残留）
DELETE FROM user_permission_overrides
WHERE user_id = $1;

-- name: DeleteUserRolesByNonAssignableRoles :execrows
-- 启动期清理：删除 user_roles 中 role.assignable=false 的记录。
-- anonymous (assignable=false) 不允许分配给真实 user。
DELETE FROM user_roles
WHERE role_id IN (SELECT id FROM roles WHERE assignable = FALSE);

-- name: DeleteGroupRolesByNonAssignableRoles :execrows
-- 启动期清理：删除 group_roles 中 role.assignable=false 的记录。
-- anonymous (assignable=false) 不允许绑定给 group。
DELETE FROM group_roles
WHERE role_id IN (SELECT id FROM roles WHERE assignable = FALSE);

-- name: ListEffectivePermissionsByUser :many
-- 完整权限合并：(user_roles + group_roles) ∪ user_allow - user_deny
-- 单条 SQL 出结果，业务层无 merge 负担。
WITH inherited_permissions AS (
    SELECT rp.permission
    FROM user_roles ur
    JOIN role_permissions rp ON rp.role_id = ur.role_id
    WHERE ur.user_id = $1

    UNION

    SELECT rp.permission
    FROM user_group_memberships ugm
    JOIN group_roles gr ON gr.group_id = ugm.group_id
    JOIN role_permissions rp ON rp.role_id = gr.role_id
    WHERE ugm.user_id = $1
),
allowed_permissions AS (
    SELECT permission
    FROM inherited_permissions

    UNION

    SELECT permission
    FROM user_permission_overrides
    WHERE user_id = $1 AND effect = 'allow'
),
denied_permissions AS (
    SELECT permission
    FROM user_permission_overrides
    WHERE user_id = $1 AND effect = 'deny'
)
SELECT DISTINCT permission
FROM allowed_permissions
WHERE permission NOT IN (SELECT permission FROM denied_permissions)
ORDER BY permission;

-- name: ListAnonymousPermissions :many
-- 访客权限 = anonymous role 的 permission 集合
SELECT rp.permission
FROM roles r
JOIN role_permissions rp ON rp.role_id = r.id
WHERE r.name = 'anonymous'
ORDER BY rp.permission;

-- name: SetUserPermissionOverride :execrows
INSERT INTO user_permission_overrides (user_id, permission, effect, reason)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, permission)
DO UPDATE SET
    effect = EXCLUDED.effect,
    reason = EXCLUDED.reason,
    updated_at = NOW();

-- name: ClearUserPermissionOverride :execrows
DELETE FROM user_permission_overrides
WHERE user_id = $1 AND permission = $2;

-- name: ListUserPermissionOverrides :many
SELECT user_id, permission, effect, reason, assigned_at, updated_at
FROM user_permission_overrides
WHERE user_id = $1
ORDER BY permission;
