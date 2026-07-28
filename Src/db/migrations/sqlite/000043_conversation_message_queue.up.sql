CREATE TABLE conversation_message_queue (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL,
    active_task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    input_json TEXT NOT NULL DEFAULT '{}',
    kind TEXT NOT NULL DEFAULT 'follow_up' CHECK (kind IN ('follow_up', 'steering')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'claimed', 'consumed', 'canceled')),
    created_at TEXT NOT NULL,
    claimed_at TEXT NOT NULL DEFAULT '',
    consumed_at TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE CASCADE
);

CREATE INDEX idx_queue_conversation_status ON conversation_message_queue(conversation_id, status);
CREATE INDEX idx_queue_task_status ON conversation_message_queue(active_task_id, status);
