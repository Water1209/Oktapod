CREATE TABLE IF NOT EXISTS tool_batches (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    source_step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    plan_iteration INTEGER NOT NULL,
    execution_mode TEXT NOT NULL CHECK (execution_mode IN ('parallel', 'sequential')),
    status TEXT NOT NULL CHECK (status IN ('planned', 'waiting_approval', 'running', 'succeeded', 'failed', 'blocked', 'canceled')),
    tool_count INTEGER NOT NULL,
    continuation_claimed INTEGER NOT NULL DEFAULT 0 CHECK (continuation_claimed IN (0, 1)),
    continuation_step_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (run_id, source_step_id)
);

ALTER TABLE steps ADD COLUMN tool_batch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN batch_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tool_calls ADD COLUMN tool_batch_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN batch_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tool_calls ADD COLUMN completed_at TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_steps_tool_batch
    ON steps(tool_batch_id, batch_index, status);

CREATE INDEX IF NOT EXISTS idx_tool_calls_batch
    ON tool_calls(tool_batch_id, batch_index, status);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (46, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
