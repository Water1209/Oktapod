ALTER TABLE conversation_approval_rules ADD COLUMN rule_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE conversation_approval_rules ADD COLUMN match_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE conversation_approval_rules ADD COLUMN match_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_approval_rules ADD COLUMN risk_level TEXT NOT NULL DEFAULT '';

ALTER TABLE project_approval_rules ADD COLUMN rule_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE project_approval_rules ADD COLUMN match_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE project_approval_rules ADD COLUMN match_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE project_approval_rules ADD COLUMN risk_level TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_conversation_approval_rules_v2
    ON conversation_approval_rules(organization_id, project_id, conversation_id, tool_name, rule_version, match_hash, expires_at);

CREATE INDEX IF NOT EXISTS idx_project_approval_rules_v2
    ON project_approval_rules(organization_id, project_id, tool_name, rule_version, match_hash, expires_at, revoked_at);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (52, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
