CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL    PRIMARY KEY,
    username      VARCHAR(64)  NOT NULL UNIQUE,
    password_hash TEXT         NOT NULL,
    roles         TEXT[]       NOT NULL DEFAULT '{user}',
    groups        TEXT[]       NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    -- roles：固定枚举（user 普通 / all 超管），数据库 + 代码双重固定。
    -- 'all' 永远不落库：CHECK no_all 兜底，CHECK valid 限定取值范围。
    CONSTRAINT users_roles_valid   CHECK ( roles <@ ARRAY['user', 'all'] ),
    CONSTRAINT users_roles_no_all  CHECK ( NOT ('all' = ANY(roles)) )
    -- groups：用户组，完全开放，不加 CHECK。
    -- 规划用于配额管理（VIP/SVIP 等），目前不实现业务逻辑，仅预留字段。
);
