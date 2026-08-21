ALTER TABLE side_effect_receipts ADD COLUMN network_class TEXT NOT NULL DEFAULT '';
ALTER TABLE side_effect_receipts ADD COLUMN matched_rule_id TEXT NOT NULL DEFAULT '';
ALTER TABLE side_effect_receipts ADD COLUMN policy_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE side_effect_receipts ADD COLUMN authorization_id TEXT NOT NULL DEFAULT '';
