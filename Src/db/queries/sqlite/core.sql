-- name: UpsertOrganization :exec
INSERT INTO organizations (id, name, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at;

-- name: UpsertUser :exec
INSERT INTO users (id, display_name, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET display_name = excluded.display_name, updated_at = excluded.updated_at;

-- name: UpsertMembership :exec
INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(organization_id, user_id)
DO UPDATE SET role = excluded.role, status = excluded.status, updated_at = excluded.updated_at;

-- name: GetOrganization :one
SELECT id, name, created_at, updated_at
FROM organizations
WHERE id = ?;

-- name: GetUser :one
SELECT id, display_name, created_at, updated_at
FROM users
WHERE id = ?;

-- name: CreateProject :one
INSERT INTO projects (
    id, organization_id, owner_user_id, name, description, kind, scene, default_model, fallback_model, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
RETURNING *;

-- name: CreateWorkspace :exec
INSERT INTO workspaces (project_id, workspace_key, relative_path, created_at)
VALUES (?, ?, ?, ?);

-- name: UpsertProjectMembership :exec
INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(project_id, user_id)
DO UPDATE SET role = excluded.role, status = excluded.status, updated_at = excluded.updated_at;

-- name: GetProjectForUser :one
SELECT p.*
FROM projects p
JOIN memberships m ON m.organization_id = p.organization_id
JOIN project_memberships pm ON pm.project_id = p.id AND pm.user_id = m.user_id
WHERE p.id = ?
  AND p.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND p.status = 'active';

-- name: ListProjectsForUser :many
SELECT p.*
FROM projects p
JOIN memberships m ON m.organization_id = p.organization_id
JOIN project_memberships pm ON pm.project_id = p.id AND pm.user_id = m.user_id
WHERE p.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND p.status = 'active'
ORDER BY p.updated_at DESC, p.id DESC
LIMIT ?;

-- name: CreateConversation :one
INSERT INTO conversations (
    id, organization_id, project_id, owner_user_id, title, visibility,
    next_message_seq, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)
RETURNING *;

-- name: AllocateMessageSequence :one
UPDATE conversations
SET next_message_seq = next_message_seq + 1,
    updated_at = ?
WHERE id = ?
RETURNING next_message_seq;

-- name: CreateMessage :one
INSERT INTO messages (
    id, organization_id, project_id, conversation_id, owner_user_id,
    server_seq, role, visibility, content, created_at, task_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListConversationsForUser :many
SELECT c.*,
       CAST(COALESCE((
           SELECT m.content FROM messages m
           WHERE m.conversation_id = c.id
           ORDER BY m.server_seq DESC LIMIT 1
       ), '') AS TEXT) AS last_message
FROM conversations c
JOIN memberships m ON m.organization_id = c.organization_id
JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = m.user_id
WHERE c.organization_id = ?
  AND c.project_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (c.visibility = 'project' OR c.owner_user_id = ?)
ORDER BY c.updated_at DESC, c.id DESC
LIMIT ?;

-- name: ListMessagesForUser :many
SELECT msg.*
FROM messages msg
JOIN conversations c ON c.id = msg.conversation_id
JOIN memberships m ON m.organization_id = msg.organization_id
JOIN project_memberships pm ON pm.project_id = msg.project_id AND pm.user_id = m.user_id
WHERE msg.conversation_id = ?
  AND msg.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (msg.visibility = 'project' OR msg.owner_user_id = ?)
  AND msg.server_seq > ?
ORDER BY msg.server_seq ASC
LIMIT ?;

-- name: CreateTask :one
INSERT INTO tasks (
    id, organization_id, project_id, owner_user_id, title, goal, acceptance, conversation_id,
    mode, visibility, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?)
RETURNING *;

-- name: CreateRun :one
INSERT INTO runs (
    id, organization_id, project_id, task_id, status, config_snapshot_json, created_at, updated_at
) VALUES (?, ?, ?, ?, 'queued', ?, ?, ?)
RETURNING *;

-- name: CreateStep :one
INSERT INTO steps (
    id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?)
RETURNING *;

-- name: GetTaskForUser :one
SELECT t.*,
       (SELECT id FROM runs WHERE task_id = t.id ORDER BY created_at DESC LIMIT 1) AS run_id,
       COALESCE((
           SELECT ao.id FROM accepted_outcomes ao
           WHERE ao.run_id = (SELECT id FROM runs WHERE task_id = t.id ORDER BY created_at DESC LIMIT 1)
       ), '') AS accepted_outcome_id
FROM tasks t
JOIN memberships m ON m.organization_id = t.organization_id
JOIN project_memberships pm ON pm.project_id = t.project_id AND pm.user_id = m.user_id
WHERE t.id = ?
  AND t.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (t.visibility = 'project' OR t.owner_user_id = ?);

-- name: ListTasksForUser :many
SELECT t.*,
       (SELECT id FROM runs WHERE task_id = t.id ORDER BY created_at DESC LIMIT 1) AS run_id,
       COALESCE((
           SELECT ao.id FROM accepted_outcomes ao
           WHERE ao.run_id = (SELECT id FROM runs WHERE task_id = t.id ORDER BY created_at DESC LIMIT 1)
       ), '') AS accepted_outcome_id
FROM tasks t
JOIN memberships m ON m.organization_id = t.organization_id
JOIN project_memberships pm ON pm.project_id = t.project_id AND pm.user_id = m.user_id
WHERE t.organization_id = ?
  AND t.project_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (t.visibility = 'project' OR t.owner_user_id = ?)
ORDER BY t.updated_at DESC, t.id DESC
LIMIT ?;

-- name: AllocateRunEventSequence :one
INSERT INTO run_event_streams (run_id, next_seq)
VALUES (?, 1)
ON CONFLICT(run_id) DO UPDATE SET next_seq = run_event_streams.next_seq + 1
RETURNING next_seq;

-- name: CreateRunEvent :one
INSERT INTO run_events (
    id, organization_id, project_id, run_id, stream_seq, event_type, schema_version,
    actor_type, actor_id, correlation_id, causation_id, visibility, payload_json, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CreateOutboxEvent :exec
INSERT INTO outbox_events (
    id, organization_id, project_id, topic, aggregate_id, payload_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListRunEventsForUser :many
SELECT e.*
FROM run_events e
JOIN runs r ON r.id = e.run_id
JOIN tasks t ON t.id = r.task_id
JOIN memberships m ON m.organization_id = e.organization_id
JOIN project_memberships pm ON pm.project_id = e.project_id AND pm.user_id = m.user_id
WHERE e.run_id = ?
  AND e.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (e.visibility = 'project' OR t.owner_user_id = ?)
  AND e.stream_seq > ?
ORDER BY e.stream_seq ASC
LIMIT ?;

-- name: ListProjectActivityForUser :many
SELECT e.id, e.project_id, e.run_id, r.task_id, t.title AS task_title,
       t.status AS task_status, t.mode, e.event_type, e.visibility,
       e.payload_json, e.occurred_at
FROM run_events e
JOIN runs r ON r.id = e.run_id
JOIN tasks t ON t.id = r.task_id
JOIN memberships m ON m.organization_id = e.organization_id
JOIN project_memberships pm ON pm.project_id = e.project_id AND pm.user_id = m.user_id
WHERE e.organization_id = ?
  AND e.project_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (e.visibility = 'project' OR t.owner_user_id = ?)
  AND e.occurred_at < COALESCE(NULLIF(?, ''), '9999-12-31T23:59:59.999999999Z')
ORDER BY e.occurred_at DESC, e.id DESC
LIMIT ?;

-- name: NextPlanItemRank :one
SELECT COALESCE(MAX(rank), 0) + 1024
FROM plan_items
WHERE project_id = ? AND status = ?;

-- name: CreatePlanItem :one
INSERT INTO plan_items (
    id, organization_id, project_id, owner_user_id, title, description,
    status, source, linked_task_id, visibility, rank, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListPlanItemsForUser :many
SELECT pi.*
FROM plan_items pi
JOIN memberships m ON m.organization_id = pi.organization_id
JOIN project_memberships pm ON pm.project_id = pi.project_id AND pm.user_id = m.user_id
WHERE pi.organization_id = ?
  AND pi.project_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (pi.visibility = 'project' OR pi.owner_user_id = ?)
ORDER BY CASE pi.status
    WHEN 'todo' THEN 1 WHEN 'in_progress' THEN 2 WHEN 'paused' THEN 3 ELSE 4 END,
    pi.rank ASC, pi.id ASC
LIMIT ?;

-- name: GetPlanItemForUser :one
SELECT pi.*
FROM plan_items pi
JOIN memberships m ON m.organization_id = pi.organization_id
JOIN project_memberships pm ON pm.project_id = pi.project_id AND pm.user_id = m.user_id
WHERE pi.id = ?
  AND pi.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (pi.visibility = 'project' OR pi.owner_user_id = ?);

-- name: UpdatePlanItemStatusForUser :one
UPDATE plan_items
SET status = ?, rank = ?, updated_at = ?
WHERE plan_items.id = ?
  AND plan_items.organization_id = ?
  AND EXISTS (
      SELECT 1
      FROM memberships m
      JOIN project_memberships pm ON pm.project_id = plan_items.project_id AND pm.user_id = m.user_id
      WHERE m.organization_id = plan_items.organization_id
        AND m.user_id = ?
        AND m.status = 'active'
        AND pm.status = 'active'
  )
  AND (plan_items.visibility = 'project' OR plan_items.owner_user_id = ?)
RETURNING *;

-- name: CreateAsset :one
INSERT INTO assets (
    id, organization_id, project_id, owner_user_id, parent_id, name, kind,
    media_type, content_hash, size_bytes, visibility, source_type,
    source_artifact_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAssetForUser :one
SELECT a.*
FROM assets a
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.id = ?
  AND a.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR a.owner_user_id = ?);

-- name: GetAssetBlobForUser :one
SELECT a.*, b.storage_key
FROM assets a
JOIN blobs b ON b.content_hash = a.content_hash
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.id = ?
  AND a.kind = 'file'
  AND a.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR a.owner_user_id = ?);

-- name: ListAssetsByParentForUser :many
SELECT a.*
FROM assets a
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.organization_id = ?
  AND a.project_id = ?
  AND a.parent_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR a.owner_user_id = ?)
ORDER BY CASE a.kind WHEN 'folder' THEN 1 ELSE 2 END, lower(a.name), a.id
LIMIT ?;

-- name: ListAssetsByParentAndKindForUser :many
SELECT a.*
FROM assets a
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.organization_id = ?
  AND a.project_id = ?
  AND a.parent_id = ?
  AND a.kind = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR a.owner_user_id = ?)
ORDER BY lower(a.name), a.id
LIMIT ?;

-- name: SearchAssetsForUser :many
SELECT a.*
FROM assets a
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.organization_id = ?
  AND a.project_id = ?
  AND instr(lower(a.name), lower(?)) > 0
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR a.owner_user_id = ?)
ORDER BY a.updated_at DESC, a.id DESC
LIMIT ?;

-- name: SearchAssetsByKindForUser :many
SELECT a.*
FROM assets a
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.organization_id = ?
  AND a.project_id = ?
  AND instr(lower(a.name), lower(?)) > 0
  AND a.kind = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR a.owner_user_id = ?)
ORDER BY a.updated_at DESC, a.id DESC
LIMIT ?;

-- name: SumVisibleAssetBytesForUser :one
SELECT CAST(COALESCE(SUM(a.size_bytes), 0) AS INTEGER)
FROM assets a
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.organization_id = ?
  AND a.project_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR a.owner_user_id = ?);

-- name: SumProjectAssetBytes :one
SELECT CAST(COALESCE(SUM(size_bytes), 0) AS INTEGER)
FROM assets
WHERE organization_id = ? AND project_id = ?;

-- name: GetArtifactForUser :one
SELECT a.*
FROM artifacts a
JOIN tasks t ON t.id = a.task_id
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.id = ?
  AND a.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR t.owner_user_id = ?);

