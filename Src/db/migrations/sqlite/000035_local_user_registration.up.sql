ALTER TABLE users ADD COLUMN username TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_login_username
    ON users(lower(username))
    WHERE username <> '';
