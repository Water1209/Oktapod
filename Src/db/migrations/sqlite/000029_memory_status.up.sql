ALTER TABLE memories ADD COLUMN status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','inactive'));
CREATE INDEX IF NOT EXISTS idx_memories_project_status_time ON memories(project_id, status, visibility, updated_at DESC, id);
