CREATE TABLE IF NOT EXISTS mcp_connections (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    capability_id TEXT NOT NULL UNIQUE REFERENCES capabilities(id) ON DELETE CASCADE,
    transport TEXT NOT NULL CHECK (transport IN ('stdio','streamable_http')),
    status TEXT NOT NULL CHECK (status IN ('disabled','needs-auth','connected','failed')),
    protocol TEXT NOT NULL DEFAULT '',
    tool_count INTEGER NOT NULL DEFAULT 0,
    tools_json TEXT NOT NULL DEFAULT '[]',
    last_error TEXT NOT NULL DEFAULT '',
    connected_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_connections_project ON mcp_connections(organization_id, project_id, status, updated_at DESC);
