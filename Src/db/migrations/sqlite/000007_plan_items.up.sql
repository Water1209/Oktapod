CREATE TABLE IF NOT EXISTS plan_items (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('todo', 'in_progress', 'paused', 'done')),
    source TEXT NOT NULL CHECK (source IN ('manual', 'task')),
    linked_task_id TEXT NOT NULL DEFAULT '',
    visibility TEXT NOT NULL CHECK (visibility IN ('private', 'selected', 'project')),
    rank INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

INSERT OR IGNORE INTO plan_items (
    id, organization_id, project_id, owner_user_id, title, description,
    status, source, linked_task_id, visibility, rank, created_at, updated_at
)
SELECT id, organization_id, project_id, owner_user_id, title, goal,
       'todo', 'task', id, visibility,
       ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY created_at, id) * 1024,
       created_at, updated_at
FROM tasks;

CREATE INDEX IF NOT EXISTS idx_plan_items_board
    ON plan_items(organization_id, project_id, status, visibility, owner_user_id, rank, id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_plan_items_linked_task
    ON plan_items(linked_task_id)
    WHERE linked_task_id <> '';

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (7, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