-- name: GetArtifactBlobForUser :one
SELECT a.*, b.storage_key
FROM artifacts a
JOIN blobs b ON b.content_hash = a.content_hash
JOIN tasks t ON t.id = a.task_id
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.id = ?
  AND a.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR t.owner_user_id = ?);

-- name: FindUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(?) AND email <> '';

-- name: CreateInvitedUser :one
INSERT INTO users (id, display_name, email, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: ActivateOrganizationMembership :exec
INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at)
VALUES (?, ?, 'member', 'active', ?, ?)
ON CONFLICT(organization_id, user_id)
DO UPDATE SET status = 'active', updated_at = excluded.updated_at;

-- name: GetProjectMembershipForUser :one
SELECT pm.*
FROM project_memberships pm
JOIN projects p ON p.id = pm.project_id
WHERE pm.project_id = ?
  AND pm.user_id = ?
  AND pm.status = 'active'
  AND p.organization_id = ?
  AND p.status = 'active';

-- name: CountActiveProjectOwners :one
SELECT COUNT(*)
FROM project_memberships
WHERE project_id = ? AND role = 'owner' AND status = 'active';

-- name: ListProjectMembersForUser :many
SELECT u.id AS user_id, u.display_name, u.email, pm.role, pm.status, pm.created_at, pm.updated_at
FROM project_memberships pm
JOIN users u ON u.id = pm.user_id
JOIN projects p ON p.id = pm.project_id
JOIN project_memberships requester ON requester.project_id = pm.project_id
WHERE pm.project_id = ?
  AND p.organization_id = ?
  AND p.status = 'active'
  AND requester.user_id = ?
  AND requester.status = 'active'
