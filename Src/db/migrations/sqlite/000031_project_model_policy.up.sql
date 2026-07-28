ALTER TABLE projects ADD COLUMN default_model TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN fallback_model TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_projects_model_policy
    ON projects(organization_id, default_model, fallback_model);
