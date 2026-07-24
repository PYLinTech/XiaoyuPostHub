-- 消息正文统一为 HTML；旧 JSON 消息只在本迁移中转换，业务代码不保留兼容分支。
CREATE FUNCTION xph_migration_021_html_escape(value TEXT)
RETURNS TEXT
LANGUAGE SQL
IMMUTABLE
STRICT
PARALLEL SAFE
AS $$
    SELECT replace(
        replace(
            replace(
                replace(
                    replace(value, '&', '&amp;'),
                    '<', '&lt;'
                ),
                '>', '&gt;'
            ),
            '"', '&quot;'
        ),
        '''', '&#39;'
    )
$$;

ALTER TABLE messages
    ADD COLUMN title TEXT,
    ADD COLUMN tag TEXT,
    ADD COLUMN content_html TEXT;

UPDATE messages AS message
SET
    title = COALESCE(
        NULLIF(BTRIM(message.content ->> 'title'), ''),
        '系统消息'
    ),
    tag = COALESCE(
        NULLIF(BTRIM(message.content ->> 'tag'), ''),
        CASE message.message_type
            WHEN 'invitation' THEN '邀请码'
            WHEN 'moderation' THEN '审核结果'
            ELSE '系统'
        END
    ),
    content_html =
        CASE
            WHEN NULLIF(BTRIM(message.content ->> 'body'), '') IS NULL
                THEN '<p>暂无消息正文</p>'
            ELSE '<p>' ||
                replace(
                    replace(
                        xph_migration_021_html_escape(message.content ->> 'body'),
                        E'\r',
                        ''
                    ),
                    E'\n',
                    '<br>'
                ) ||
                '</p>'
        END
        ||
        COALESCE(
            (
                SELECT
                    '<div class="message-copy-actions">' ||
                    string_agg(
                        '<button type="button" data-message-action="copy" data-copy-text="' ||
                        xph_migration_021_html_escape(code.value) ||
                        '"><span>复制邀请码</span><code>' ||
                        xph_migration_021_html_escape(code.value) ||
                        '</code></button>',
                        ''
                        ORDER BY code.ordinality
                    ) ||
                    '</div>'
                FROM jsonb_array_elements_text(
                    CASE
                        WHEN jsonb_typeof(message.content -> 'codes') = 'array'
                            THEN message.content -> 'codes'
                        ELSE '[]'::JSONB
                    END
                ) WITH ORDINALITY AS code(value, ordinality)
            ),
            ''
        );

ALTER TABLE messages
    ALTER COLUMN title SET NOT NULL,
    ALTER COLUMN tag SET NOT NULL,
    ALTER COLUMN content_html SET NOT NULL,
    DROP COLUMN message_type,
    DROP COLUMN content;

ALTER TABLE messages
    RENAME COLUMN content_html TO content;

ALTER TABLE messages
    ADD CONSTRAINT messages_title_not_blank CHECK (BTRIM(title) <> ''),
    ADD CONSTRAINT messages_tag_not_blank CHECK (BTRIM(tag) <> ''),
    ADD CONSTRAINT messages_content_not_blank CHECK (BTRIM(content) <> '');

DROP FUNCTION xph_migration_021_html_escape(TEXT);
