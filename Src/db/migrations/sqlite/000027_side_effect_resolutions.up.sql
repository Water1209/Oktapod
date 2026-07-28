CREATE TABLE IF NOT EXISTS side_effect_resolutions (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    receipt_id TEXT NOT NULL REFERENCES side_effect_receipts(id) ON DELETE CASCADE,
    decision TEXT NOT NULL CHECK (decision IN ('confirm_applied', 'confirm_not_applied', 'abandon')),
    note TEXT NOT NULL DEFAULT '',
    actor_user_id TEXT NOT NULL REFERENCES users(id),
    created_at TEXT NOT NULL,
    UNIQUE (receipt_id, decision)
);

CREATE INDEX IF NOT EXISTS idx_side_effect_resolutions_scope
    ON side_effect_resolutions(organization_id, project_id, created_at DESC, id);
