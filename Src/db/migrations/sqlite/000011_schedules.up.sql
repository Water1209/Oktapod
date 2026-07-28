CREATE TABLE schedules (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    owner_user_id TEXT NOT NULL REFERENCES users(id),
    name TEXT NOT NULL,
    cron_expression TEXT NOT NULL,
    timezone TEXT NOT NULL,
    goal TEXT NOT NULL,
    acceptance TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL CHECK (mode IN ('answer', 'single_agent', 'agent_team')),
    visibility TEXT NOT NULL CHECK (visibility IN ('private', 'selected', 'project')),
    status TEXT NOT NULL CHECK (status IN ('enabled', 'disabled')),
    next_run_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX schedules_due
    ON schedules(status, next_run_at, organization_id, project_id);

CREATE TABLE schedule_fires (
    id TEXT PRIMARY KEY,
    schedule_id TEXT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    planned_at TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL CHECK (status IN ('pending', 'created', 'skipped', 'waiting_confirmation')),
    task_id TEXT REFERENCES tasks(id),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(schedule_id, planned_at, generation)
);

CREATE INDEX schedule_fires_status
    ON schedule_fires(schedule_id, status, planned_at);
