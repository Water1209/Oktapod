DROP INDEX IF EXISTS idx_session_entries_entry_seq;
DROP INDEX IF EXISTS idx_session_tree_events_conversation;
DROP TABLE IF EXISTS session_tree_events;
DELETE FROM schema_versions WHERE version = 47;
