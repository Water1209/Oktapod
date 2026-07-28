ALTER TABLE run_budgets ADD COLUMN reserved_model_calls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE run_budgets ADD COLUMN reserved_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE steps ADD COLUMN budget_reserved_tokens INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_run_budgets_status ON run_budgets(status);
