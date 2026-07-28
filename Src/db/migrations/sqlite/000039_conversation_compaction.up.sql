CREATE TABLE IF NOT EXISTS conversation_context_settings (
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    auto_compact INTEGER NOT NULL DEFAULT 1 CHECK (auto_compact IN (0, 1)),
    trigger_tokens INTEGER NOT NULL DEFAULT 100000 CHECK (trigger_tokens > 0),
    retain_tokens INTEGER NOT NULL DEFAULT 20000 CHECK (retain_tokens > 0),
    summary_max_tokens INTEGER NOT NULL DEFAULT 4096 CHECK (summary_max_tokens > 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (organization_id, owner_user_id)
);

INSERT OR IGNORE INTO conversation_context_settings (
    organization_id, owner_user_id, auto_compact, trigger_tokens,
    retain_tokens, summary_max_tokens, updated_at
)
SELECT organization_id, user_id, 1, 100000, 20000, 4096,
       strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
FROM memberships
WHERE status = 'active';

CREATE TABLE IF NOT EXISTS conversation_compactions (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    summary TEXT NOT NULL,
    compacted_through_seq INTEGER NOT NULL CHECK (compacted_through_seq > 0),
    retained_from_seq INTEGER NOT NULL CHECK (retained_from_seq > compacted_through_seq),
    source_message_count INTEGER NOT NULL CHECK (source_message_count > 0),
    source_tokens INTEGER NOT NULL CHECK (source_tokens > 0),
    trigger TEXT NOT NULL CHECK (trigger IN ('auto', 'manual')),
    created_at TEXT NOT NULL,
    UNIQUE (conversation_id, compacted_through_seq)
);

CREATE INDEX IF NOT EXISTS idx_conversation_compactions_latest
    ON conversation_compactions(organization_id, conversation_id, compacted_through_seq DESC);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (39, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
