-- 启动期只保证默认配额方案与默认用户组存在，不维护角色或权限目录。

-- name: InsertQuotaProfileIfMissing :execrows
INSERT INTO quota_profiles (
    name, description, storage_bytes_limit, single_file_bytes_limit,
    daily_upload_bytes_limit, daily_upload_count_limit,
    active_share_count_limit, active_direct_link_limit, is_system
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE)
ON CONFLICT (name) DO NOTHING;

-- name: InsertSystemUserGroupIfMissing :execrows
INSERT INTO user_groups (name, is_system, description, quota_profile_id, priority)
VALUES ($1, TRUE, $2, $3, $4)
ON CONFLICT (name) DO NOTHING;

-- name: InsertDefaultGroupPermissionIfMissing :exec
INSERT INTO group_permissions(group_id, permission)
SELECT id, $2 FROM user_groups WHERE name=$1
ON CONFLICT DO NOTHING;
