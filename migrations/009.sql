-- 站内消息总表。消息是否可见按接收范围实时计算，因此新注册用户也能看到
-- 注册前发给 all 或其当前用户组的历史消息。
CREATE TABLE IF NOT EXISTS messages (
    id             BIGSERIAL   PRIMARY KEY,
    sent_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    receiver_type  TEXT        NOT NULL,
    receiver_id    BIGINT,
    message_type   TEXT        NOT NULL,
    content        JSONB       NOT NULL,

    CONSTRAINT messages_receiver_type_valid CHECK (receiver_type IN ('all', 'group', 'user')),
    CONSTRAINT messages_receiver_shape CHECK (
        (receiver_type = 'all' AND receiver_id IS NULL)
        OR (receiver_type IN ('group', 'user') AND receiver_id IS NOT NULL)
    ),
    CONSTRAINT messages_type_not_blank CHECK (BTRIM(message_type) <> ''),
    CONSTRAINT messages_content_object CHECK (jsonb_typeof(content) = 'object')
);

CREATE INDEX IF NOT EXISTS messages_sent_idx ON messages(id DESC);
CREATE INDEX IF NOT EXISTS messages_receiver_idx ON messages(receiver_type, receiver_id, id DESC);

-- 每个用户只为实际操作过的消息保存一行状态，避免把不断增长的消息编号数组
-- 重复塞进 users。deleted 已包含 read 语义，因此单列枚举即可。
CREATE TABLE IF NOT EXISTS user_message_states (
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    state      TEXT   NOT NULL CHECK (state IN ('read', 'deleted')),
    PRIMARY KEY (user_id, message_id)
);

CREATE INDEX IF NOT EXISTS user_message_states_message_idx
    ON user_message_states(message_id);

-- 迁移旧版 users 数组字段，随后删除不再使用的列。
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='read_message_ids') THEN
        INSERT INTO user_message_states(user_id,message_id,state)
        SELECT u.id, ids.message_id, 'read'
        FROM users u CROSS JOIN LATERAL unnest(u.read_message_ids) AS ids(message_id)
        JOIN messages m ON m.id=ids.message_id
        ON CONFLICT(user_id,message_id) DO NOTHING;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='users' AND column_name='deleted_message_ids') THEN
        INSERT INTO user_message_states(user_id,message_id,state)
        SELECT u.id, ids.message_id, 'deleted'
        FROM users u CROSS JOIN LATERAL unnest(u.deleted_message_ids) AS ids(message_id)
        JOIN messages m ON m.id=ids.message_id
        ON CONFLICT(user_id,message_id) DO UPDATE SET state='deleted';
    END IF;
END $$;

ALTER TABLE users DROP COLUMN IF EXISTS read_message_ids;
ALTER TABLE users DROP COLUMN IF EXISTS deleted_message_ids;
