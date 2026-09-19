-- name: GetUserGroupByName :one
SELECT id, name, is_system, description, quota_profile_id, priority, created_at, storage_backend_id
FROM user_groups
WHERE name = $1;

-- name: GetUserGroupByID :one
SELECT id, name, is_system, description, quota_profile_id, priority, created_at, storage_backend_id
FROM user_groups
WHERE id = $1;

-- name: ListUserGroups :many
SELECT id, name, is_system, description, quota_profile_id, priority, created_at, storage_backend_id
FROM user_groups
ORDER BY priority DESC, id ASC;

-- name: CreateUserGroup :one
INSERT INTO user_groups (name, is_system, description, quota_profile_id, priority)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, name, is_system, description, quota_profile_id, priority, created_at, storage_backend_id;

-- name: UpdateUserGroupQuotaProfile :execrows
UPDATE user_groups
SET quota_profile_id = $2
WHERE id = $1;

-- name: UpdateUserGroupPriority :execrows
UPDATE user_groups
SET priority = $2
WHERE id = $1;

-- name: UpdateUserGroupStorageBackend :execrows
-- 绑定组内用户新上传的目标存储后端；NULL 表示回退全局默认后端。
-- 管理员切换绑定后，由调用方（管理端）创建迁移任务把该组存量对象搬到新后端。
UPDATE user_groups
SET storage_backend_id = $2
WHERE id = $1;

-- name: UpdateUserGroupSystemFlag :execrows
-- 启动时同步系统用户组的 is_system 标志位（不重置其他字段）。
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
-- 清空用户的所有用户组关联，随后由调用方绑定目标用户组。
DELETE FROM user_group_memberships
WHERE user_id = $1;

-- name: ListGroupIDsByUser :many
SELECT group_id
FROM user_group_memberships
WHERE user_id = $1
ORDER BY group_id;

-- name: ListEffectivePermissionsByUser :many
SELECT DISTINCT gp.permission
FROM user_group_memberships membership
JOIN group_permissions gp ON gp.group_id = membership.group_id
WHERE membership.user_id = $1
ORDER BY gp.permission;

-- name: ListPermissionsByGroupName :many
-- 按用户组名读取权限集合：匿名请求按 guest 系统用户组校验消费侧权限
-- （preview / download）时使用。guest 组不接受成员，无法走上一条 SQL。
SELECT gp.permission
FROM user_groups g
JOIN group_permissions gp ON gp.group_id = g.id
WHERE g.name = $1
ORDER BY gp.permission;

-- name: GetEffectiveStorageBackendByUser :one
-- 用户的有效存储后端：所属组中 priority 最高且已绑定后端的组（与配额方案的
-- 选择规则一致）。未绑定任何后端时返回 sql.ErrNoRows，调用方回退全局默认。
-- is_enabled 一并返回：绑定后端被停用属于明确错误（不允许静默改写到其它后端）。
SELECT g.storage_backend_id, sb.is_enabled
FROM user_group_memberships membership
JOIN user_groups g ON g.id = membership.group_id
JOIN storage_backends sb ON sb.id = g.storage_backend_id
WHERE membership.user_id = $1 AND g.storage_backend_id IS NOT NULL
ORDER BY g.priority DESC, g.id ASC
LIMIT 1;
