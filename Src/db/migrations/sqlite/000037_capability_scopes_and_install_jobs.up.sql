ALTER TABLE capabilities ADD COLUMN scope_type TEXT NOT NULL DEFAULT 'project';
ALTER TABLE capabilities ADD COLUMN scope_id TEXT NOT NULL DEFAULT '';
ALTER TABLE capabilities ADD COLUMN source_filename TEXT NOT NULL DEFAULT '';
ALTER TABLE capabilities ADD COLUMN bundle_storage_key TEXT NOT NULL DEFAULT '';
ALTER TABLE capabilities ADD COLUMN install_job_id TEXT NOT NULL DEFAULT '';

UPDATE capabilities SET scope_id = project_id WHERE scope_id = '';

CREATE TABLE IF NOT EXISTS capability_install_jobs (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    actor_user_id TEXT NOT NULL REFERENCES users(id),
    scope_type TEXT NOT NULL CHECK (scope_type IN ('global','project')),
    scope_id TEXT NOT NULL,
    source_filename TEXT NOT NULL DEFAULT '',
    bundle_hash TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('received','scanned','awaiting_approval','succeeded','rejected','failed')),
    capability_id TEXT,
    error_code TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_capabilities_effective_scope
    ON capabilities(organization_id, scope_type, scope_id, status, updated_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_capabilities_scope_version
    ON capabilities(organization_id, scope_type, scope_id, kind, name, version);

CREATE INDEX IF NOT EXISTS idx_capability_install_jobs_scope
    ON capability_install_jobs(organization_id, scope_type, scope_id, updated_at);
