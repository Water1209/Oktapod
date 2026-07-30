ALTER TABLE mcp_connections ADD COLUMN schema_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE mcp_connections ADD COLUMN config_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE mcp_connections ADD COLUMN health_state TEXT NOT NULL DEFAULT 'unknown';

CREATE TABLE IF NOT EXISTS run_tool_snapshots (
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL DEFAULT 1,
    tool_identity TEXT NOT NULL,
    invocation_name TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('native','mcp','builtin')),
    capability_id TEXT NOT NULL DEFAULT '',
    schema_hash TEXT NOT NULL,
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    selection_reason TEXT NOT NULL DEFAULT '',
    policy_version TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (run_id, revision, tool_identity)
);
CREATE INDEX IF NOT EXISTS idx_run_tool_snapshots_run
    ON run_tool_snapshots(run_id, revision, invocation_name);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (54, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
