CREATE TABLE IF NOT EXISTS capability_reviews (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    capability_id TEXT NOT NULL REFERENCES capabilities(id) ON DELETE CASCADE,
    reviewer_user_id TEXT NOT NULL REFERENCES users(id),
    requested_hash TEXT NOT NULL,
    decision TEXT NOT NULL CHECK (decision IN ('approved', 'rejected')),
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    UNIQUE (capability_id, requested_hash)
);

CREATE INDEX IF NOT EXISTS idx_capability_reviews_project_created
    ON capability_reviews(project_id, created_at DESC);
