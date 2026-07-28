ALTER TABLE approval_requests ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_approvals_expiry
    ON approval_requests(organization_id, status, expires_at, id);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (34, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
