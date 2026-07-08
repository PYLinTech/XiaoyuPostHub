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

-- name: UpdateUserGroupDescription :execrows
-- 系统 group（is_system=TRUE）的 description / quota / priority **允许**通过配置面板修改。
-- name 和 row 是否删除由 SQL/业务层用 is_system 守卫。
UPDATE user_groups
SET description = $2
WHERE id = $1;

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

-- name: UnassignUserFromGroup :execrows
DELETE FROM user_group_memberships
WHERE user_id = $1 AND group_id = $2;

-- name: UnassignAllGroupsFromUser :execrows
-- 清空 user 的所有 group 关联（用于升级为超管时清残留）
DELETE FROM user_group_memberships
WHERE user_id = $1;

-- name: ListGroupIDsByUser :many
SELECT group_id
FROM user_group_memberships
WHERE user_id = $1
ORDER BY group_id;

-- name: AssignRoleToGroup :execrows
INSERT INTO group_roles (group_id, role_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: UnassignRoleFromGroup :execrows
DELETE FROM group_roles
WHERE group_id = $1 AND role_id = $2;

-- name: ListRoleIDsByGroup :many
SELECT role_id
FROM group_roles
WHERE group_id = $1
ORDER BY role_id;
