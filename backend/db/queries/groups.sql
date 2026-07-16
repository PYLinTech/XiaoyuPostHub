-- name: GetUserGroupByName :one
SELECT id, name, is_system, description, quota_profile_id, priority, created_at
FROM user_groups
WHERE name = $1;

-- name: GetUserGroupByID :one
SELECT id, name, is_system, description, quota_profile_id, priority, created_at
FROM user_groups
WHERE id = $1;

-- name: ListUserGroups :many
SELECT id, name, is_system, description, quota_profile_id, priority, created_at
FROM user_groups
ORDER BY priority DESC, id ASC;

-- name: CreateUserGroup :one
INSERT INTO user_groups (name, is_system, description, quota_profile_id, priority)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, name, is_system, description, quota_profile_id, priority, created_at;

-- name: UpdateUserGroupQuotaProfile :execrows
UPDATE user_groups
SET quota_profile_id = $2
WHERE id = $1;

-- name: UpdateUserGroupPriority :execrows
UPDATE user_groups
SET priority = $2
WHERE id = $1;

-- name: UpdateUserGroupSystemFlag :execrows
-- 启动期修复 user group 的 is_system 标志位（不重置其它字段）。
UPDATE user_groups
SET is_system = $2
WHERE name = $1;

-- name: DeleteUserGroup :execrows
-- 仅禁止删系统 group（保留 name 不可改 / row 不可删的语义）。
-- 其他字段（description / quota / priority）放开。
DELETE FROM user_groups
WHERE id = $1 AND is_system = FALSE;

-- name: AssignUserToGroup :execrows
INSERT INTO user_group_memberships (user_id, group_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: UnassignAllGroupsFromUser :execrows
-- 清空 user 的所有 group 关联（用于升级为超管时清理非默认组，随后重新绑定默认组）
DELETE FROM user_group_memberships
WHERE user_id = $1;

-- name: ListGroupIDsByUser :many
SELECT group_id
FROM user_group_memberships
WHERE user_id = $1
ORDER BY group_id;

-- name: ListPermissionsByGroup :many
SELECT permission
FROM group_permissions
WHERE group_id = $1
ORDER BY permission;

-- name: ListEffectivePermissionsByUser :many
SELECT DISTINCT gp.permission
FROM user_group_memberships membership
JOIN group_permissions gp ON gp.group_id = membership.group_id
WHERE membership.user_id = $1
ORDER BY gp.permission;

-- name: DeletePermissionsByGroup :exec
DELETE FROM group_permissions WHERE group_id = $1;

-- name: AddPermissionToGroup :exec
INSERT INTO group_permissions(group_id, permission)
VALUES($1, $2)
ON CONFLICT DO NOTHING;
