CREATE TABLE IF NOT EXISTS run_budgets (
    run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    max_model_calls INTEGER NOT NULL DEFAULT 20 CHECK (max_model_calls > 0),
    max_tokens INTEGER NOT NULL DEFAULT 100000 CHECK (max_tokens > 0),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','exhausted','approved_exception')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
