-- A project can have at most one deterministic daily summary per UTC date.
-- The partial unique index also makes concurrent Personal/Team runners safe.
CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_daily_summary_unique
    ON memories(project_id, title)
    WHERE kind = 'daily_summary';
