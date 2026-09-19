-- 032.sql 上传用量事件与审核查询索引
--
-- 1) upload_usage_events：把"每日上传额度"的口径从"统计 resources.created_at"
--    改为"每次上传完成追加事件"。原口径有两个漏洞：
--    - 覆盖上传不改变 created_at，反复覆盖即不计次、不计流量；
--    - 管理员物理清理资源行会让当日用量回落（额度被"返还"），与"删除不重置
--      每日额度"的防刷注释矛盾。
--    事件表只增不减：资源删除（ON DELETE SET NULL）不影响已记录的用量。
-- 2) 回填历史资源为一次 upload 事件（仅在事件表为空时执行，重复执行安全）。
-- 3) upload_sessions(resource_id) 索引：审核列表按 resource_id 关联最近上传
--    会话（LATERAL 子查询），缺索引会随会话表增长退化为全表扫描。

CREATE TABLE IF NOT EXISTS upload_usage_events (
    id             BIGSERIAL PRIMARY KEY,
    owner_user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    resource_id    TEXT REFERENCES resources(id) ON DELETE SET NULL,
    size_bytes     BIGINT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'upload',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS upload_usage_events_owner_time_idx
    ON upload_usage_events(owner_user_id, created_at DESC);

INSERT INTO upload_usage_events (owner_user_id, resource_id, size_bytes, kind, created_at)
SELECT r.owner_user_id, r.id, r.size_bytes, 'upload', r.created_at
FROM resources r
WHERE r.kind = 'file'
  AND NOT EXISTS (SELECT 1 FROM upload_usage_events);

CREATE INDEX IF NOT EXISTS upload_sessions_resource_id_idx
    ON upload_sessions(resource_id, created_at DESC)
    WHERE resource_id IS NOT NULL;
