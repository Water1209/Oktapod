ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN failed_login_attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN locked_until TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_users_login_email
    ON users(lower(email), locked_until);
