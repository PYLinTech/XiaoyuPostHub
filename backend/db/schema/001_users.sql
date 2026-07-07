CREATE TABLE IF NOT EXISTS users (
    id            BIGSERIAL    PRIMARY KEY,
    username      VARCHAR(64)  NOT NULL UNIQUE,
    password_hash TEXT         NOT NULL,
    groups        TEXT[]       NOT NULL DEFAULT '{user}',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT users_groups_valid   CHECK ( groups <@ ARRAY['user', 'all'] ),
    CONSTRAINT users_groups_no_all  CHECK ( NOT ('all' = ANY(groups)) )
);