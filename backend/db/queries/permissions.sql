-- name: ListPermissions :many
SELECT code, description
FROM permissions
ORDER BY code;

-- name: ListPermissionCodes :many
SELECT code
FROM permissions;
