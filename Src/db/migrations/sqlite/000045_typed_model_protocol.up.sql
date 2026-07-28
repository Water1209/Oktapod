ALTER TABLE model_calls ADD COLUMN purpose TEXT NOT NULL DEFAULT 'execution';
ALTER TABLE model_calls ADD COLUMN through_entry_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN requested_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN requested_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN effective_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN effective_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE model_calls ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_calls ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_calls ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_calls ADD COLUMN usage_semantics_version TEXT NOT NULL DEFAULT 'v1';

ALTER TABLE conversation_model_calls ADD COLUMN through_entry_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_model_calls ADD COLUMN requested_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_model_calls ADD COLUMN requested_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_model_calls ADD COLUMN effective_provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_model_calls ADD COLUMN effective_model_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_model_calls ADD COLUMN stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_model_calls ADD COLUMN context_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_model_calls ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_model_calls ADD COLUMN cache_write_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE conversation_model_calls ADD COLUMN usage_semantics_version TEXT NOT NULL DEFAULT 'v1';

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (45, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
