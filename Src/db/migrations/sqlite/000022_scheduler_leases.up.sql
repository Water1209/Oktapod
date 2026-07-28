CREATE TABLE IF NOT EXISTS scheduler_leases (
    lease_key TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL,
    lease_until TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
