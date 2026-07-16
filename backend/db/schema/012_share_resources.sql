-- 一个分享的有序根资源清单，是分享与资源关系的唯一来源。
CREATE TABLE IF NOT EXISTS share_resources (
    share_id       BIGINT  NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
    resource_id    TEXT    NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
    display_order  INTEGER NOT NULL,
    PRIMARY KEY (share_id, resource_id),
    CONSTRAINT share_resources_order_non_negative CHECK (display_order >= 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS share_resources_order_unique
    ON share_resources(share_id, display_order);

-- 迁移旧版 shares.resource_id 主资源字段，随后删除重复关系列。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='shares' AND column_name='resource_id') THEN
        INSERT INTO share_resources(share_id,resource_id,display_order)
        SELECT id,resource_id,0 FROM shares
        ON CONFLICT(share_id,resource_id) DO NOTHING;
    END IF;
END $$;

ALTER TABLE shares DROP COLUMN IF EXISTS resource_id;
