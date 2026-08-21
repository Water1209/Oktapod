ALTER TABLE approval_requests ADD COLUMN approval_scope TEXT NOT NULL DEFAULT 'once';
ALTER TABLE approval_requests ADD COLUMN target_origin TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_requests ADD COLUMN matched_rule_id TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_requests ADD COLUMN policy_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE approval_requests ADD COLUMN approval_grant_key TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS run_approval_rules (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    grant_type TEXT NOT NULL,
    grant_key TEXT NOT NULL,
    policy_hash TEXT NOT NULL,
    canonical_origin TEXT NOT NULL,
    created_from_approval_id TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    UNIQUE (run_id, tool_name, grant_type, grant_key, policy_hash),
    FOREIGN KEY (organization_id) REFERENCES organizations(id),
    FOREIGN KEY (project_id) REFERENCES projects(id),
    FOREIGN KEY (run_id) REFERENCES runs(id),
    FOREIGN KEY (created_from_approval_id) REFERENCES approval_requests(id)
);

CREATE INDEX IF NOT EXISTS idx_run_approval_rules_match
    ON run_approval_rules(organization_id, project_id, run_id, tool_name, grant_type, grant_key, policy_hash, expires_at);
