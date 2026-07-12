-- name: GetUserByUsername :one
SELECT id, username, password_hash, quota_profile_id, created_at
FROM users
WHERE username = $1;

-- name: GetUserByID :one
-- 业务层 userInfo 接口走 user.Repo.GetByID 调用，命中此 query 拿基础字段。
SELECT id, username, password_hash, quota_profile_id, created_at
FROM users
WHERE id = $1;

-- name: CreateUser :one
-- v2：去掉 groups 参数；权限通过 user_group_memberships → group_roles 表达。
INSERT INTO users (username, password_hash)
VALUES ($1, $2)
RETURNING id, username, password_hash, quota_profile_id, created_at;

-- name: UpdatePasswordHashByUsername :execrows
UPDATE users
SET password_hash = $2
WHERE username = $1;

-- name: UpdateUserQuotaProfile :execrows
UPDATE users
SET quota_profile_id = $2
WHERE id = $1;
