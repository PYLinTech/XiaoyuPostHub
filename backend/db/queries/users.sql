-- 说明：以下三条查询必须覆盖 users 表**全部列**，sqlc 才会把它们映射为模型
-- 类型 sqlcgen.User（否则每列一条会生成 GetUserByIDRow 这类匿名 Row 结构，
-- 与 user.Repo.hydrate 期望的 sqlcgen.User 不兼容，导致 sqlc 重新生成后编译
-- 失败）。新增 users 列时请同步这里（迁移与查询一起改）。

-- name: GetUserByUsername :one
SELECT id, username, password_hash, is_disabled, created_at, totp_secret, totp_grace_used, last_totp_step
FROM users
WHERE username = $1;

-- name: GetUserByID :one
-- 业务层 userInfo 接口走 user.Repo.GetByID 调用，命中此 query 拿基础字段。
SELECT id, username, password_hash, is_disabled, created_at, totp_secret, totp_grace_used, last_totp_step
FROM users
WHERE id = $1;

-- name: CreateUser :one
-- 用户创建后由业务事务加入 default_user 用户组。
INSERT INTO users (username, password_hash)
VALUES ($1, $2)
RETURNING id, username, password_hash, is_disabled, created_at, totp_secret, totp_grace_used, last_totp_step;

-- name: UpdatePasswordHashByUsername :execrows
UPDATE users
SET password_hash = $2
WHERE username = $1;
