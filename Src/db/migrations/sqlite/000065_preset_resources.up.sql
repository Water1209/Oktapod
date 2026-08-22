ALTER TABLE capabilities ADD COLUMN origin TEXT NOT NULL DEFAULT 'custom';
ALTER TABLE capabilities ADD COLUMN preset_key TEXT NOT NULL DEFAULT '';
ALTER TABLE capabilities ADD COLUMN dependencies_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE capabilities ADD COLUMN managed_version TEXT NOT NULL DEFAULT '';
ALTER TABLE capabilities ADD COLUMN update_state TEXT NOT NULL DEFAULT 'current';

CREATE UNIQUE INDEX idx_capabilities_owner_preset
    ON capabilities(organization_id, owner_user_id, preset_key)
    WHERE preset_key <> '';

CREATE TABLE preset_resource_states (
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    preset_key TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('installed','deleted')),
    capability_id TEXT NOT NULL DEFAULT '',
    managed_version TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (organization_id, owner_user_id, preset_key)
);

CREATE INDEX idx_preset_resource_states_owner
    ON preset_resource_states(organization_id, owner_user_id, state, updated_at DESC);
