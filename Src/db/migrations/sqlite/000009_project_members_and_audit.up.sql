ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX users_email_unique
    ON users(lower(email))
    WHERE email <> '';

CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL DEFAULT '',
    actor_user_id TEXT NOT NULL REFERENCES users(id),
    action TEXT NOT NULL,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('succeeded', 'denied', 'failed')),
    detail_json TEXT NOT NULL DEFAULT '{}',
    correlation_id TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL
);

CREATE INDEX audit_events_scope_time
    ON audit_events(organization_id, project_id, occurred_at DESC, id DESC);
