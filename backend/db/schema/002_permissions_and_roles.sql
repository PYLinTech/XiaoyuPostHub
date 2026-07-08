-- v2 权限系统：4 张核心表
--
-- 角色语义：
--   is_system = true：
--     - name 不可改
--     - role 行不可删
--     - permission 绑定**允许**通过权限配置面板修改（不强制只能走 PR）
--   assignable = false：
--     - 不允许分配给真实用户
--     - 不允许分配给用户组
--     - anonymous 必须 assignable=false
--
-- 启动期 seed 通过 db/queries/bootstrap.sql 的专用 query 完成（带 ON CONFLICT），
-- 普通业务 CRUD（CreateRole/UpdateRole/DeleteRole）不参与 seed 流程。
--
-- 重要不变量：
--   - name = 'super_admin' 是**代码层概念**（真超管走 .env 短路）；
--     SQL 层加 CHECK roles_no_super_admin 兜底，业务层 CreateRole 拒绝，
--     双重保证它不会进 roles 表。
--   - assigned_at 保留：审计表后续需要用它做追溯。

-- =====================================================
-- permissions：权限枚举（原子动作）
-- description 字段是展示文案，启动 seed 会**更新**它（ON CONFLICT DO UPDATE）。
-- =====================================================
CREATE TABLE IF NOT EXISTS permissions (
    code        TEXT PRIMARY KEY,
    description TEXT NOT NULL
);

-- =====================================================
-- roles：角色定义
-- UNIQUE 约束自带唯一索引。
-- =====================================================
CREATE TABLE IF NOT EXISTS roles (
    id          BIGSERIAL    PRIMARY KEY,
    name        TEXT         NOT NULL UNIQUE,
    is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    assignable  BOOLEAN      NOT NULL DEFAULT TRUE,
    description TEXT,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT roles_name_format      CHECK (name ~ '^[a-z][a-z0-9_]{1,31}$'),
    CONSTRAINT roles_no_super_admin  CHECK (name <> 'super_admin')
);

-- =====================================================
-- role_permissions：角色 ↔ 权限 多对多
-- permission 直接用 code 当 FK，删除 permission 时自动级联清理关联。
-- 系统角色的此表内容允许通过配置面板修改。
-- =====================================================
CREATE TABLE IF NOT EXISTS role_permissions (
    role_id     BIGINT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    permission  TEXT   NOT NULL REFERENCES permissions(code) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission)
);

-- =====================================================
-- user_roles：用户 ↔ 角色 多对多
-- 普通用户走 user_roles 绑定 role；超管不入此表。
-- anonymous role (assignable=false) 不允许插入此表。
-- =====================================================
CREATE TABLE IF NOT EXISTS user_roles (
    user_id     BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id     BIGINT      NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
    assigned_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, role_id)
);
-- 反向查询"这个 role 给了哪些 user"用
CREATE INDEX IF NOT EXISTS user_roles_role_id_idx ON user_roles(role_id);
