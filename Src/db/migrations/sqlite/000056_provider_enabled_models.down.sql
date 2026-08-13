DROP INDEX IF EXISTS provider_enabled_models_org_provider;
DROP TABLE IF EXISTS provider_enabled_models;
ALTER TABLE projects DROP COLUMN model_provider_id;
