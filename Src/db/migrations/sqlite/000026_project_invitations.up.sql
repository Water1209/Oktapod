CREATE TABLE IF NOT EXISTS project_invitations (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    invited_by TEXT NOT NULL REFERENCES users(id),
    email TEXT NOT NULL,
    display_name TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('owner', 'member', 'approver')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'accepted', 'revoked', 'expired')),
    token_hash TEXT NOT NULL UNIQUE,
    accepted_user_id TEXT REFERENCES users(id),
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_project_invitations_project_status
    ON project_invitations(project_id, status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_invitations_token
    ON project_invitations(token_hash, status, expires_at);
