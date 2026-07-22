ALTER TABLE group_permissions DROP CONSTRAINT IF EXISTS group_permissions_code_valid;
ALTER TABLE group_permissions ADD CONSTRAINT group_permissions_code_valid CHECK (
    permission IN ('login','upload','download','preview','rename','delete_own','share','pickup_share','direct_link','view_admin_overview','manage_users','manage_user_groups','manage_permissions','manage_quotas','manage_invitations','review_files','review_shares','read_audit_log','manage_system')
);
INSERT INTO group_permissions(group_id,permission)
SELECT id,'pickup_share' FROM user_groups WHERE name='default_user' ON CONFLICT DO NOTHING;
