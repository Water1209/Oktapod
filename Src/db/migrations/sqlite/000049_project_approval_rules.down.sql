DROP INDEX IF EXISTS idx_project_approval_rules_match;
DROP TABLE IF EXISTS project_approval_rules;
DELETE FROM schema_versions WHERE version = 49;
