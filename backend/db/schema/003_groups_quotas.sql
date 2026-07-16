-- 用户组是账户权限与配额的唯一来源。
-- 用户只通过 user_group_memberships 关联用户组；不再存在角色、用户级权限覆盖
-- 或用户级配额字段。

CREATE TABLE IF NOT EXISTS user_groups (
    id               BIGSERIAL   PRIMARY KEY,
    name             TEXT        NOT NULL UNIQUE,
    is_system        BOOLEAN     NOT NULL DEFAULT FALSE,
    description      TEXT,
    quota_profile_id BIGINT      NOT NULL REFERENCES quota_profiles(id) ON DELETE RESTRICT,
    priority         INTEGER     NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_groups_name_format
        CHECK (name ~ '^[a-z][a-z0-9_]{1,31}$')
);

CREATE TABLE IF NOT EXISTS user_group_memberships (
    user_id     BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id    BIGINT      NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, group_id)
);

ALTER TABLE user_group_memberships DROP COLUMN IF EXISTS assigned_at;

CREATE INDEX IF NOT EXISTS user_group_memberships_group_id_idx
    ON user_group_memberships(group_id);

-- 权限 code 的权威来源是 backend/permission 中的代码常量；数据库仅保存用户组
-- 当前被授予的 code，不再维护一份重复的权限目录表。
CREATE TABLE IF NOT EXISTS group_permissions (
    group_id   BIGINT NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    permission TEXT   NOT NULL,
    PRIMARY KEY (group_id, permission),

    CONSTRAINT group_permissions_code_valid CHECK (
        permission IN (
            'login', 'upload', 'download', 'preview', 'rename', 'delete_own',
            'share', 'direct_link', 'manage_users',
            'read_audit_log', 'manage_roles'
        )
    )
);

CREATE INDEX IF NOT EXISTS group_permissions_permission_idx
    ON group_permissions(permission);

-- 清理旧版从未接入任何接口的 delete_any 权限，并收紧已有数据库的约束。
DELETE FROM group_permissions WHERE permission = 'delete_any';
ALTER TABLE group_permissions DROP CONSTRAINT IF EXISTS group_permissions_code_valid;
ALTER TABLE group_permissions ADD CONSTRAINT group_permissions_code_valid CHECK (
    permission IN (
        'login', 'upload', 'download', 'preview', 'rename', 'delete_own',
        'share', 'direct_link', 'manage_users', 'read_audit_log', 'manage_roles'
    )
);
