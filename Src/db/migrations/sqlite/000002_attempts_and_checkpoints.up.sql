CREATE TABLE IF NOT EXISTS step_attempts (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL,
    attempt_kind TEXT NOT NULL CHECK (attempt_kind IN ('initial', 'recovery')),
    owner_id TEXT NOT NULL,
    fence_token INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'blocked', 'canceled', 'lease_lost')),
    error_code TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL DEFAULT '',
    UNIQUE (step_id, attempt_number)
);

CREATE INDEX IF NOT EXISTS idx_step_attempts_run
    ON step_attempts(organization_id, project_id, run_id, step_id, attempt_number);

CREATE TABLE IF NOT EXISTS checkpoints (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    attempt_id TEXT NOT NULL REFERENCES step_attempts(id),
    checkpoint_kind TEXT NOT NULL,
    state_json TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_checkpoints_latest
    ON checkpoints(organization_id, project_id, run_id, step_id, created_at DESC, id DESC);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (2, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
