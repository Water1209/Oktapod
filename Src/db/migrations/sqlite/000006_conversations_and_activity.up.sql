ALTER TABLE tasks ADD COLUMN conversation_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN task_id TEXT NOT NULL DEFAULT '';

-- Existing Personal tasks become one-conversation histories without losing data.
-- Reusing the task UUID is safe because identifiers are scoped by table.
INSERT OR IGNORE INTO conversations (
    id, organization_id, project_id, owner_user_id, title, visibility,
    next_message_seq, created_at, updated_at
)
SELECT id, organization_id, project_id, owner_user_id, title, visibility,
       1, created_at, updated_at
FROM tasks
WHERE conversation_id = '';

INSERT OR IGNORE INTO messages (
    id, organization_id, project_id, conversation_id, owner_user_id,
    server_seq, role, visibility, content, created_at, task_id
)
SELECT id, organization_id, project_id, id, owner_user_id,
       1, 'user', visibility, goal, created_at, id
FROM tasks
WHERE conversation_id = '';

UPDATE tasks SET conversation_id = id WHERE conversation_id = '';

CREATE INDEX IF NOT EXISTS idx_conversations_scope_updated
    ON conversations(organization_id, project_id, visibility, owner_user_id, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_messages_conversation_cursor
    ON messages(organization_id, project_id, conversation_id, server_seq);

CREATE INDEX IF NOT EXISTS idx_tasks_conversation
    ON tasks(conversation_id, updated_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_run_events_project_activity
    ON run_events(organization_id, project_id, occurred_at DESC, id DESC);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (6, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
