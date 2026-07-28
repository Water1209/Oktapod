CREATE TABLE IF NOT EXISTS memories (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    title TEXT NOT NULL,
    content TEXT NOT NULL,
    source_hash TEXT NOT NULL,
    visibility TEXT NOT NULL CHECK (visibility IN ('private','selected','project')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project_time ON memories(project_id, visibility, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_memories_source_hash ON memories(project_id, source_hash);
