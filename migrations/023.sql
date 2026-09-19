-- 023.sql — 用户删除改为标记制，并新增文件审核历史表。
--
-- 背景：
--   1. 之前用户「永久删除」、清空回收站与回收站到期清理都会物理删除 resources
--      行和磁盘文件，导致管理员审查页在用户自行删除后完全丢失记录（存在
--      "上传 → 自行删除"规避审查的通道）。现在用户侧的一切删除操作只写
--      purged_at 标记：对用户不可见、不可恢复，但资源行与物理文件保留，
--      供管理员继续审查与清理；物理删除只保留管理员入口。
--   2. 新增 file_moderation_history 追加记录审核与处置全过程（覆盖上传、
--      彻底删除、管理员审核处置等）。resource_id 不设外键，资源行最终被
--      管理员清理后历史仍然保留。
--
-- 兼容性：purged_at 可空、历史表只增不改，升级期间旧版本二进制可以继续运行。

ALTER TABLE resources
    ADD COLUMN IF NOT EXISTS purged_at TIMESTAMPTZ;

COMMENT ON COLUMN resources.purged_at IS
    '用户侧彻底删除标记：对用户不可见、不可恢复；资源行保留供管理员审查与清理。';

-- 管理员按用户排查"已彻底删除"内容时使用。
CREATE INDEX IF NOT EXISTS resources_owner_purged_idx
    ON resources(owner_user_id)
    WHERE purged_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS file_moderation_history (
    id              BIGSERIAL   PRIMARY KEY,
    resource_id     TEXT        NOT NULL,
    owner_user_id   BIGINT,
    file_name       TEXT        NOT NULL DEFAULT '',
    size_bytes      BIGINT      NOT NULL DEFAULT 0,
    mime_type       TEXT,
    upload_task_id  TEXT        NOT NULL DEFAULT '',
    event           TEXT        NOT NULL,
    status          TEXT        NOT NULL DEFAULT '',
    reason          TEXT        NOT NULL DEFAULT '',
    delete_file     BOOLEAN     NOT NULL DEFAULT FALSE,
    blocked         BOOLEAN     NOT NULL DEFAULT FALSE,
    actor_type      TEXT        NOT NULL DEFAULT '',
    actor_name      TEXT        NOT NULL DEFAULT '',
    details         JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT file_moderation_history_event_valid
        CHECK (event IN ('submitted', 'approved', 'rejected', 'overwritten', 'purged', 'restored', 'migrated')),
    CONSTRAINT file_moderation_history_actor_valid
        CHECK (actor_type IN ('user', 'admin', 'system', ''))
);

CREATE INDEX IF NOT EXISTS file_moderation_history_resource_idx
    ON file_moderation_history(resource_id, created_at DESC);

CREATE INDEX IF NOT EXISTS file_moderation_history_created_idx
    ON file_moderation_history(created_at DESC);

COMMENT ON TABLE file_moderation_history IS
    '文件审核与处置历史：覆盖上传、彻底删除、管理员审核等事件只追加不修改，资源行被管理员清理后历史仍保留。';
COMMENT ON COLUMN file_moderation_history.event IS
    '事件类型：submitted=进入审核，approved/rejected=管理员审核，overwritten=被覆盖上传替换，purged=用户/系统/管理员彻底删除，restored=从回收站恢复，migrated=存储迁移。';
COMMENT ON COLUMN file_moderation_history.actor_type IS
    '事件发起方：user=资源所有者，admin=管理员，system=后台任务，空=未知（历史数据回填场景）。';
