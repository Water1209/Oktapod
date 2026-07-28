CREATE TABLE run_budgets_unlimited (
    run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    max_model_calls INTEGER NOT NULL DEFAULT 0 CHECK (max_model_calls >= 0),
    max_tokens INTEGER NOT NULL DEFAULT 0 CHECK (max_tokens >= 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','exhausted','approved_exception')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    reserved_model_calls INTEGER NOT NULL DEFAULT 0,
    reserved_tokens INTEGER NOT NULL DEFAULT 0
);

INSERT INTO run_budgets_unlimited (
    run_id, max_model_calls, max_tokens, status, created_at, updated_at,
    reserved_model_calls, reserved_tokens
)
SELECT run_id, max_model_calls, max_tokens, status, created_at, updated_at,
       reserved_model_calls, reserved_tokens
FROM run_budgets;

DROP TABLE run_budgets;
ALTER TABLE run_budgets_unlimited RENAME TO run_budgets;
CREATE INDEX IF NOT EXISTS idx_run_budgets_status ON run_budgets(status);
