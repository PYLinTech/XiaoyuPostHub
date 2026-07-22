-- name: GetUserByUsername :one
SELECT id, username, password_hash, is_disabled, created_at, totp_secret, totp_grace_used
FROM users
WHERE username = $1;

-- name: GetUserByID :one
-- 业务层 userInfo 接口走 user.Repo.GetByID 调用，命中此 query 拿基础字段。
SELECT id, username, password_hash, is_disabled, created_at, totp_secret, totp_grace_used
FROM users
WHERE id = $1;

-- name: CreateUser :one
-- 用户创建后由业务事务加入 default_user 用户组。
INSERT INTO users (username, password_hash)
VALUES ($1, $2)
RETURNING id, username, password_hash, is_disabled, created_at, totp_secret, totp_grace_used;

-- name: UpdatePasswordHashByUsername :execrows
UPDATE users
SET password_hash = $2
WHERE username = $1;
