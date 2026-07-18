-- name: EnsureSystemSettings :exec
INSERT INTO system_settings (id, site_name, storage_path)
VALUES (1, 'XiaoyuPostHub', '/data/uploads')
ON CONFLICT (id) DO NOTHING;

-- name: GetSystemSettings :one
SELECT * FROM system_settings WHERE id=1;

-- name: UpdateSystemIdentity :one
UPDATE system_settings SET site_name=$1, storage_path=$2, updated_at=NOW()
WHERE id=1 RETURNING *;

-- name: UpdateAllSystemSettings :one
UPDATE system_settings SET
    site_name=sqlc.arg(site_name),
    storage_path=sqlc.arg(storage_path),
    folder_pack_mode=sqlc.arg(folder_pack_mode),
    share_delivery_mode=sqlc.arg(share_delivery_mode),
    invitation_length=sqlc.arg(invitation_length),
    invitation_case_sensitive=sqlc.arg(invitation_case_sensitive),
    invitation_include_letters=sqlc.arg(invitation_include_letters),
    invitation_include_numbers=sqlc.arg(invitation_include_numbers),
    share_length=sqlc.arg(share_length),
    share_case_sensitive=sqlc.arg(share_case_sensitive),
    share_include_letters=sqlc.arg(share_include_letters),
    share_include_numbers=sqlc.arg(share_include_numbers),
    upload_requires_review=sqlc.arg(upload_requires_review),
    custom_share_requires_review=sqlc.arg(custom_share_requires_review),
    upload_chunk_size_bytes=sqlc.arg(upload_chunk_size_bytes),
    upload_task_chunk_concurrency=sqlc.arg(upload_task_chunk_concurrency),
    upload_user_task_concurrency=sqlc.arg(upload_user_task_concurrency),
    trash_retention_days=sqlc.arg(trash_retention_days),
    updated_at=NOW()
WHERE id=1 RETURNING *;
