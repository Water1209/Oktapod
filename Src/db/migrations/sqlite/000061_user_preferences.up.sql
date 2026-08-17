CREATE TABLE user_preferences (
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    theme TEXT NOT NULL DEFAULT 'system' CHECK (theme IN ('system', 'light', 'dark')),
    density TEXT NOT NULL DEFAULT 'comfortable' CHECK (density IN ('compact', 'comfortable')),
    default_permission TEXT NOT NULL DEFAULT 'ask' CHECK (default_permission IN ('ask', 'auto_review')),
    recent_provider_id TEXT NOT NULL DEFAULT '',
    recent_model_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (organization_id, user_id)
);

DROP TABLE IF EXISTS model_provider_configs;
