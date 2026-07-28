CREATE TABLE project_runtime_settings (
    project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
    max_model_calls INTEGER NOT NULL DEFAULT 32 CHECK (max_model_calls >= 0 AND max_model_calls <= 1000),
    max_tokens INTEGER NOT NULL DEFAULT 200000 CHECK (max_tokens >= 0 AND max_tokens <= 10000000),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((max_model_calls = 0 AND max_tokens = 0) OR (max_model_calls > 0 AND max_tokens > 0))
);

INSERT INTO project_runtime_settings (project_id, max_model_calls, max_tokens, created_at, updated_at)
SELECT id, 32, 200000, created_at, updated_at
FROM projects;