ORDER BY CASE pm.role WHEN 'owner' THEN 1 WHEN 'approver' THEN 2 ELSE 3 END,
         lower(u.display_name), u.id;

-- name: GetProjectMemberForUser :one
SELECT u.id AS user_id, u.display_name, u.email, pm.role, pm.status, pm.created_at, pm.updated_at
FROM project_memberships pm
JOIN users u ON u.id = pm.user_id
JOIN projects p ON p.id = pm.project_id
JOIN project_memberships requester ON requester.project_id = pm.project_id
WHERE pm.project_id = ?
  AND pm.user_id = ?
  AND p.organization_id = ?
  AND p.status = 'active'
  AND requester.user_id = ?
  AND requester.status = 'active';

-- name: RemoveProjectMemberAsOwner :execrows
DELETE FROM project_memberships
WHERE project_memberships.project_id = ?
  AND project_memberships.user_id = ?
  AND EXISTS (
      SELECT 1 FROM project_memberships requester
      JOIN projects p ON p.id = requester.project_id
      WHERE requester.project_id = project_memberships.project_id
        AND requester.user_id = ?
        AND requester.role = 'owner'
        AND requester.status = 'active'
        AND p.organization_id = ?
        AND p.status = 'active'
  );

-- name: CreateAuditEvent :exec
INSERT INTO audit_events (
    id, organization_id, project_id, actor_user_id, action, target_type,
    target_id, outcome, detail_json, correlation_id, occurred_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CreateAgentTeamStream :exec
INSERT INTO agent_team_streams (run_id, next_seq) VALUES (?, 0);

-- name: CreateAgentTeamMember :one
INSERT INTO agent_team_members (
    id, organization_id, project_id, task_id, run_id, role, display_name,
    status, current_step_id, model, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CreateAgentStep :one
INSERT INTO steps (
    id, organization_id, project_id, run_id, step_type, position, status,
    input_json, agent_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?)
RETURNING *;

-- name: UpdateAgentTeamMemberStatus :execrows
UPDATE agent_team_members
SET status = sqlc.arg(status),
    current_step_id = sqlc.arg(current_step_id),
    model = CASE WHEN sqlc.arg(model) = '' THEN model ELSE sqlc.arg(model) END,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND run_id = sqlc.arg(run_id);

-- name: AllocateAgentTeamMessageSequence :one
UPDATE agent_team_streams
SET next_seq = next_seq + 1
WHERE run_id = ?
RETURNING next_seq;

-- name: CreateAgentTeamMessage :one
INSERT INTO agent_team_messages (
    id, organization_id, project_id, task_id, run_id, sender_agent_id,
    server_seq, message_type, content, content_hash, visibility, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAgentTeamMembersForUser :many
SELECT atm.*
FROM agent_team_members atm
JOIN tasks t ON t.id = atm.task_id
JOIN memberships m ON m.organization_id = atm.organization_id
JOIN project_memberships pm ON pm.project_id = atm.project_id AND pm.user_id = m.user_id
WHERE atm.run_id = ?
  AND atm.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (t.visibility = 'project' OR t.owner_user_id = ?)
ORDER BY CASE atm.role WHEN 'coordinator' THEN 1 WHEN 'researcher' THEN 2 WHEN 'builder' THEN 3 ELSE 4 END;

-- name: ListAgentTeamMessagesForUser :many
SELECT atm.*
FROM agent_team_messages atm
JOIN tasks t ON t.id = atm.task_id
JOIN memberships m ON m.organization_id = atm.organization_id
JOIN project_memberships pm ON pm.project_id = atm.project_id AND pm.user_id = m.user_id
WHERE atm.run_id = ?
  AND atm.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (t.visibility = 'project' OR t.owner_user_id = ?)
ORDER BY atm.server_seq;

-- name: CreateSchedule :one
INSERT INTO schedules (
    id, organization_id, project_id, owner_user_id, name, cron_expression,
    timezone, goal, acceptance, mode, visibility, status, next_run_at,
    misfire_policy, overlap_policy, misfire_grace_seconds, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'enabled', ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListSchedulesForUser :many
SELECT s.*
FROM schedules s
JOIN project_memberships pm ON pm.project_id = s.project_id
JOIN projects p ON p.id = s.project_id
WHERE s.project_id = ?
  AND s.organization_id = ?
  AND p.status = 'active'
  AND pm.user_id = ?
  AND pm.status = 'active'
ORDER BY s.next_run_at, s.created_at, s.id
LIMIT ?;

-- name: GetScheduleForUser :one
SELECT s.*
FROM schedules s
JOIN project_memberships pm ON pm.project_id = s.project_id
JOIN projects p ON p.id = s.project_id
WHERE s.id = ?
  AND s.organization_id = ?
  AND p.status = 'active'
  AND pm.user_id = ?
  AND pm.status = 'active';

-- name: SetScheduleStatusAsOwner :one
UPDATE schedules
SET status = ?, updated_at = ?
WHERE id = ?
  AND organization_id = ?
  AND owner_user_id = ?
RETURNING *;

-- name: GetDueSchedule :one
SELECT *
FROM schedules
WHERE status = 'enabled'
  AND next_run_at <= ?
ORDER BY next_run_at, created_at, id
LIMIT 1;

-- name: CreateScheduleFire :one
INSERT OR IGNORE INTO schedule_fires (
    id, schedule_id, organization_id, project_id, planned_at, generation,
    status, task_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetScheduleFire :one
SELECT * FROM schedule_fires
WHERE schedule_id = ? AND planned_at = ? AND generation = ?;

-- name: UpdateScheduleAfterFire :exec
UPDATE schedules
SET next_run_at = ?, updated_at = ?
WHERE id = ? AND next_run_at = ?;

-- name: UpdateScheduleFire :exec
UPDATE schedule_fires
SET status = ?, task_id = ?, updated_at = ?
WHERE id = ? AND status = 'pending';

-- name: CreateCapability :one
INSERT INTO capabilities (id, organization_id, project_id, owner_user_id, kind, name, version, manifest_json, content_hash, status, risk_level, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListCapabilitiesForUser :many
SELECT c.* FROM capabilities c
JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = ? AND pm.status = 'active'
WHERE c.organization_id = ? AND c.project_id = ?
ORDER BY c.updated_at DESC, c.id ASC LIMIT ?;

-- name: SetCapabilityStatusAsOwner :one
UPDATE capabilities SET status = ?, updated_at = ?
WHERE id = ? AND organization_id = ? AND owner_user_id = ?
RETURNING *;

-- name: CreateMemory :one
INSERT INTO memories (id, organization_id, project_id, owner_user_id, kind, title, content, source_hash, visibility, status, source_refs_json, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)
RETURNING *;

-- name: ListMemoriesForUser :many
SELECT m.* FROM memories m
JOIN project_memberships pm ON pm.project_id = m.project_id AND pm.user_id = ? AND pm.status = 'active'
WHERE m.organization_id = ? AND m.project_id = ?
  AND (m.visibility = 'project' OR m.owner_user_id = ?)
  AND (m.status = 'active' OR m.owner_user_id = ? OR pm.role IN ('owner', 'approver'))
  AND (? = '' OR lower(m.title) LIKE lower('%' || ? || '%') OR lower(m.content) LIKE lower('%' || ? || '%'))
ORDER BY m.created_at DESC, m.id ASC LIMIT ?;

-- name: CreateAgentDefinition :one
INSERT INTO agent_definitions (id, organization_id, project_id, owner_user_id, name, version, instructions, model, fallback_model, config_json, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAgentDefinitionsForUser :many
SELECT a.* FROM agent_definitions a
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = ? AND pm.status = 'active'
WHERE a.organization_id = ? AND a.project_id = ?
ORDER BY a.updated_at DESC, a.id ASC LIMIT ?;

-- name: SetAgentDefinitionStatusAsOwner :one
UPDATE agent_definitions SET status = ?, updated_at = ?
WHERE id = ? AND organization_id = ? AND owner_user_id = ?
RETURNING *;

-- name: ClaimNextStep :one
UPDATE steps
SET status = 'running',
    owner_id = ?,
    lease_until = ?,
    fence_token = fence_token + 1,
    attempt_count = attempt_count + 1,
    updated_at = ?
WHERE id = (
    SELECT candidate.id
    FROM steps AS candidate
    JOIN runs AS candidate_run ON candidate_run.id = candidate.run_id
    WHERE (candidate.status = 'queued'
       OR (candidate.status = 'running' AND candidate.lease_until <> '' AND candidate.lease_until < ?))
      AND candidate_run.status IN ('queued', 'running')
      AND (
          EXISTS (
              SELECT 1 FROM steps same_run
              WHERE same_run.run_id = candidate.run_id
                AND same_run.status = 'running'
                AND same_run.lease_until >= sqlc.arg(now_active)
          )
          OR (
              SELECT COUNT(DISTINCT active.run_id)
              FROM steps active
              WHERE active.status = 'running'
                AND active.lease_until >= sqlc.arg(now_capacity)
          ) < 4
      )
      AND NOT EXISTS (
          SELECT 1
          FROM steps AS earlier
          WHERE earlier.run_id = candidate.run_id
            AND earlier.position < candidate.position
            AND earlier.status IN ('queued', 'running', 'waiting_approval')
            AND NOT (
                candidate.tool_batch_id <> ''
                AND earlier.tool_batch_id = candidate.tool_batch_id
                AND (
                    (
                        NOT EXISTS (SELECT 1 FROM tool_batch_schedulers scheduler WHERE scheduler.batch_id = candidate.tool_batch_id)
                        AND EXISTS (
                            SELECT 1 FROM tool_batches batch
                            WHERE batch.id = candidate.tool_batch_id
                              AND batch.execution_mode = 'parallel'
                        )
                    )
                    OR EXISTS (
                        SELECT 1
                        FROM tool_call_barriers candidate_barrier
                        JOIN tool_call_barriers earlier_barrier
                          ON earlier_barrier.step_id = earlier.id
                        WHERE candidate_barrier.step_id = candidate.id
                          AND candidate_barrier.barrier_group = earlier_barrier.barrier_group
                    )
                )
            )
      )
      AND (
          candidate.tool_batch_id = ''
          OR NOT EXISTS (
              SELECT 1 FROM steps batch_failure
              WHERE batch_failure.tool_batch_id = candidate.tool_batch_id
                AND batch_failure.status IN ('blocked', 'failed', 'canceled')
          )
      )
    ORDER BY candidate.created_at ASC, candidate.id ASC
    LIMIT 1
)
RETURNING *;

-- name: CompleteStep :execrows
UPDATE steps
SET status = ?, output_json = ?, error_code = ?, lease_until = '', updated_at = ?
WHERE id = ? AND owner_id = ? AND fence_token = ? AND status = 'running';

-- name: HeartbeatStep :execrows
UPDATE steps
SET lease_until = ?, updated_at = ?
WHERE id = ? AND owner_id = ? AND fence_token = ? AND status = 'running';

-- name: CreateStepAttempt :exec
INSERT INTO step_attempts (
    id, organization_id, project_id, run_id, step_id, attempt_number,
    attempt_kind, owner_id, fence_token, status, started_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'running', ?);

-- name: FinishStepAttempt :execrows
UPDATE step_attempts
SET status = ?, error_code = ?, finished_at = ?
WHERE id = ? AND owner_id = ? AND fence_token = ? AND status = 'running';

-- name: LosePreviousStepAttempts :exec
UPDATE step_attempts
SET status = 'lease_lost', error_code = 'LEASE_EXPIRED', finished_at = ?
WHERE step_id = ? AND status = 'running';

-- name: CreateCheckpoint :exec
INSERT INTO checkpoints (
    id, organization_id, project_id, run_id, step_id, attempt_id,
    checkpoint_kind, state_json, content_hash, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CreateAcceptedOutcome :one
INSERT INTO accepted_outcomes (
    id, organization_id, project_id, task_id, run_id, accepted_by, note, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAcceptedOutcomeByRun :one
SELECT * FROM accepted_outcomes WHERE run_id = ?;

-- name: CreateBlob :exec
INSERT INTO blobs (content_hash, storage_key, size_bytes, media_type, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(content_hash) DO NOTHING;

-- name: CreateArtifact :one
INSERT INTO artifacts (
    id, organization_id, project_id, task_id, run_id, step_id, name, kind,
    media_type, content_hash, size_bytes, visibility, source_type, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListRunArtifactsForUser :many
SELECT a.*
FROM artifacts a
JOIN runs r ON r.id = a.run_id
JOIN tasks t ON t.id = r.task_id
JOIN memberships m ON m.organization_id = a.organization_id
JOIN project_memberships pm ON pm.project_id = a.project_id AND pm.user_id = m.user_id
WHERE a.run_id = ?
  AND a.organization_id = ?
  AND m.user_id = ?
  AND m.status = 'active'
  AND pm.status = 'active'
  AND (a.visibility = 'project' OR t.owner_user_id = ?)
ORDER BY a.created_at ASC, a.id ASC;

-- name: UpdateTaskResult :exec
UPDATE tasks
SET status = ?, result_summary = ?, error_code = ?, updated_at = ?
WHERE id = ?;

-- name: UpdateRunStatus :exec
UPDATE runs
SET status = ?, updated_at = ?
WHERE id = ?;

-- name: GetRunContext :one
SELECT r.id AS run_id, r.task_id, r.project_id, r.organization_id, r.status AS run_status,
       t.owner_user_id, t.conversation_id, t.title, t.goal, t.acceptance, t.mode, t.visibility, t.status AS task_status
FROM runs r
JOIN tasks t ON t.id = r.task_id
WHERE r.id = ?;

-- name: CreateModelCall :exec
INSERT INTO model_calls (
    id, organization_id, project_id, task_id, run_id, step_id, provider, endpoint, model,
    routing_reason, status, provider_request_id, input_tokens, output_tokens, cached_tokens,
    reasoning_tokens, usage_estimated, latency_ms, error_code, created_at, pricing_snapshot_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: CreateUsageLedger :exec
INSERT INTO usage_ledger (
    id, organization_id, project_id, task_id, run_id, step_id, call_id,
    input_tokens, output_tokens, cached_tokens, reasoning_tokens, cost_decimal,
    currency, confirmed, created_at, pricing_snapshot_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
