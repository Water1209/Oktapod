ALTER TABLE memories ADD COLUMN source_refs_json TEXT NOT NULL DEFAULT '[]';
CREATE INDEX IF NOT EXISTS idx_memories_source_refs ON memories(project_id, source_hash);
