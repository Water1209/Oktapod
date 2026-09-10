DROP INDEX idx_queue_project_claim;
CREATE UNIQUE INDEX idx_queue_conversation_claim ON conversation_message_queue(organization_id, conversation_id) WHERE status = 'claimed' AND kind = 'follow_up';
CREATE INDEX idx_queue_conversation_order ON conversation_message_queue(organization_id, conversation_id, status, created_at, id);
