CREATE TABLE IF NOT EXISTS human_action_requests (
    id TEXT PRIMARY KEY,
    organization_id TEXT NOT NULL REFERENCES organizations(id),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    step_id TEXT NOT NULL DEFAULT '',
    plan_revision_id TEXT NOT NULL DEFAULT '',
    run_plan_item_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL CHECK (kind IN ('tool_approval', 'permission_escalation', 'input_request', 'budget_request', 'side_effect_resolution')),
    status TEXT NOT NULL CHECK (status IN ('reviewing', 'waiting_user', 'approved', 'rejected', 'answered', 'expired', 'superseded')),
    reviewer TEXT NOT NULL CHECK (reviewer IN ('user', 'auto')),
    risk_level TEXT NOT NULL DEFAULT '',
    title TEXT NOT NULL,
    reason TEXT NOT NULL,
    request_json TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    response_json TEXT NOT NULL DEFAULT '{}',
    requested_by TEXT NOT NULL,
    decided_by TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    decided_at TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1
);

CREATE INDEX IF NOT EXISTS idx_human_actions_inbox
    ON human_action_requests(organization_id, project_id, conversation_id, status, requested_at, id);

CREATE INDEX IF NOT EXISTS idx_human_actions_run
    ON human_action_requests(run_id, plan_revision_id, run_plan_item_id, status);

INSERT OR IGNORE INTO human_action_requests (
    id, organization_id, project_id, conversation_id, task_id, run_id, step_id,
    kind, status, reviewer, risk_level, title, reason, request_json, request_hash,
    response_json, requested_by, decided_by, requested_at, expires_at, decided_at, version
)
SELECT
    a.id, a.organization_id, a.project_id, t.conversation_id, a.task_id, a.run_id, a.step_id,
    'tool_approval',
    CASE a.status WHEN 'pending' THEN 'waiting_user' ELSE a.status END,
    'user', a.risk_level, a.tool_name, a.reason, a.frozen_params_json, a.input_hash,
    CASE WHEN a.status = 'pending' THEN '{}' ELSE json_object('decision', a.status, 'note', a.decision_note) END,
    a.requested_by, a.decided_by, a.requested_at, a.expires_at, a.decided_at, 1
FROM approval_requests a
JOIN tasks t ON t.id = a.task_id;

INSERT OR IGNORE INTO schema_versions(version, applied_at)
VALUES (51, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
