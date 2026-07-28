CREATE TABLE IF NOT EXISTS accepted_outcomes (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    accepted_by TEXT NOT NULL REFERENCES users(id),
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (run_id)
);

CREATE INDEX IF NOT EXISTS idx_accepted_outcomes_project
    ON accepted_outcomes(organization_id, project_id, created_at DESC, id DESC);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (3, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
