CREATE TABLE ai_governance_settings (
    organization_id TEXT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    reviewer_provider_id TEXT NOT NULL DEFAULT '',
    reviewer_model_id TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    timeout_seconds INTEGER NOT NULL DEFAULT 30 CHECK (timeout_seconds BETWEEN 5 AND 120),
    fail_closed INTEGER NOT NULL DEFAULT 1 CHECK (fail_closed = 1),
    policy_version INTEGER NOT NULL DEFAULT 1 CHECK (policy_version > 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
