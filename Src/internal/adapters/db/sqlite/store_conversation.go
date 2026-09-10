package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	sqlitegen "github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite/sqlc"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) HasActiveTask(ctx context.Context, scope core.Scope, conversationID string) (bool, string, error) {
	var taskID string
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id
		FROM tasks t
		JOIN runs r ON r.id = (
			SELECT latest.id
			FROM runs latest
			WHERE latest.task_id = t.id
			ORDER BY latest.created_at DESC, latest.id DESC
			LIMIT 1
		)
		WHERE t.conversation_id = ? AND t.organization_id = ?
		  AND r.status IN ('queued', 'running', 'waiting_approval', 'waiting_input', 'paused', 'blocked', 'unknown')
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT 1
	`, conversationID, scope.OrganizationID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, taskID, nil
}

func (s *Store) EnqueueConversationTurn(ctx context.Context, scope core.Scope, projectID, conversationID, activeTaskID, content, inputJSON, kind string, presentation core.MessagePresentation, admission ...core.Idempotency) (core.ConversationQueueItem, error) {
	var key core.Idempotency
	if len(admission) > 0 {
		key = admission[0]
	}
	if key.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		result, found, err := s.ReplayProjectTurn(ctx, scope, projectID, key)
		if err != nil {
			return core.ConversationQueueItem{}, err
		}
		if found && result.QueueItem != nil {
			return *result.QueueItem, nil
		}
	}
	id, err := newID()
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	messageID, err := newID()
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if conversationID == "" {
		if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
			return core.ConversationQueueItem{}, err
		}
		conversationID, err = newID()
		if err != nil {
			return core.ConversationQueueItem{}, err
		}
		if _, err := q.CreateConversation(ctx, sqlitegen.CreateConversationParams{ID: conversationID, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, Title: titleFromMessage(content), Visibility: core.VisibilityPrivate, CreatedAt: now, UpdatedAt: now}); err != nil {
			return core.ConversationQueueItem{}, err
		}
	}
	var conversationProjectID, visibility string
	if err := tx.QueryRowContext(ctx, `
		SELECT c.project_id, c.visibility
		FROM conversations c
		JOIN project_memberships pm ON pm.project_id = c.project_id
		WHERE c.id = ? AND c.organization_id = ?
		  AND pm.user_id = ? AND pm.status = 'active'
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)`,
		conversationID, scope.OrganizationID, scope.UserID, scope.UserID,
	).Scan(&conversationProjectID, &visibility); err != nil {
		return core.ConversationQueueItem{}, err
	}
	if conversationProjectID != projectID {
		return core.ConversationQueueItem{}, &core.ForbiddenError{Message: "conversation is not in this project"}
	}
	messageTaskID := ""
	if kind == "steering" {
		messageTaskID = activeTaskID
	}
	if _, _, err := s.appendSessionMessageTx(ctx, tx, q, sessionMessageInput{
		MessageID: messageID, OrganizationID: scope.OrganizationID, ProjectID: projectID,
		ConversationID: conversationID, OwnerUserID: scope.UserID, TaskID: messageTaskID,
		Role: "user", Visibility: visibility, Content: content, Presentation: presentation, Now: now,
	}); err != nil {
		return core.ConversationQueueItem{}, fmt.Errorf("append queued user message: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_message_queue (id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, 'pending', ?, '', '', ?)
	`, id, scope.OrganizationID, projectID, conversationID, activeTaskID, content, inputJSON, kind, now, messageID); err != nil {
		return core.ConversationQueueItem{}, fmt.Errorf("enqueue conversation turn: %w", err)
	}
	createdAt, _ := parseTime(now)
	item := core.ConversationQueueItem{
		ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID,
		ConversationID: conversationID, ActiveTaskID: activeTaskID, MessageID: messageID,
		Content: content, InputJSON: inputJSON, Kind: kind, Status: "pending",
		CreatedAt: createdAt,
	}
	if key.Key != "" {
		conversation, err := getConversation(ctx, tx, scope, conversationID)
		if err != nil {
			return core.ConversationQueueItem{}, err
		}
		messages, err := listMessages(ctx, tx, scope, conversationID, 0, 200)
		if err != nil {
			return core.ConversationQueueItem{}, err
		}
		result := core.ProjectTurnResult{Route: "task", Conversation: conversation, Messages: messages, Queued: true, QueueItem: &item}
		if err := saveIdempotency(ctx, tx, scope, "POST /projects/"+projectID+"/turns", key, httpStatusAccepted, result); err != nil {
			return core.ConversationQueueItem{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return core.ConversationQueueItem{}, err
	}
	return item, nil
}

func (s *Store) ListConversationQueue(ctx context.Context, scope core.Scope, conversationID string, status string) ([]core.ConversationQueueItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, organization_id, project_id, conversation_id, COALESCE(active_task_id, ''), content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id, claim_version
		FROM conversation_message_queue
		WHERE conversation_id = ? AND organization_id = ? AND status = ?
		ORDER BY created_at ASC
	`, conversationID, scope.OrganizationID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []core.ConversationQueueItem
	for rows.Next() {
		var item core.ConversationQueueItem
		var createdAt, claimedAt, consumedAt string
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt, &item.MessageID, &item.ClaimVersion); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = parseTime(createdAt)
		item.ClaimedAt, _ = parseTime(claimedAt)
		item.ConsumedAt, _ = parseTime(consumedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ClaimSteeringAfterStep(ctx context.Context, scope core.Scope, taskID, conversationID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	now := formatTime(time.Now())
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'claimed', claimed_at = ?, claim_version = claim_version + 1
		WHERE id = (
			SELECT id FROM conversation_message_queue
			WHERE conversation_id = ? AND active_task_id = ? AND kind = 'steering' AND status = 'pending'
			ORDER BY created_at ASC LIMIT 1
		)
		RETURNING id, organization_id, project_id, conversation_id, COALESCE(active_task_id, ''), content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id, claim_version
	`, now, conversationID, taskID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt, &item.MessageID, &item.ClaimVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

// ApplyConversationSteering claims the oldest pending steering message for the
// conversation and, in one transaction, replaces the remaining queued plan with
// a steering replan step. The queue item is consumed only after the replan step
// is durably created; any failure rolls the claim back to pending.
func replaceRunPlanForSteering(ctx context.Context, tx *sql.Tx, run core.RunContext, steering, actorID, now string) (string, string, error) {
	var previousID string
	var previousRevision int64
	err := tx.QueryRowContext(ctx, `SELECT id, revision FROM run_plan_revisions WHERE run_id = ? AND status = 'active' ORDER BY revision DESC LIMIT 1`, run.RunID).Scan(&previousID, &previousRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if previousID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'superseded', superseded_at = ? WHERE id = ?`, now, previousID); err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'canceled', completed_at = ?, updated_at = ? WHERE plan_revision_id = ? AND status NOT IN ('completed', 'canceled')`, now, now, previousID); err != nil {
			return "", "", err
		}
	}
	revisionID, err := newID()
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_plan_revisions (
			id, organization_id, project_id, task_id, run_id, revision, trigger,
			goal_summary, status, supersedes_revision_id, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'steering', ?, 'active', ?, ?, ?)`,
		revisionID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID,
		previousRevision+1, truncateText(strings.TrimSpace(steering), 1000), previousID, actorID, now,
	); err != nil {
		return "", "", err
	}
	titles := []string{"正在根据追加要求重新规划"}
	firstItemID := ""
	for index, title := range titles {
		itemID, idErr := newID()
		if idErr != nil {
			return "", "", idErr
		}
		if index == 0 {
			firstItemID = itemID
		}
		status, startedAt := core.RunPlanItemPending, ""
		if index == 0 {
			status, startedAt = core.RunPlanItemRunning, now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO run_plan_items (
				id, organization_id, project_id, task_id, run_id, plan_revision_id,
				position, title, description, status, started_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, revisionID,
			index+1, title, "模型将保留已完成结果，并生成覆盖合并目标的完整新语义计划。", status, startedAt, now, now,
		); err != nil {
			return "", "", err
		}
	}
	return revisionID, firstItemID, nil
}

func (s *Store) ApplyConversationSteering(ctx context.Context, scope core.Scope, sourceStep core.ClaimedStep, run core.RunContext, correlationID string) (core.ConversationQueueItem, string, error) {
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)

	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	err = tx.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'claimed', claimed_at = ?, claim_version = claim_version + 1
		WHERE id = (
			SELECT id FROM conversation_message_queue
			WHERE conversation_id = ? AND active_task_id = ? AND kind = 'steering' AND status = 'pending'
			ORDER BY created_at ASC LIMIT 1
		)
		RETURNING id, organization_id, project_id, conversation_id, COALESCE(active_task_id, ''), content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id, claim_version
	`, now, run.ConversationID, run.TaskID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt, &item.MessageID, &item.ClaimVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, "", core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)

	history, err := s.collectContinuationHistory(ctx, tx, run)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}

	// Cancel the remainder of the superseded plan. Finished steps stay as
	// context; anything not started is replaced by the steering replan.
	supersededRows, err := tx.QueryContext(ctx, `SELECT id FROM steps WHERE run_id = ? AND status IN ('queued', 'waiting_approval')`, run.RunID)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	supersededStepIDs := make([]string, 0)
	for supersededRows.Next() {
		var stepID string
		if err := supersededRows.Scan(&stepID); err != nil {
			_ = supersededRows.Close()
			return core.ConversationQueueItem{}, "", err
		}
		supersededStepIDs = append(supersededStepIDs, stepID)
	}
	if err := supersededRows.Close(); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	for _, stepID := range supersededStepIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'canceled', error_code = 'PLAN_SUPERSEDED', updated_at = ? WHERE id = ?`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'rejected', error_code = 'PLAN_SUPERSEDED', updated_at = ? WHERE step_id = ?`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = 'system', decision_note = '计划已由用户引导重新规划', decided_at = ? WHERE step_id = ? AND status = 'pending'`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE human_action_requests
			SET status = 'superseded', response_json = '{"reason":"steering_replan"}',
			    decided_by = 'system', decided_at = ?, version = version + 1
			WHERE step_id = ? AND status IN ('waiting_user', 'reviewing')`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
	}

	planRevisionID, planItemID, err := replaceRunPlanForSteering(ctx, tx, run, item.Content, scope.UserID, now)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	replanInput := map[string]any{"history": history, "stage": "steering_replan", "steering": item.Content, "source_step_id": sourceStep.ID}
	replanStepID, err := insertQueuedModelStep(ctx, tx, run, replanInput, now)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, planRevisionID, planItemID, replanStepID); err != nil {
		return core.ConversationQueueItem{}, "", err
	}

	if err := q.UpdateTaskResult(ctx, sqlitegen.UpdateTaskResultParams{Status: core.StatusRunning, ResultSummary: "", ErrorCode: "", UpdatedAt: now, ID: run.TaskID}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := q.UpdateRunStatus(ctx, sqlitegen.UpdateRunStatusParams{Status: core.StatusRunning, UpdatedAt: now, ID: run.RunID}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}

	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "conversation.steered", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"queue_item_id": item.ID, "task_id": run.TaskID, "content": item.Content, "source_step_id": sourceStep.ID, "replan_step_id": replanStepID}}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.replan_queued", ActorType: "system", ActorID: sourceStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": replanStepID, "source_step_id": sourceStep.ID, "queue_item_id": item.ID, "history_items": len(history)}}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "plan.published", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"plan_revision_id": planRevisionID, "trigger": "steering", "goal_summary": item.Content}}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversation_message_queue SET status = 'consumed', consumed_at = ? WHERE id = ? AND status = 'claimed'`, now, item.ID); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	return item, replanStepID, nil
}

func (s *Store) ClaimNextFollowUp(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationQueueItem, error) {
	conversation, err := s.GetConversation(ctx, scope, conversationID)
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	return s.ClaimProjectFollowUp(ctx, scope, conversation.ProjectID, "")
}

// ClaimNextIdleFollowUp lets an idle worker retry queued turns even when no
// step is executing. It first resets items whose claim went stale (worker crash
// between claim and consume), then claims the oldest pending follow-up whose
// conversation has no active task and no other claimed item. Pending items
// claimed since retryBefore are skipped so a repeatedly failing starter backs
// off instead of hot-looping; claimed_at doubles as the last-attempt marker.
func (s *Store) ClaimNextIdleFollowUp(ctx context.Context, retryBefore time.Time) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	now := formatTime(time.Now())
	stale := formatTime(retryBefore)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'pending', claimed_at = ''
		WHERE status = 'claimed' AND claimed_at < ?`, stale); err != nil {
		return core.ConversationQueueItem{}, err
	}
	err = tx.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'claimed', claimed_at = ?, claim_version = claim_version + 1
		WHERE id = (
			SELECT q.id FROM conversation_message_queue q
			WHERE q.status = 'pending'
			  AND (q.claimed_at = '' OR q.claimed_at < ?)
			  AND NOT EXISTS (SELECT 1 FROM conversation_message_queue earlier WHERE earlier.conversation_id = q.conversation_id AND earlier.status IN ('pending','claimed') AND (earlier.created_at < q.created_at OR (earlier.created_at = q.created_at AND earlier.id < q.id)))
 AND NOT EXISTS (SELECT 1 FROM steps held JOIN runs held_run ON held_run.id = held.run_id JOIN tasks held_task ON held_task.id = held_run.task_id WHERE held.organization_id = q.organization_id AND held_task.conversation_id = q.conversation_id AND held.lease_until <> '' AND julianday(held.lease_until) > julianday('now'))
 AND NOT EXISTS (SELECT 1 FROM side_effect_receipts unsettled JOIN runs effect_run ON effect_run.id = unsettled.run_id JOIN tasks effect_task ON effect_task.id = effect_run.task_id WHERE unsettled.organization_id = q.organization_id AND effect_task.conversation_id = q.conversation_id AND unsettled.status IN ('started', 'unknown'))
			  AND NOT EXISTS (
			      SELECT 1 FROM conversation_message_queue c
			      WHERE c.conversation_id = q.conversation_id AND c.status = 'claimed'
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM tasks t
			      WHERE t.conversation_id = q.conversation_id
			        AND COALESCE((SELECT r.status FROM runs r WHERE r.task_id = t.id ORDER BY r.created_at DESC, r.id DESC LIMIT 1), t.status) IN ('queued', 'running', 'waiting_approval', 'waiting_input', 'paused', 'blocked', 'unknown')
			  )
			ORDER BY q.created_at ASC, q.id ASC LIMIT 1
		)
		RETURNING id, organization_id, project_id, conversation_id, COALESCE(active_task_id, ''), content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id, claim_version
	`, now, stale).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt, &item.MessageID, &item.ClaimVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

func (s *Store) MarkConversationQueueItemConsumed(ctx context.Context, scope core.Scope, itemID string, versions ...int64) (core.ConversationQueueItem, error) {
	version := int64(0)
	if len(versions) > 0 {
		version = versions[0]
	}
	now := formatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'consumed', consumed_at = ?
		WHERE id = ? AND organization_id = ? AND status = 'claimed' AND claim_version = ?
	`, now, itemID, scope.OrganizationID, version)
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	return core.ConversationQueueItem{ID: itemID, Status: "consumed"}, nil
}

