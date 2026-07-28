CREATE TABLE provider_instances (
    provider_id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    protocol_family TEXT NOT NULL CHECK (protocol_family IN ('openai_compatible', 'anthropic', 'gemini')),
    endpoint TEXT NOT NULL,
    credential_ref TEXT NOT NULL,
    default_model_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'error')),
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (organization_id, display_name)
);

CREATE UNIQUE INDEX provider_instances_one_default
ON provider_instances(organization_id)
WHERE is_default = 1;

CREATE INDEX provider_instances_org_updated
ON provider_instances(organization_id, updated_at DESC);

CREATE TABLE model_catalog_entries (
    provider_id TEXT NOT NULL REFERENCES provider_instances(provider_id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    capabilities_json TEXT NOT NULL DEFAULT '{}',
    context_window INTEGER NOT NULL DEFAULT 0,
    max_output INTEGER NOT NULL DEFAULT 0,
    thinking_map_json TEXT NOT NULL DEFAULT '{}',
    pricing_json TEXT NOT NULL DEFAULT '{}',
    fetched_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    etag TEXT NOT NULL DEFAULT '',
    last_error TEXT NOT NULL DEFAULT '',
    catalog_version TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider_id, model_id)
);

INSERT INTO provider_instances (
    provider_id, organization_id, display_name, protocol_family, endpoint,
    credential_ref, default_model_id, status, is_default, created_at, updated_at
)
SELECT
    'legacy-default-' || organization_id,
    organization_id,
    'Default Provider',
    provider_kind,
    endpoint,
    secret_ref,
    default_model,
    'active',
    1,
    created_at,
    updated_at
FROM model_provider_configs;

ALTER TABLE runs ADD COLUMN requested_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN requested_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN effective_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN effective_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN thinking_level TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN model_fallback_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN model_catalog_version TEXT NOT NULL DEFAULT '';
