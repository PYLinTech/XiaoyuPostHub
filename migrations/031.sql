-- 031.sql — 存储任务的「旧物理定位符」回溯记录。
--
-- 背景：迁移/补加密/重新分片遵循「先写新对象 → 更新元数据 → 再删旧对象」的顺序。
-- 若第三步（删旧对象）失败，元数据已指向新对象，旧物理对象变得无任何记录可查；
-- 重跑任务时又会因「已在目标后端」被幂等跳过，旧对象将永远无法被清理。
--
-- 方案：在替换元数据之前，把旧对象的定位符（后端 id + 各分片 ref 清单）写入任务
-- 项；重跑该任务项时先按记录补删旧对象，再继续正常流程。清理成功后清除记录。
--
-- 迁移编写规范（供后续迁移遵循，详见 README 的数据库迁移章节）：
--   - 升级为停机窗口（install.sh 先停服务再迁移）：所有迁移都在单事务内执行，
--     文件内部必须幂等（IF NOT EXISTS / ON CONFLICT / 先 DROP ... IF EXISTS 再 ADD）；
--   - ADD COLUMN：直接执行即可（常量默认值是轻量元数据操作）；
--   - ADD CONSTRAINT：直接一次成型（无读写竞争，无需 NOT VALID + VALIDATE 两步）；
--   - CREATE INDEX：直接 `CREATE INDEX IF NOT EXISTS`（单遍扫描更快，失败随事务
--     回滚不留无效索引）。**不要使用 CREATE INDEX CONCURRENTLY**：它不能在事务块
--     内执行，而迁移助手一律把文件包进事务；
--   - 数据回填与结构变更尽量拆分为独立迁移文件（便于失败定位与重跑）。

ALTER TABLE storage_task_items
    ADD COLUMN IF NOT EXISTS old_backend_id BIGINT,
    ADD COLUMN IF NOT EXISTS old_object_refs TEXT[];

COMMENT ON COLUMN storage_task_items.old_backend_id IS
    '续跑回溯：上一次执行已替换元数据、但旧对象尚未清理成功时所在的后端 id。';
COMMENT ON COLUMN storage_task_items.old_object_refs IS
    '续跑回溯：旧对象的物理定位符清单（分片对象为各分片 ref）。清理成功后置空。';
