CREATE TABLE conversation_message_queue_v2 (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL,
    project_id TEXT NOT NULL,
    conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    active_task_id TEXT REFERENCES tasks(id) ON DELETE CASCADE,
    content TEXT NOT NULL,
    input_json TEXT NOT NULL DEFAULT '{}',
    kind TEXT NOT NULL DEFAULT 'follow_up' CHECK (kind IN ('follow_up', 'steering')),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'claimed', 'consumed', 'canceled')),
    created_at TEXT NOT NULL,
    claimed_at TEXT NOT NULL DEFAULT '',
    consumed_at TEXT NOT NULL DEFAULT '',
    message_id TEXT NOT NULL DEFAULT '',
    claim_version INTEGER NOT NULL DEFAULT 0
);
INSERT INTO conversation_message_queue_v2
    (id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id)
SELECT id, organization_id, project_id, conversation_id, NULLIF(active_task_id, ''), content, input_json, kind,
    CASE WHEN status = 'claimed' THEN 'pending' ELSE status END, created_at,
    CASE WHEN status = 'claimed' THEN '' ELSE claimed_at END, consumed_at, message_id
FROM conversation_message_queue;
DROP TABLE conversation_message_queue;
ALTER TABLE conversation_message_queue_v2 RENAME TO conversation_message_queue;
CREATE INDEX idx_queue_conversation_status ON conversation_message_queue(conversation_id, status);
CREATE INDEX idx_queue_task_status ON conversation_message_queue(active_task_id, status);
CREATE INDEX idx_queue_message_id ON conversation_message_queue(message_id) WHERE message_id <> '';
CREATE INDEX idx_queue_project_order ON conversation_message_queue(organization_id, project_id, status, created_at, id);
CREATE UNIQUE INDEX idx_queue_project_claim ON conversation_message_queue(organization_id, project_id) WHERE status = 'claimed' AND kind = 'follow_up';
