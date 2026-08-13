CREATE TABLE message_presentations (
    message_id TEXT PRIMARY KEY REFERENCES messages(id) ON DELETE CASCADE,
    presentation_json TEXT NOT NULL DEFAULT '{}'
);

ALTER TABLE conversation_message_queue
    ADD COLUMN message_id TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_queue_message_id
    ON conversation_message_queue(message_id)
    WHERE message_id <> '';
