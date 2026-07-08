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
-- 3 级优先级：users.quota_profile_id > group.quota_profile_id(priority DESC) > name='default_user'
--
-- 防御性：用户不存在时返回 no rows（不会误返回 default_user quota）。
-- 每个候选源都限定到真实存在的用户；前两级都空时才用 default_user 兜底；
-- 外层显式列名 + ORDER BY priority_tier（仅用于排序）+ LIMIT 1 保证 :one 单行返回。
-- 不在 SELECT 列表里输出 priority_tier，让 sqlc 直接返回 QuotaProfile 类型。
SELECT
    id, name, description,
    storage_bytes_limit, single_file_bytes_limit,
    daily_upload_bytes_limit, daily_upload_count_limit,
    active_share_count_limit, active_direct_link_limit,
    is_system, created_at, updated_at
FROM (
    -- 候选 1：用户自己的 quota
    SELECT
        qp.id, qp.name, qp.description,
        qp.storage_bytes_limit, qp.single_file_bytes_limit,
        qp.daily_upload_bytes_limit, qp.daily_upload_count_limit,
        qp.active_share_count_limit, qp.active_direct_link_limit,
        qp.is_system, qp.created_at, qp.updated_at,
        1 AS priority_tier
    FROM users u
    JOIN quota_profiles qp ON qp.id = u.quota_profile_id
    WHERE u.id = $1

    UNION ALL

    -- 候选 2：所属 group 中 priority 最高的 quota
    SELECT * FROM (
        SELECT
            qp.id, qp.name, qp.description,
            qp.storage_bytes_limit, qp.single_file_bytes_limit,
            qp.daily_upload_bytes_limit, qp.daily_upload_count_limit,
            qp.active_share_count_limit, qp.active_direct_link_limit,
            qp.is_system, qp.created_at, qp.updated_at,
            2 AS priority_tier
        FROM user_group_memberships ugm
        JOIN user_groups g ON g.id = ugm.group_id
        JOIN quota_profiles qp ON qp.id = g.quota_profile_id
        WHERE ugm.user_id = $1
        ORDER BY g.priority DESC, g.id ASC
        LIMIT 1
    ) AS group_top

    UNION ALL

    -- 候选 3：默认 quota（仅在用户存在时）
    SELECT
        qp.id, qp.name, qp.description,
        qp.storage_bytes_limit, qp.single_file_bytes_limit,
        qp.daily_upload_bytes_limit, qp.daily_upload_count_limit,
        qp.active_share_count_limit, qp.active_direct_link_limit,
        qp.is_system, qp.created_at, qp.updated_at,
        3 AS priority_tier
    FROM quota_profiles qp
    WHERE qp.name = 'default_user'
      AND EXISTS (SELECT 1 FROM users WHERE id = $1)
) candidates
ORDER BY priority_tier ASC
LIMIT 1;
