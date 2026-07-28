-- Project usage is queried by project and time window. Keep the aggregate
-- endpoint bounded without scanning unrelated organizations or old calls.
CREATE INDEX IF NOT EXISTS idx_model_calls_project_created
    ON model_calls(organization_id, project_id, created_at, model, id);

CREATE INDEX IF NOT EXISTS idx_usage_ledger_project_created
    ON usage_ledger(organization_id, project_id, created_at, id);
