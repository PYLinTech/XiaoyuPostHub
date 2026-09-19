-- 034.sql 取件码可回收 + 管理端新增可调项
--
-- 1) 取件码"永久占位"问题：原唯一索引覆盖所有历史行（含已删除/已停用/已过期），
--    被删除的取件码也永不归还，短码配置下码空间会被逐步耗尽且无法自救。
--    新增 pickup_code_live 标志，唯一索引只约束"仍占位"的码；失效码由维护任务
--    与手动清理释放后可被重新分配（码仍保留在行上供追溯）。
-- 2) 新增管理端可调项：
--    - pickup_allow_permanent：是否允许创建/修改为"永久有效"的取件码分享
--    - invitation_valid_days：邀请码有效期（天，0 = 永久）
--    - upload_max_file_bytes：单文件系统硬上限（独立于用户组配额的安全上限）

ALTER TABLE shares ADD COLUMN IF NOT EXISTS pickup_code_live BOOLEAN NOT NULL DEFAULT TRUE;

DROP INDEX IF EXISTS shares_pickup_code_unique;
CREATE UNIQUE INDEX IF NOT EXISTS shares_pickup_code_live_unique
    ON shares(pickup_code) WHERE pickup_code IS NOT NULL AND pickup_code_live;

-- 回填：已删除 / 已停用 / 已过期 / 审核判定需删链的取件码不再占位（码保留在行上
-- 供追溯，可被重新分配）。判定口径与运行时的 ReleaseDeadPickupCodes 完全一致，
-- 保证本文件重复执行（未登记时重跑）不会把已释放的码错误置回占位。
UPDATE shares s
SET pickup_code_live = (
        s.deleted_at IS NULL AND s.is_active AND (s.expires_at IS NULL OR s.expires_at > NOW())
        AND COALESCE((SELECT NOT (m.status='rejected' AND m.delete_link)
                      FROM share_moderations m WHERE m.share_id=s.id), TRUE)
    )
WHERE s.share_type = 'pickup' AND s.pickup_code IS NOT NULL;

ALTER TABLE system_settings
    ADD COLUMN IF NOT EXISTS pickup_allow_permanent BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS invitation_valid_days SMALLINT NOT NULL DEFAULT 90,
    ADD COLUMN IF NOT EXISTS upload_max_file_bytes BIGINT NOT NULL DEFAULT 107374182400;

DO $$
BEGIN
    ALTER TABLE system_settings
        ADD CONSTRAINT system_settings_invitation_valid_days CHECK (invitation_valid_days BETWEEN 0 AND 3650);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
    ALTER TABLE system_settings
        ADD CONSTRAINT system_settings_upload_max_file_bytes CHECK (upload_max_file_bytes >= 16777216);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;
