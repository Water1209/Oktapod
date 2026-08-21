DROP INDEX IF EXISTS idx_run_approval_rules_match;
DROP TABLE IF EXISTS run_approval_rules;
ALTER TABLE approval_requests DROP COLUMN approval_grant_key;
ALTER TABLE approval_requests DROP COLUMN policy_hash;
ALTER TABLE approval_requests DROP COLUMN matched_rule_id;
ALTER TABLE approval_requests DROP COLUMN target_origin;
ALTER TABLE approval_requests DROP COLUMN approval_scope;
