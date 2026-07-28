CREATE TABLE IF NOT EXISTS blobs (
    content_hash TEXT PRIMARY KEY,
    storage_key TEXT NOT NULL UNIQUE,
    size_bytes INTEGER NOT NULL,
    media_type TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS artifacts (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL REFERENCES steps(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    media_type TEXT NOT NULL,
    content_hash TEXT NOT NULL REFERENCES blobs(content_hash),
    size_bytes INTEGER NOT NULL,
    visibility TEXT NOT NULL CHECK (visibility IN ('private', 'selected', 'project')),
    source_type TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_artifacts_run
    ON artifacts(organization_id, project_id, run_id, created_at, id);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (4, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
