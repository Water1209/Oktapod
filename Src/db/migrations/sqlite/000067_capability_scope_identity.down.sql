-- oktapod:sqlite-only
-- oktapod:foreign-keys-off
PRAGMA foreign_keys = OFF;
PRAGMA defer_foreign_keys = ON;
PRAGMA legacy_alter_table = ON;

ALTER TABLE capabilities RENAME TO capabilities_scoped;

CREATE TABLE capabilities (
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
    scope_type TEXT NOT NULL DEFAULT 'project',
    scope_id TEXT NOT NULL DEFAULT '',
    source_filename TEXT NOT NULL DEFAULT '',
    bundle_storage_key TEXT NOT NULL DEFAULT '',
    install_job_id TEXT NOT NULL DEFAULT '',
    credential_ref TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL DEFAULT 'custom' CHECK (origin IN ('system','preset','custom')),
    preset_key TEXT NOT NULL DEFAULT '',
    dependencies_json TEXT NOT NULL DEFAULT '[]',
    managed_version TEXT NOT NULL DEFAULT '',
    update_state TEXT NOT NULL DEFAULT 'current' CHECK (update_state IN ('current','update_available','degraded')),
    UNIQUE(project_id, kind, name, version)
);

INSERT INTO capabilities SELECT * FROM capabilities_scoped;
DROP TABLE capabilities_scoped;

CREATE INDEX idx_capabilities_project_status ON capabilities(project_id, status, updated_at DESC);
CREATE INDEX idx_capabilities_org_name ON capabilities(organization_id, name);
CREATE INDEX idx_capabilities_effective_scope ON capabilities(organization_id, scope_type, scope_id, status, updated_at);
CREATE UNIQUE INDEX idx_capabilities_scope_version ON capabilities(organization_id, scope_type, scope_id, kind, name, version);
CREATE UNIQUE INDEX idx_capabilities_owner_preset ON capabilities(organization_id, owner_user_id, preset_key) WHERE preset_key <> '';

PRAGMA legacy_alter_table = OFF;
