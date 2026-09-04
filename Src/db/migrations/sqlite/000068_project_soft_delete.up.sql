-- oktapod:sqlite-only
-- oktapod:foreign-keys-off
PRAGMA foreign_keys = OFF;
PRAGMA defer_foreign_keys = ON;
PRAGMA legacy_alter_table = ON;

ALTER TABLE projects RENAME TO projects_legacy;

CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('personal', 'team')),
    scene TEXT NOT NULL CHECK (scene IN ('work', 'code')),
    default_model TEXT NOT NULL DEFAULT '',
    fallback_model TEXT NOT NULL DEFAULT '',
    model_provider_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived', 'deleted')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT INTO projects (
    id, organization_id, owner_user_id, name, description, kind, scene,
    default_model, fallback_model, model_provider_id, status, created_at, updated_at
)
SELECT
    id, organization_id, owner_user_id, name, description, kind, scene,
    default_model, fallback_model, model_provider_id, status, created_at, updated_at
FROM projects_legacy;

DROP TABLE projects_legacy;

CREATE INDEX IF NOT EXISTS idx_projects_scope_updated
    ON projects(organization_id, owner_user_id, status, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_projects_model_policy
    ON projects(organization_id, default_model, fallback_model);

PRAGMA legacy_alter_table = OFF;
PRAGMA defer_foreign_keys = OFF;
PRAGMA foreign_keys = ON;
