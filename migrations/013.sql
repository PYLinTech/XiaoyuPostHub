-- 将旧版后台组合权限拆分为与各管理功能对应的原子权限。
-- 旧权限数据只在本迁移中转换，应用代码只使用最终权限原子。

INSERT INTO group_permissions (group_id, permission)
SELECT DISTINCT group_id, capability
FROM group_permissions
CROSS JOIN (
    VALUES
        ('view_admin_overview'),
        ('manage_user_groups'),
        ('manage_permissions'),
        ('manage_quotas'),
        ('manage_invitations')
) AS capabilities(capability)
WHERE permission = 'manage_roles'
ON CONFLICT DO NOTHING;

INSERT INTO group_permissions (group_id, permission)
SELECT DISTINCT group_id, capability
FROM group_permissions
CROSS JOIN (
    VALUES
        ('view_admin_overview'),
        ('review_files'),
        ('review_shares')
) AS capabilities(capability)
WHERE permission = 'read_audit_log'
ON CONFLICT DO NOTHING;

INSERT INTO group_permissions (group_id, permission)
SELECT DISTINCT group_id, 'view_admin_overview'
FROM group_permissions
WHERE permission = 'manage_users'
ON CONFLICT DO NOTHING;

DELETE FROM group_permissions WHERE permission = 'manage_roles';

ALTER TABLE group_permissions
    DROP CONSTRAINT IF EXISTS group_permissions_code_valid;

ALTER TABLE group_permissions
    ADD CONSTRAINT group_permissions_code_valid CHECK (
        permission IN (
            'login', 'upload', 'download', 'preview', 'rename', 'delete_own',
            'share', 'direct_link', 'view_admin_overview', 'manage_users',
            'manage_user_groups', 'manage_permissions', 'manage_quotas',
            'manage_invitations', 'review_files', 'review_shares',
            'read_audit_log', 'manage_system'
        )
    );