func (s *Store) PromoteConversationQueueItemToSteering(ctx context.Context, scope core.Scope, conversationID, itemID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET kind = 'steering'
		WHERE id = ? AND conversation_id = ? AND organization_id = ? AND status = 'pending' AND kind = 'follow_up'
		RETURNING id, organization_id, project_id, conversation_id, COALESCE(active_task_id, ''), content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id, claim_version
	`, itemID, conversationID, scope.OrganizationID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt, &item.MessageID, &item.ClaimVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

// RequeueConversationQueueItem restores a claimed item to pending after a
// failed start attempt. claimed_at keeps the attempt timestamp so idle drains
// back off instead of immediately retrying a failing item.
func (s *Store) RequeueConversationQueueItem(ctx context.Context, scope core.Scope, itemID string, versions ...int64) error {
	version := int64(0)
	if len(versions) > 0 {
		version = versions[0]
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'pending', claimed_at = ?
		WHERE id = ? AND organization_id = ? AND status = 'claimed' AND claim_version = ?
	`, formatTime(time.Now()), itemID, scope.OrganizationID, version)
	return err
}

func (s *Store) CancelConversationQueueItems(ctx context.Context, scope core.Scope, conversationID, taskID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'canceled'
		WHERE conversation_id = ? AND active_task_id = ? AND organization_id = ? AND status = 'pending'
	`, conversationID, taskID, scope.OrganizationID)
	return err
}

func (s *Store) CancelConversationQueueItem(ctx context.Context, scope core.Scope, conversationID, itemID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'canceled'
		WHERE id = ? AND conversation_id = ? AND organization_id = ? AND status = 'pending'
		RETURNING id, organization_id, project_id, conversation_id, COALESCE(active_task_id, ''), content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id, claim_version
	`, itemID, conversationID, scope.OrganizationID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt, &item.MessageID, &item.ClaimVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}
