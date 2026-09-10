-- Fence all starters before restoring project-wide uniqueness. Queued intent
-- is retained, and stale workers cannot consume the reclaimed entries.
UPDATE conversation_message_queue SET status = 'pending', claimed_at = '', claim_version = claim_version + 1 WHERE status = 'claimed' AND kind = 'follow_up';
DROP INDEX idx_queue_conversation_claim;
DROP INDEX idx_queue_conversation_order;
CREATE UNIQUE INDEX idx_queue_project_claim ON conversation_message_queue(organization_id, project_id) WHERE status = 'claimed' AND kind = 'follow_up';
