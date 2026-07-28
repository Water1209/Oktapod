-- 回滚 Session Tree。SQLite 无法 DROP COLUMN，重建 conversations。

ALTER TABLE conversations RENAME TO conversations_with_tree;

CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    title TEXT NOT NULL DEFAULT '',
    visibility TEXT NOT NULL CHECK (visibility IN ('private', 'selected', 'project')),
    next_message_seq INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO conversations (
    id, organization_id, project_id, owner_user_id, title, visibility,
    next_message_seq, created_at, updated_at
)
SELECT id, organization_id, project_id, owner_user_id, title, visibility,
       next_message_seq, created_at, updated_at
FROM conversations_with_tree;

DROP TABLE conversations_with_tree;
DROP INDEX IF EXISTS idx_session_entries_parent;
DROP INDEX IF EXISTS idx_session_entries_conversation_seq;
DROP INDEX IF EXISTS idx_session_entries_label;
DROP TABLE IF EXISTS session_entries;

DELETE FROM schema_versions WHERE version = 44;
