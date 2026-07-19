-- users 表
--
-- 用户仅保存账户自身数据；权限与配额全部由 user_group_memberships 继承。
CREATE TABLE IF NOT EXISTS users (
    id                BIGSERIAL    PRIMARY KEY,
    username          VARCHAR(64)  NOT NULL UNIQUE,
    password_hash     TEXT         NOT NULL,
    is_disabled       BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

DROP INDEX IF EXISTS users_is_disabled_idx;
