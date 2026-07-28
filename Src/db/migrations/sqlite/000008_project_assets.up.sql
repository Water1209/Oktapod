CREATE TABLE IF NOT EXISTS assets (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    parent_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('file', 'folder')),
    media_type TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    visibility TEXT NOT NULL CHECK (visibility IN ('private', 'selected', 'project')),
    source_type TEXT NOT NULL CHECK (source_type IN ('upload', 'artifact')),
    source_artifact_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((kind = 'folder' AND content_hash = '' AND size_bytes = 0) OR (kind = 'file' AND content_hash <> ''))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_assets_unique_name
    ON assets(project_id, parent_id, name);

CREATE INDEX IF NOT EXISTS idx_assets_scope_parent
    ON assets(organization_id, project_id, parent_id, visibility, owner_user_id, kind, name, id);

CREATE INDEX IF NOT EXISTS idx_assets_scope_search
    ON assets(organization_id, project_id, visibility, owner_user_id, name, id);

CREATE INDEX IF NOT EXISTS idx_assets_content_hash
    ON assets(content_hash)
    WHERE content_hash <> '';

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (8, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
