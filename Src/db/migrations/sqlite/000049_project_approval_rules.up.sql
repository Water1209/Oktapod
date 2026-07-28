CREATE TABLE IF NOT EXISTS project_approval_rules (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    rule_key TEXT NOT NULL,
    created_from_approval_id TEXT NOT NULL REFERENCES approval_requests(id) ON DELETE CASCADE,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT NOT NULL DEFAULT '',
    revoked_by TEXT NOT NULL DEFAULT '',
    UNIQUE (project_id, tool_name, rule_key)
);

CREATE INDEX IF NOT EXISTS idx_project_approval_rules_match
    ON project_approval_rules(organization_id, project_id, tool_name, rule_key, expires_at, revoked_at);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (49, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
