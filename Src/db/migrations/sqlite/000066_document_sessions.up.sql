CREATE TABLE document_sessions (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    open_key TEXT NOT NULL,
    source_ref TEXT NOT NULL DEFAULT '',
    source_content_hash TEXT NOT NULL,
    format TEXT NOT NULL CHECK (format IN ('pptx','pdf','docx','xlsx')),
    mode TEXT NOT NULL CHECK (mode IN ('structure','full','create')),
    runtime_version TEXT NOT NULL,
    extraction_profile_hash TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    revision_hash TEXT NOT NULL,
    working_handle TEXT NOT NULL,
    structure_json TEXT NOT NULL,
    warnings_json TEXT NOT NULL DEFAULT '[]',
    rendered_revision INTEGER NOT NULL DEFAULT 0,
    render_hash TEXT NOT NULL DEFAULT '',
    render_manifest_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('ready','degraded','failed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (run_id, open_key)
);

CREATE INDEX idx_document_sessions_run
    ON document_sessions(organization_id, owner_user_id, run_id, updated_at DESC);

CREATE TABLE document_revisions (
    document_id TEXT NOT NULL REFERENCES document_sessions(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK (revision >= 1),
    revision_hash TEXT NOT NULL,
    operation_hash TEXT NOT NULL,
    working_handle TEXT NOT NULL,
    structure_json TEXT NOT NULL,
    warnings_json TEXT NOT NULL DEFAULT '[]',
    created_at TEXT NOT NULL,
    PRIMARY KEY (document_id, revision),
    UNIQUE (document_id, operation_hash)
);

CREATE INDEX idx_document_revisions_hash
    ON document_revisions(document_id, revision_hash);
