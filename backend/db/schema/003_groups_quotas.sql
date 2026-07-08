-- 用户组 + 个人权限覆盖
-- quota_profiles 已在 000_quota_profiles.sql 建好，本文件不重复建。
--
-- 权限来源 4 层（user package 内部合并）：
--   1. anonymous role → 访客
--   2. user_roles → role_permissions
--   3. user_group_memberships → group_roles → role_permissions
--   4. user_permission_overrides（allow ∪ deny）
-- 最终 = (2) ∪ (3) ∪ allow - deny
--
-- 配额 3 级优先级（quota package 内部合并）：
--   1. users.quota_profile_id（用户专属，001 已内联 FK）
--   2. 用户所属 group 中 priority 最高的 quota_profile_id
--   3. quota_profiles.name = 'default_user'

-- =====================================================
-- user_groups：用户组定义
-- is_system=true 不可删；priority 越高越优先匹配 quota。
-- =====================================================
CREATE TABLE IF NOT EXISTS user_groups (
    id               BIGSERIAL   PRIMARY KEY,
    name             TEXT        NOT NULL UNIQUE,
    is_system        BOOLEAN     NOT NULL DEFAULT FALSE,
    description      TEXT,
    quota_profile_id BIGINT      REFERENCES quota_profiles(id) ON DELETE SET NULL,
    priority         INTEGER     NOT NULL DEFAULT 0,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT user_groups_name_format
        CHECK (name ~ '^[a-z][a-z0-9_]{1,31}$')
);

-- =====================================================
-- user_group_memberships：用户 ↔ 用户组 多对多
-- =====================================================
CREATE TABLE IF NOT EXISTS user_group_memberships (
    user_id     BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id    BIGINT      NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, group_id)
);
-- 反向查询"这个 group 有哪些 user"用
CREATE INDEX IF NOT EXISTS user_group_memberships_group_id_idx
    ON user_group_memberships(group_id);

-- =====================================================
-- group_roles：用户组 ↔ 角色 多对多
-- 用户组权限必须通过 group_roles → role_permissions 获得，不存 group_permissions。
-- anonymous role (assignable=false) 不允许插入此表。
-- =====================================================
CREATE TABLE IF NOT EXISTS group_roles (
    group_id    BIGINT      NOT NULL REFERENCES user_groups(id) ON DELETE CASCADE,
    role_id     BIGINT      NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_id, role_id)
);
-- 反向查询"这个 role 给了哪些 group"用
CREATE INDEX IF NOT EXISTS group_roles_role_id_idx
    ON group_roles(role_id);

-- =====================================================
-- user_permission_overrides：用户个人权限覆盖
-- effect: 'allow' 或 'deny'。deny 只存在于本层，不下放到 role / group。
-- 最终权限 = (role ∪ group) ∪ allow - deny
-- =====================================================
CREATE TABLE IF NOT EXISTS user_permission_overrides (
    user_id     BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    permission  TEXT        NOT NULL REFERENCES permissions(code) ON DELETE CASCADE,
    effect      TEXT        NOT NULL,
    reason      TEXT,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, permission),

    CONSTRAINT user_permission_overrides_effect_valid
        CHECK (effect IN ('allow', 'deny'))
);
