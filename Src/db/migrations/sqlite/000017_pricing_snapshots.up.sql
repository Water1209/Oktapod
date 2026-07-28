ALTER TABLE model_calls ADD COLUMN pricing_snapshot_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE usage_ledger ADD COLUMN pricing_snapshot_json TEXT NOT NULL DEFAULT '{}';
