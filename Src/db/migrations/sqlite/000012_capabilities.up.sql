CREATE TABLE IF NOT EXISTS capabilities (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('skill','plugin','mcp','agent')),
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    manifest_json TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled','disabled','quarantined')),
    risk_level TEXT NOT NULL DEFAULT 'low',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, kind, name, version)
);
CREATE INDEX IF NOT EXISTS idx_capabilities_project_status ON capabilities(project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_capabilities_org_name ON capabilities(organization_id, name);
