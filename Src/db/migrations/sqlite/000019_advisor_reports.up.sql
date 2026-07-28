CREATE TABLE IF NOT EXISTS advisor_reports (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    requested_by TEXT NOT NULL REFERENCES users(id),
    trigger_reason TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('preview', 'completed', 'rejected')),
    summary TEXT NOT NULL,
    evidence_json TEXT NOT NULL DEFAULT '[]',
    estimated_model_calls INTEGER NOT NULL DEFAULT 0,
    estimated_tokens INTEGER NOT NULL DEFAULT 0,
    estimated_cost_decimal TEXT NOT NULL DEFAULT '0',
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_advisor_reports_run
    ON advisor_reports(organization_id, project_id, run_id, created_at DESC);
