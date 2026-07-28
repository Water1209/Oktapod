CREATE TABLE IF NOT EXISTS tool_calls (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    tool_name TEXT NOT NULL,
    arguments_json TEXT NOT NULL,
    arguments_hash TEXT NOT NULL,
    risk_level TEXT NOT NULL CHECK (risk_level IN ('L0', 'L1', 'L2', 'L3')),
    policy_decision TEXT NOT NULL CHECK (policy_decision IN ('allow', 'ask', 'deny')),
    status TEXT NOT NULL CHECK (status IN ('planned', 'waiting_approval', 'queued', 'running', 'succeeded', 'failed', 'rejected', 'blocked')),
    error_code TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (run_id, position)
);

CREATE INDEX IF NOT EXISTS idx_tool_calls_step
    ON tool_calls(organization_id, project_id, run_id, step_id, position);

CREATE TABLE IF NOT EXISTS approval_requests (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id) ON DELETE CASCADE,
    tool_name TEXT NOT NULL,
    risk_level TEXT NOT NULL CHECK (risk_level IN ('L0', 'L1', 'L2', 'L3')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    reason TEXT NOT NULL,
    frozen_params_json TEXT NOT NULL,
    input_hash TEXT NOT NULL,
    requested_by TEXT NOT NULL,
    decided_by TEXT NOT NULL DEFAULT '',
    decision_note TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    decided_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_approvals_inbox
    ON approval_requests(organization_id, project_id, status, requested_at, id);

CREATE TABLE IF NOT EXISTS side_effect_receipts (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL UNIQUE REFERENCES tool_calls(id) ON DELETE CASCADE,
    invocation_id TEXT NOT NULL UNIQUE,
    input_hash TEXT NOT NULL,
    idempotent INTEGER NOT NULL CHECK (idempotent IN (0, 1)),
    status TEXT NOT NULL CHECK (status IN ('started', 'succeeded', 'failed', 'unknown')),
    exit_code INTEGER NOT NULL DEFAULT -1,
    output_json TEXT NOT NULL DEFAULT '{}',
    error_code TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_receipts_run
    ON side_effect_receipts(organization_id, project_id, run_id, started_at, id);

CREATE TABLE IF NOT EXISTS validation_results (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    tool_call_id TEXT NOT NULL REFERENCES tool_calls(id) ON DELETE CASCADE,
    validator TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('passed', 'failed')),
    input_hash TEXT NOT NULL,
    details_json TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_validation_results_run
    ON validation_results(organization_id, project_id, run_id, created_at, id);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (5, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
