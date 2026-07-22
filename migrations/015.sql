-- 每个用户只允许一个登录会话。升级时仅保留最新的一条，再由唯一约束兜底。
DELETE FROM user_sessions AS older
USING user_sessions AS newer
WHERE older.user_id = newer.user_id
  AND (older.created_at, older.id) < (newer.created_at, newer.id);

CREATE UNIQUE INDEX IF NOT EXISTS user_sessions_one_per_user_idx
    ON user_sessions(user_id);
