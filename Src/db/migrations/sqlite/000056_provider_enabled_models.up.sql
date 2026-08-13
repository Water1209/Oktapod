ALTER TABLE projects ADD COLUMN model_provider_id TEXT NOT NULL DEFAULT '';

CREATE TABLE provider_enabled_models (
    organization_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL REFERENCES provider_instances(provider_id) ON DELETE CASCADE,
    model_id TEXT NOT NULL,
    source TEXT NOT NULL CHECK (source IN ('catalog', 'manual')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (provider_id, model_id)
);

CREATE INDEX provider_enabled_models_org_provider
    ON provider_enabled_models(organization_id, provider_id, updated_at DESC);

UPDATE projects
SET model_provider_id = COALESCE((
    SELECT provider_id FROM provider_instances pi
    WHERE pi.organization_id = projects.organization_id
    ORDER BY pi.is_default DESC, pi.updated_at DESC
    LIMIT 1
), '')
WHERE default_model <> '' OR fallback_model <> '';

INSERT INTO provider_enabled_models (organization_id, provider_id, model_id, source, created_at, updated_at)
SELECT pi.organization_id, pi.provider_id, pi.default_model_id,
       CASE WHEN mce.model_id IS NULL THEN 'manual' ELSE 'catalog' END,
       pi.created_at, pi.updated_at
FROM provider_instances pi
LEFT JOIN model_catalog_entries mce
  ON mce.provider_id = pi.provider_id AND mce.model_id = pi.default_model_id
WHERE pi.default_model_id <> ''
ON CONFLICT(provider_id, model_id) DO NOTHING;

INSERT INTO provider_enabled_models (organization_id, provider_id, model_id, source, created_at, updated_at)
SELECT DISTINCT p.organization_id, p.model_provider_id, models.model_id,
       CASE WHEN mce.model_id IS NULL THEN 'manual' ELSE 'catalog' END,
       p.created_at, p.updated_at
FROM projects p
JOIN (
    SELECT id AS project_id, default_model AS model_id FROM projects WHERE default_model <> ''
    UNION
    SELECT id AS project_id, fallback_model AS model_id FROM projects WHERE fallback_model <> ''
) models ON models.project_id = p.id
LEFT JOIN model_catalog_entries mce
  ON mce.provider_id = p.model_provider_id AND mce.model_id = models.model_id
WHERE p.model_provider_id <> ''
ON CONFLICT(provider_id, model_id) DO NOTHING;

INSERT INTO provider_enabled_models (organization_id, provider_id, model_id, source, created_at, updated_at)
SELECT DISTINCT a.organization_id, p.model_provider_id, models.model_id,
       CASE WHEN mce.model_id IS NULL THEN 'manual' ELSE 'catalog' END,
       a.created_at, a.updated_at
FROM agent_definitions a
JOIN projects p ON p.id = a.project_id
JOIN (
    SELECT id AS agent_id, model AS model_id FROM agent_definitions WHERE model <> ''
    UNION
    SELECT id AS agent_id, fallback_model AS model_id FROM agent_definitions WHERE fallback_model <> ''
) models ON models.agent_id = a.id
LEFT JOIN model_catalog_entries mce
  ON mce.provider_id = p.model_provider_id AND mce.model_id = models.model_id
WHERE p.model_provider_id <> ''
ON CONFLICT(provider_id, model_id) DO NOTHING;
