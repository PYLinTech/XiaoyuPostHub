-- name: GetUserByUsername :one
SELECT id, username, password_hash, roles, groups, created_at
FROM users
WHERE username = $1;

-- name: CreateUser :one
INSERT INTO users (username, password_hash, roles, groups)
VALUES ($1, $2, $3, $4)
RETURNING id, username, password_hash, roles, groups, created_at;

-- name: UpdatePasswordHashByUsername :execrows
UPDATE users
SET password_hash = $2
WHERE username = $1;

-- name: RemoveAllFromAllUsers :execrows
UPDATE users
SET roles = ARRAY_REMOVE(roles, 'all')
WHERE 'all' = ANY(roles);
