-- 027.sql — 管理员存储维护任务（跨后端迁移、存量补加密、孤儿对象清理）。
--
-- 设计：任务与明细在数据库中排队，由后端进程内的轮询器逐个执行（每 5 秒取一个
-- 排队任务），进度可随时查询；进程重启会把中断的 running 任务标记为失败，由
-- 管理员重新发起（迁移/补加密是幂等操作，可安全重跑）。
--
--   migrate       把对象迁移到指定后端（params.target_backend_id）
--   encrypt       把明文对象补加密（要求已配置加密密钥）
--   purge_orphan  物理删除引用计数为 0 的孤儿对象（唯一会真正删除文件的入口）

CREATE TABLE IF NOT EXISTS storage_tasks (
    id            BIGSERIAL PRIMARY KEY,
    task_type     TEXT        NOT NULL CHECK (task_type IN ('migrate', 'encrypt', 'purge_orphan')),
    params        JSONB       NOT NULL DEFAULT '{}',
    status        TEXT        NOT NULL DEFAULT 'queued'
                              CHECK (status IN ('queued', 'running', 'done', 'failed', 'canceled')),
    total_items   INT         NOT NULL DEFAULT 0,
    done_items    INT         NOT NULL DEFAULT 0,
    failed_items  INT         NOT NULL DEFAULT 0,
    error         TEXT,
    created_by    BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS storage_tasks_status_idx ON storage_tasks(status, id);

COMMENT ON TABLE storage_tasks IS '管理员存储维护任务（迁移 / 补加密 / 孤儿清理）。';
COMMENT ON COLUMN storage_tasks.status IS
    'queued=排队中，running=执行中，done=已结束（可能有失败项），failed=整体失败，canceled=已取消。';

CREATE TABLE IF NOT EXISTS storage_task_items (
    task_id    BIGINT      NOT NULL REFERENCES storage_tasks(id) ON DELETE CASCADE,
    blob_id    TEXT        NOT NULL,
    status     TEXT        NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending', 'done', 'failed', 'skipped')),
    error      TEXT,
    bytes_done BIGINT      NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (task_id, blob_id)
);

CREATE INDEX IF NOT EXISTS storage_task_items_status_idx
    ON storage_task_items(task_id, status);
