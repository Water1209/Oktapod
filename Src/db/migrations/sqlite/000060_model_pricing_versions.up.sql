CREATE TABLE model_pricing_versions (
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL REFERENCES provider_instances(provider_id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    currency TEXT NOT NULL DEFAULT 'USD',
    input_per_1k TEXT NOT NULL,
    output_per_1k TEXT NOT NULL,
    effective_at TEXT NOT NULL,
    version INTEGER NOT NULL CHECK (version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (organization_id, provider_id, model_id, effective_at, version)
);

CREATE INDEX model_pricing_versions_lookup
    ON model_pricing_versions(organization_id, provider_id, model_id, effective_at DESC, version DESC);
