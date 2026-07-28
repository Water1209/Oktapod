-- Session Tree：conversations 即 Session（D2，不建独立 threads 表）。
-- session_entries 承载消息/配置变更/压缩/自定义条目的父子树；
-- conversations 增加 active_leaf_entry_id（当前 Leaf）与 parent_session_id（Fork 来源）。

CREATE TABLE IF NOT EXISTS session_entries (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    -- 根 Entry 的 parent 为 NULL
    parent_entry_id TEXT REFERENCES session_entries(id) ON DELETE CASCADE,
    -- 树内顺序：同一 conversation 下单调递增，与 messages.server_seq 对齐
    seq INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('message', 'config_change', 'compaction', 'custom', 'branch_summary')),
    -- kind = message 时指向 messages.id；compaction 指向 conversation_compactions.id；其余为 ''
    ref_id TEXT NOT NULL DEFAULT '',
    label TEXT NOT NULL DEFAULT '',
    -- 分支摘要（kind = branch_summary 时必填）：离开分支时生成的摘要正文
    summary TEXT NOT NULL DEFAULT '',
    -- 扩展命名空间（kind = custom 时必填），格式 <extension>/<name>
    namespace TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    UNIQUE (conversation_id, seq)
);

-- 父子导航与分支枚举
CREATE INDEX IF NOT EXISTS idx_session_entries_parent
    ON session_entries(conversation_id, parent_entry_id, seq);
CREATE INDEX IF NOT EXISTS idx_session_entries_conversation_seq
    ON session_entries(organization_id, conversation_id, seq);
CREATE INDEX IF NOT EXISTS idx_session_entries_label
    ON session_entries(conversation_id, label) WHERE label <> '';

-- FK 列使用 NULL 而非空串，避免引用不存在的 Entry/Session。
ALTER TABLE conversations ADD COLUMN active_leaf_entry_id TEXT REFERENCES session_entries(id);
ALTER TABLE conversations ADD COLUMN parent_session_id TEXT REFERENCES conversations(id);
ALTER TABLE conversations ADD COLUMN forked_from_entry_id TEXT REFERENCES session_entries(id);
ALTER TABLE conversations ADD COLUMN next_entry_seq INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_conversations_parent_session
    ON conversations(parent_session_id) WHERE parent_session_id <> '';

-- 回填：现有 messages 按 (conversation_id, server_seq) 生成线性 message Entry，
-- parent 指向同会话前一条 Entry；active_leaf 指向每会话最后一条 Entry。
INSERT INTO session_entries (
    id, organization_id, project_id, conversation_id, parent_entry_id,
    seq, kind, ref_id, label, summary, namespace, payload_json, created_at
)
SELECT
    'entry-' || m.id,
    m.organization_id, m.project_id, m.conversation_id,
    (
        SELECT 'entry-' || p.id FROM messages p
        WHERE p.conversation_id = m.conversation_id AND p.server_seq = m.server_seq - 1
    ),
    m.server_seq,
    'message', m.id, '', '', '', '{}', m.created_at
FROM messages m;

UPDATE conversations
SET next_entry_seq = COALESCE((
        SELECT MAX(server_seq) FROM messages WHERE messages.conversation_id = conversations.id
    ), 0) + 1,
    active_leaf_entry_id = (
        SELECT 'entry-' || id FROM messages
        WHERE messages.conversation_id = conversations.id
        ORDER BY server_seq DESC LIMIT 1
    );

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (44, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
