CREATE TABLE model_provider_configs (
    organization_id TEXT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    provider_kind TEXT NOT NULL DEFAULT 'openai_compatible' CHECK (provider_kind = 'openai_compatible'),
    endpoint TEXT NOT NULL,
    default_model TEXT NOT NULL,
    secret_ref TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
