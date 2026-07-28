ALTER TABLE steps ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';

CREATE TABLE agent_team_members (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    role TEXT NOT NULL CHECK (role IN ('coordinator', 'researcher', 'builder', 'reviewer')),
    display_name TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('planned', 'working', 'waiting', 'done', 'failed')),
    current_step_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(run_id, role)
);

CREATE INDEX agent_team_members_run_status
    ON agent_team_members(organization_id, project_id, run_id, status, role);

CREATE TABLE agent_team_streams (
    run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    next_seq INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE agent_team_messages (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    sender_agent_id TEXT NOT NULL REFERENCES agent_team_members(id),
    server_seq INTEGER NOT NULL,
    message_type TEXT NOT NULL CHECK (message_type IN ('plan', 'handoff', 'result', 'review', 'synthesis')),
    content TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    visibility TEXT NOT NULL CHECK (visibility IN ('private', 'selected', 'project')),
    created_at TEXT NOT NULL,
    UNIQUE(run_id, server_seq)
);

CREATE INDEX agent_team_messages_run_seq
    ON agent_team_messages(organization_id, project_id, run_id, server_seq);
