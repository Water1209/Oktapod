DROP INDEX IF EXISTS idx_preset_resource_states_owner;
DROP TABLE IF EXISTS preset_resource_states;
DROP INDEX IF EXISTS idx_capabilities_owner_preset;
ALTER TABLE capabilities DROP COLUMN update_state;
ALTER TABLE capabilities DROP COLUMN managed_version;
ALTER TABLE capabilities DROP COLUMN dependencies_json;
ALTER TABLE capabilities DROP COLUMN preset_key;
ALTER TABLE capabilities DROP COLUMN origin;
