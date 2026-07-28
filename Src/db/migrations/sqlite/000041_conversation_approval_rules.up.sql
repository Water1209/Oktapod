CREATE TABLE IF NOT EXISTS conversation_approval_rules (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    rule_key TEXT NOT NULL,
    created_from_approval_id TEXT NOT NULL REFERENCES approval_requests(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    UNIQUE (conversation_id, tool_name, rule_key)
);

CREATE INDEX IF NOT EXISTS idx_conversation_approval_rules_match
    ON conversation_approval_rules(organization_id, project_id, conversation_id, tool_name, expires_at);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (41, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
