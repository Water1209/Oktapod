DROP INDEX IF EXISTS idx_tool_calls_batch;
DROP INDEX IF EXISTS idx_steps_tool_batch;
DROP TABLE IF EXISTS tool_batches;
DELETE FROM schema_versions WHERE version = 46;
