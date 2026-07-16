-- name: GetQuotaProfileByName :one
SELECT id, name, description, storage_bytes_limit, single_file_bytes_limit,
       daily_upload_bytes_limit, daily_upload_count_limit,
       active_share_count_limit, active_direct_link_limit,
       is_system, created_at, updated_at
FROM quota_profiles
WHERE name = $1;

-- name: GetQuotaProfileByID :one
SELECT id, name, description, storage_bytes_limit, single_file_bytes_limit,
       daily_upload_bytes_limit, daily_upload_count_limit,
       active_share_count_limit, active_direct_link_limit,
       is_system, created_at, updated_at
FROM quota_profiles
WHERE id = $1;

-- name: ListQuotaProfiles :many
SELECT id, name, description, storage_bytes_limit, single_file_bytes_limit,
       daily_upload_bytes_limit, daily_upload_count_limit,
       active_share_count_limit, active_direct_link_limit,
       is_system, created_at, updated_at
FROM quota_profiles
ORDER BY is_system DESC, id ASC;

-- name: CreateQuotaProfile :one
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
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, FALSE)
RETURNING id, name, description, storage_bytes_limit, single_file_bytes_limit,
          daily_upload_bytes_limit, daily_upload_count_limit,
          active_share_count_limit, active_direct_link_limit,
          is_system, created_at, updated_at;

-- name: UpdateQuotaProfile :execrows
-- 系统 quota profile 允许通过配置面板修改数值（不限制 is_system），
-- 因此 default_user 配额也能改。
UPDATE quota_profiles
SET
    description = $2,
    storage_bytes_limit = $3,
    single_file_bytes_limit = $4,
    daily_upload_bytes_limit = $5,
    daily_upload_count_limit = $6,
    active_share_count_limit = $7,
    active_direct_link_limit = $8,
    updated_at = NOW()
WHERE id = $1;

-- name: UpdateQuotaProfileSystemFlag :execrows
-- 启动期修复 quota profile 的 is_system 标志位（不重置其它字段）。
UPDATE quota_profiles
SET is_system = $2
WHERE name = $1;

-- name: DeleteQuotaProfile :execrows
DELETE FROM quota_profiles
WHERE id = $1 AND is_system = FALSE;

-- name: GetEffectiveQuotaByUser :one
-- 配额唯一来源是用户组；多个用户组时取 priority 最高者。
SELECT
    qp.id, qp.name, qp.description,
    qp.storage_bytes_limit, qp.single_file_bytes_limit,
    qp.daily_upload_bytes_limit, qp.daily_upload_count_limit,
    qp.active_share_count_limit, qp.active_direct_link_limit,
    qp.is_system, qp.created_at, qp.updated_at
FROM users u
JOIN user_group_memberships membership ON membership.user_id = u.id
JOIN user_groups g ON g.id = membership.group_id
JOIN quota_profiles qp ON qp.id = g.quota_profile_id
WHERE u.id = $1
ORDER BY g.priority DESC, g.id ASC
LIMIT 1;
