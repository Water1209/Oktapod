DROP INDEX IF EXISTS idx_queue_message_id;
ALTER TABLE conversation_message_queue DROP COLUMN message_id;
DROP TABLE IF EXISTS message_presentations;
