-- 审核开关保存在 system_settings，本文件只定义审核业务记录。
CREATE TABLE IF NOT EXISTS file_reviews (
    resource_id TEXT PRIMARY KEY REFERENCES resources(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    reason TEXT NOT NULL DEFAULT '',
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at TIMESTAMPTZ,
    reviewer_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS file_reviews_status_idx
    ON file_reviews(status, submitted_at DESC);

CREATE TABLE IF NOT EXISTS share_reviews (
    share_id BIGINT PRIMARY KEY REFERENCES shares(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    reason TEXT NOT NULL DEFAULT '',
    submitted_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reviewed_at TIMESTAMPTZ,
    reviewer_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX IF NOT EXISTS share_reviews_status_idx
    ON share_reviews(status, submitted_at DESC);
