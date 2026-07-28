DROP INDEX IF EXISTS idx_conversation_approval_rules_match;
DROP TABLE IF EXISTS conversation_approval_rules;
DELETE FROM schema_versions WHERE version = 41;
