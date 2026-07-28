-- Repair migration for the published 000044 Session Tree baseline.
-- entry_seq is an independent tree sequence; messages.server_seq remains a
-- message-stream cursor and is never reused as the tree allocator.
ALTER TABLE session_entries ADD COLUMN entry_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE session_entries ADD COLUMN schema_version INTEGER NOT NULL DEFAULT 2;
ALTER TABLE session_entries ADD COLUMN context_injection TEXT NOT NULL DEFAULT 'default'
    CHECK (context_injection IN ('default', 'include', 'exclude', 'summary_only'));

UPDATE session_entries SET entry_seq = seq WHERE entry_seq = 0;

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_entries_entry_seq
    ON session_entries(conversation_id, entry_seq);

CREATE TABLE IF NOT EXISTS session_tree_events (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    entry_id TEXT NOT NULL DEFAULT '',
    operation TEXT NOT NULL CHECK (operation IN ('navigate', 'fork', 'clone', 'label', 'branch_summary')),
    actor_user_id TEXT NOT NULL REFERENCES users(id),
    payload_json TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_session_tree_events_conversation
    ON session_tree_events(conversation_id, created_at, id);

ALTER TABLE conversation_compactions RENAME TO conversation_compactions_v1;

CREATE TABLE conversation_compactions (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    summary TEXT NOT NULL,
    compacted_through_seq INTEGER NOT NULL CHECK (compacted_through_seq >= 0),
    retained_from_seq INTEGER NOT NULL CHECK (retained_from_seq > compacted_through_seq),
    source_message_count INTEGER NOT NULL CHECK (source_message_count > 0),
    source_tokens INTEGER NOT NULL CHECK (source_tokens > 0),
    trigger TEXT NOT NULL CHECK (trigger IN ('auto', 'manual')),
    split_message_id TEXT NOT NULL DEFAULT '',
    split_prefix_runes INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

INSERT INTO conversation_compactions (
    id, organization_id, project_id, conversation_id, owner_user_id,
    summary, compacted_through_seq, retained_from_seq,
    source_message_count, source_tokens, trigger,
    split_message_id, split_prefix_runes, created_at
)
SELECT
    id, organization_id, project_id, conversation_id, owner_user_id,
    summary, compacted_through_seq, retained_from_seq,
    source_message_count, source_tokens, trigger,
    '', 0, created_at
FROM conversation_compactions_v1;

DROP TABLE conversation_compactions_v1;

CREATE INDEX IF NOT EXISTS idx_conversation_compactions_latest
    ON conversation_compactions(organization_id, conversation_id, created_at DESC, id DESC);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (47, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
