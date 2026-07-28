CREATE TABLE IF NOT EXISTS agent_definitions (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL,
    name TEXT NOT NULL,
    version TEXT NOT NULL,
    instructions TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    fallback_model TEXT NOT NULL DEFAULT '',
    config_json TEXT NOT NULL DEFAULT '{}',
    status TEXT NOT NULL DEFAULT 'enabled' CHECK (status IN ('enabled','disabled','quarantined')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(project_id, name, version)
);
CREATE INDEX IF NOT EXISTS idx_agents_project_status ON agent_definitions(project_id, status, updated_at DESC);
