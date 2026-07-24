ALTER TABLE upload_sessions
    ADD COLUMN IF NOT EXISTS conflict_action TEXT NOT NULL DEFAULT 'error';

ALTER TABLE upload_sessions
    DROP CONSTRAINT IF EXISTS upload_sessions_conflict_action_valid;
ALTER TABLE upload_sessions
    ADD CONSTRAINT upload_sessions_conflict_action_valid
    CHECK (conflict_action IN ('error', 'overwrite', 'auto_rename'));
