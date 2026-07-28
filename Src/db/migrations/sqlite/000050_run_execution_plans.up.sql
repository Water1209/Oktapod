CREATE TABLE IF NOT EXISTS run_plan_revisions (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    trigger TEXT NOT NULL CHECK (trigger IN ('initial', 'steering', 'follow_up', 'permission_change', 'recovery')),
    goal_summary TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('active', 'superseded', 'completed', 'blocked', 'canceled')),
    supersedes_revision_id TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    superseded_at TEXT NOT NULL DEFAULT '',
    UNIQUE (run_id, revision)
);

CREATE TABLE IF NOT EXISTS run_plan_items (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    plan_revision_id TEXT NOT NULL REFERENCES run_plan_revisions(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'waiting_user', 'completed', 'blocked', 'canceled')),
    result_summary TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL DEFAULT '',
    completed_at TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (plan_revision_id, position)
);

ALTER TABLE steps ADD COLUMN plan_revision_id TEXT NOT NULL DEFAULT '';
ALTER TABLE steps ADD COLUMN run_plan_item_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN plan_revision_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_calls ADD COLUMN run_plan_item_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_run_plan_revisions_active
    ON run_plan_revisions(organization_id, project_id, run_id, status, revision DESC);

CREATE INDEX IF NOT EXISTS idx_run_plan_items_revision
    ON run_plan_items(plan_revision_id, position, status);

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (50, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
