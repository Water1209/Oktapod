CREATE TABLE secret_vaults (
    organization_id TEXT PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('pending', 'active')),
    algorithm TEXT NOT NULL CHECK (algorithm = 'aes-256-gcm'),
    key_version INTEGER NOT NULL CHECK (key_version > 0),
    local_wrapped_dek TEXT NOT NULL,
    local_nonce TEXT NOT NULL,
    recovery_wrapped_dek TEXT NOT NULL,
    recovery_nonce TEXT NOT NULL,
    recovery_salt TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE secret_records (
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    secret_ref TEXT NOT NULL,
    secret_kind TEXT NOT NULL CHECK (secret_kind IN ('provider', 'mcp', 'mcp_oauth')),
    key_version INTEGER NOT NULL CHECK (key_version > 0),
    nonce TEXT NOT NULL,
    ciphertext TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (organization_id, secret_ref)
);

CREATE INDEX secret_records_org_kind
    ON secret_records(organization_id, secret_kind, updated_at DESC);
