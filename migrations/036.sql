-- 存储维护任务改为「先扫描、再执行」两阶段，并新增「解密」能力。
--
-- 背景：原来创建任务即开始改动数据。现在管理端可以先创建"扫描任务"拿到命中
-- 清单（不改动任何数据），确认后再针对该清单创建"执行任务"。
--
--   phase：scan=仅扫描并产出清单（创建后立即结束）；
--          apply=按清单逐项执行（原有行为）。
--   source_task_id：执行任务所依据的扫描任务，便于追溯"这次执行的依据"。
--                  扫描任务删除不影响执行任务（清单在创建执行任务时已复制）。
--
-- 兼容性：新列均带默认值/可空，存量任务自动视为 apply。

ALTER TABLE storage_tasks ADD COLUMN IF NOT EXISTS phase TEXT NOT NULL DEFAULT 'apply';
ALTER TABLE storage_tasks DROP CONSTRAINT IF EXISTS storage_tasks_phase_check;
ALTER TABLE storage_tasks ADD CONSTRAINT storage_tasks_phase_check
    CHECK (phase IN ('scan', 'apply'));

ALTER TABLE storage_tasks ADD COLUMN IF NOT EXISTS source_task_id BIGINT;
ALTER TABLE storage_tasks DROP CONSTRAINT IF EXISTS storage_tasks_source_task_fk;
ALTER TABLE storage_tasks ADD CONSTRAINT storage_tasks_source_task_fk
    FOREIGN KEY (source_task_id) REFERENCES storage_tasks(id) ON DELETE SET NULL;

-- 新增 decrypt：把加密对象改回明文存储（需要加密密钥可用，否则无法解出明文）。
ALTER TABLE storage_tasks DROP CONSTRAINT IF EXISTS storage_tasks_task_type_check;
ALTER TABLE storage_tasks ADD CONSTRAINT storage_tasks_task_type_check
    CHECK (task_type IN ('migrate', 'encrypt', 'decrypt', 'chunk', 'purge_orphan'));

CREATE INDEX IF NOT EXISTS storage_tasks_phase_idx ON storage_tasks(phase, status, id);

COMMENT ON COLUMN storage_tasks.phase IS 'scan=仅扫描产出清单（不改动数据）；apply=按清单执行。';
COMMENT ON COLUMN storage_tasks.source_task_id IS '执行任务依据的扫描任务；扫描任务自身为 NULL。';
