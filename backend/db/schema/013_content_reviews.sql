-- 旧审核模型从未投入使用，启动时直接移除；新模型使用独立表名，避免兼容分支。
DROP TABLE IF EXISTS file_reviews;
DROP TABLE IF EXISTS share_reviews;

CREATE TABLE IF NOT EXISTS file_moderations (
    resource_id TEXT PRIMARY KEY,
    owner_user_id BIGINT,
    file_name TEXT NOT NULL DEFAULT '',
    size_bytes BIGINT NOT NULL DEFAULT 0,
    mime_type TEXT,
    upload_task_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    reason TEXT NOT NULL DEFAULT '',
    delete_file BOOLEAN NOT NULL DEFAULT FALSE,
    blocked BOOLEAN NOT NULL DEFAULT FALSE,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at TIMESTAMPTZ,
    reviewer_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS file_moderations_status_idx
    ON file_moderations(status, submitted_at DESC);

CREATE TABLE IF NOT EXISTS share_moderations (
    share_id BIGINT PRIMARY KEY REFERENCES shares(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    reason TEXT NOT NULL DEFAULT '',
    delete_link BOOLEAN NOT NULL DEFAULT FALSE,
    blocked BOOLEAN NOT NULL DEFAULT FALSE,
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at TIMESTAMPTZ,
    reviewer_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS share_moderations_status_idx
    ON share_moderations(status, submitted_at DESC);
