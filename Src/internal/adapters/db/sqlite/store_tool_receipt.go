package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sqlitegen "github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite/sqlc"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) ListApprovals(ctx context.Context, scope core.Scope, status string, limit int) ([]core.Approval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ar.id, ar.organization_id, ar.project_id, ar.task_id, ar.run_id,
		       ar.step_id, ar.tool_call_id, ar.tool_name,
		       COALESCE(NULLIF(ha.title, ''), ar.tool_name) AS tool_display_name,
		       ar.risk_level, ar.status,
		       ar.reason, ar.frozen_params_json, ar.input_hash, ar.requested_by,
		       ar.decided_by, ar.requested_at, ar.expires_at, ar.decided_at,
		       ar.approval_scope, ar.target_origin, ar.matched_rule_id, ar.policy_hash
		FROM approval_requests ar
		LEFT JOIN human_action_requests ha ON ha.id = ar.id
		JOIN tasks t ON t.id = ar.task_id
		JOIN memberships m ON m.organization_id = ar.organization_id
		JOIN project_memberships pm ON pm.project_id = ar.project_id AND pm.user_id = m.user_id
		WHERE ar.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)
		  AND (? = 'all' OR ar.status = ?)
		ORDER BY CASE ar.status WHEN 'pending' THEN 0 ELSE 1 END, ar.requested_at ASC, ar.id ASC
		LIMIT ?`,
		scope.OrganizationID, scope.UserID, scope.UserID, status, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	approvals := make([]core.Approval, 0)
	for rows.Next() {
		approval, scanErr := scanApproval(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

// ExpireApprovals deterministically rejects unattended pending approvals and
// converges the associated task/run to BLOCKED. It runs in one transaction so
// a stale approval can never remain actionable while its task is already
// terminal, and every expiry is represented in the Run Event stream.
func (s *Store) ExpireApprovals(ctx context.Context, now time.Time) (int, error) {
	nowText := formatTime(now.UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, organization_id, project_id, task_id, run_id, step_id, tool_call_id
		FROM approval_requests
		WHERE status = 'pending'
		  AND ((expires_at <> '' AND expires_at <= ?) OR (expires_at = '' AND requested_at <= ?))
		ORDER BY CASE WHEN expires_at = '' THEN requested_at ELSE expires_at END ASC, id ASC`, nowText, formatTime(now.UTC().Add(-core.DefaultApprovalTTL)))
	if err != nil {
		return 0, err
	}
	type expiredApproval struct{ id, organizationID, projectID, taskID, runID, stepID, toolCallID string }
	items := make([]expiredApproval, 0)
	for rows.Next() {
		var item expiredApproval
		if err := rows.Scan(&item.id, &item.organizationID, &item.projectID, &item.taskID, &item.runID, &item.stepID, &item.toolCallID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	q := s.queries.WithTx(tx)
	expiredCount := 0
	for _, item := range items {
		result, err := tx.ExecContext(ctx, `
			UPDATE approval_requests
			SET status = 'rejected', decided_by = '', decision_note = ?, decided_at = ?
			WHERE id = ? AND status = 'pending'`, "审批已过期，系统默认拒绝", nowText, item.id)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return 0, err
			}
			continue
		}
		expiredCount++
		if _, err := tx.ExecContext(ctx, `
			UPDATE human_action_requests
			SET status = 'expired', response_json = '{"decision":"rejected","reason":"expired"}',
			    decided_by = 'system', decided_at = ?, version = version + 1
			WHERE id = ? AND status IN ('waiting_user', 'reviewing')`, nowText, item.id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'rejected', error_code = 'APPROVAL_EXPIRED', updated_at = ? WHERE id = ?`, nowText, item.toolCallID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'canceled', error_code = 'APPROVAL_EXPIRED', owner_id = '', lease_until = '', updated_at = ? WHERE id = ? AND status = 'waiting_approval'`, nowText, item.stepID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'blocked', error_code = 'APPROVAL_EXPIRED', result_summary = ?, updated_at = ? WHERE id = ?`, "审批已过期，风险动作未执行", nowText, item.taskID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'blocked', updated_at = ? WHERE id = ?`, nowText, item.runID); err != nil {
			return 0, err
		}
		if err := s.blockToolBatchAfterApproval(ctx, tx, q, item.organizationID, item.projectID, item.runID, item.toolCallID, "APPROVAL_EXPIRED", nowText, "", item.id); err != nil {
			return 0, err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: item.organizationID, ProjectID: item.projectID, RunID: item.runID, EventType: "approval.expired", ActorType: "system", ActorID: "", CorrelationID: item.id, Visibility: core.VisibilityPrivate, Payload: map[string]any{"approval_id": item.id, "tool_call_id": item.toolCallID, "step_id": item.stepID, "decision": core.ApprovalRejected, "reason": "expired"}}); err != nil {
			return 0, err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: item.organizationID, ProjectID: item.projectID, RunID: item.runID, EventType: "human_action.expired", ActorType: "system", ActorID: "runtime", CorrelationID: item.id, Visibility: core.VisibilityPrivate, Payload: map[string]any{"human_action_id": item.id, "kind": core.HumanActionToolApproval, "status": core.HumanActionExpired}}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return expiredCount, nil
}

func (s *Store) blockToolBatchAfterApproval(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, organizationID, projectID, runID, toolCallID, errorCode, now, actorID, correlationID string) error {
	var batchID string
	if err := tx.QueryRowContext(ctx, `SELECT tool_batch_id FROM tool_calls WHERE id = ?`, toolCallID).Scan(&batchID); err != nil {
		return err
	}
	if batchID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = 'rejected', decided_by = ?, decision_note = ?, decided_at = ?
		WHERE status = 'pending'
		  AND tool_call_id IN (SELECT id FROM tool_calls WHERE tool_batch_id = ?)`,
		actorID, "同一工具批次中的审批被拒绝或过期，剩余动作已取消", now, batchID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE human_action_requests
		SET status = 'superseded',
		    response_json = '{"reason":"tool_batch_canceled"}',
		    decided_by = ?, decided_at = ?, version = version + 1
		WHERE status IN ('waiting_user', 'reviewing')
		  AND id IN (
		    SELECT ar.id FROM approval_requests ar
		    JOIN tool_calls tc ON tc.id = ar.tool_call_id
		    WHERE tc.tool_batch_id = ?
		  )`,
		actorID, now, batchID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tool_calls
		SET status = 'blocked', error_code = ?, completed_at = ?, updated_at = ?
		WHERE tool_batch_id = ? AND status IN ('planned', 'queued', 'waiting_approval')`,
		"BATCH_CANCELED_AFTER_"+errorCode, now, now, batchID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps
		SET status = 'canceled', error_code = ?, owner_id = '', lease_until = '', updated_at = ?
		WHERE tool_batch_id = ? AND status IN ('queued', 'waiting_approval')`,
		"BATCH_CANCELED_AFTER_"+errorCode, now, batchID,
	); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tool_batches SET status = 'blocked', updated_at = ? WHERE id = ? AND status <> 'blocked'`, now, batchID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	return s.appendEvent(ctx, q, eventInput{
		OrganizationID: organizationID,
		ProjectID:      projectID,
		RunID:          runID,
		EventType:      "tool.batch_completed",
		ActorType:      "system",
		ActorID:        actorID,
		CorrelationID:  correlationID,
		Visibility:     core.VisibilityPrivate,
		Payload: map[string]any{
			"batch_id":   batchID,
			"status":     "blocked",
			"error_code": errorCode,
		},
	})
}

func (s *Store) ListInboxHistory(ctx context.Context, scope core.Scope, limit int) ([]core.InboxHistoryItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.event_type, e.project_id, t.id, t.title, e.payload_json, e.occurred_at
		FROM run_events e
		JOIN runs r ON r.id = e.run_id
		JOIN tasks t ON t.id = r.task_id
		JOIN memberships m ON m.organization_id = e.organization_id
		JOIN project_memberships pm ON pm.project_id = e.project_id AND pm.user_id = m.user_id
		WHERE e.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (e.visibility = 'project' OR t.owner_user_id = ?)
		  AND e.event_type IN ('approval.decided', 'approval.expired', 'tool.side_effect_resolved', 'run.paused', 'run.resumed', 'run.retried', 'run.canceled', 'outcome.accepted')
		ORDER BY e.occurred_at DESC, e.id DESC
		LIMIT ?`, scope.OrganizationID, scope.UserID, scope.UserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]core.InboxHistoryItem, 0)
	for rows.Next() {
		var id, eventType, projectID, taskID, title, payloadJSON, occurredAtText string
		if err := rows.Scan(&id, &eventType, &projectID, &taskID, &title, &payloadJSON, &occurredAtText); err != nil {
			return nil, err
		}
		occurredAt, err := parseTime(occurredAtText)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return nil, fmt.Errorf("decode inbox history payload: %w", err)
		}
		kind, status, summary := inboxHistorySummary(eventType, payload)
		items = append(items, core.InboxHistoryItem{ID: id, Kind: kind, ProjectID: projectID, TaskID: taskID, Title: title, Summary: summary, Status: status, OccurredAt: occurredAt})
	}
	return items, rows.Err()
}

func inboxHistorySummary(eventType string, payload map[string]any) (kind, status, summary string) {
	stringValue := func(key string) string {
		value, _ := payload[key].(string)
		return value
	}
	switch eventType {
	case "approval.decided":
		decision := stringValue("decision")
		if decision == core.ApprovalApproved {
			if stringValue("approval_scope") == core.ApprovalScopeConversation {
				return "approval", decision, "本会话已允许同类动作，冻结参数保持不变"
			}
			if stringValue("approval_scope") == core.ApprovalScopeProject {
				return "approval", decision, "当前项目已允许同类窄规则动作 30 天，可随时撤销"
			}
			if stringValue("approval_scope") == core.ApprovalScopeRunOrigin {
				return "approval", decision, "当前 Run 已允许访问同一私网 origin，终态或 24 小时后自动失效"
			}
			if stringValue("approval_scope") == core.ApprovalScopeRunTool {
				return "approval", decision, "当前任务已允许使用同一个 MCP 工具，任务终态或 24 小时后自动失效"
			}
			return "approval", decision, "审批已批准一次，冻结参数保持不变"
		}
		return "approval", decision, "审批已拒绝，风险动作未执行"
	case "approval.expired":
		return "approval", core.ApprovalRejected, "审批已过期，系统默认拒绝"
	case "tool.side_effect_resolved":
		return "side_effect", stringValue("decision"), "未知副作用已由人工完成核查"
	case "run.paused":
		return "task", core.StatusPaused, "任务已暂停"
	case "run.resumed":
		return "task", core.StatusQueued, "任务已恢复排队"
	case "run.retried":
		return "task", core.StatusQueued, "任务已从安全点重试"
	case "run.canceled":
		return "task", core.StatusCanceled, "任务已取消"
	case "outcome.accepted":
		return "task", core.StatusSucceeded, "任务结果已被人工验收"
	default:
		return "task", "", "人机协作状态已更新"
	}
}

func (s *Store) ListSideEffectReceipts(ctx context.Context, scope core.Scope, status string, limit int) ([]core.SideEffectReceipt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.organization_id, r.project_id, r.task_id, r.run_id,
		       r.step_id, r.tool_call_id, tc.tool_name, r.input_hash, r.idempotent,
		       r.status, r.error_code, r.network_class, r.matched_rule_id,
		       r.policy_hash, r.authorization_id, r.started_at, r.finished_at
		FROM side_effect_receipts r
		JOIN tool_calls tc ON tc.id = r.tool_call_id
		JOIN tasks t ON t.id = r.task_id
		JOIN memberships m ON m.organization_id = r.organization_id
		JOIN project_memberships pm ON pm.project_id = r.project_id AND pm.user_id = m.user_id
		WHERE r.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)
		  AND (? = 'all' OR r.status = ?)
		ORDER BY CASE r.status WHEN 'unknown' THEN 0 ELSE 1 END, r.started_at ASC, r.id ASC
		LIMIT ?`,
		scope.OrganizationID, scope.UserID, scope.UserID, status, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]core.SideEffectReceipt, 0)
	for rows.Next() {
		item, scanErr := scanSideEffectReceipt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ResolveSideEffect(ctx context.Context, scope core.Scope, receiptID string, input core.ResolveSideEffectInput, correlationID string, idempotency core.Idempotency) (core.SideEffectReceipt, error) {
	route := "POST /side-effect-receipts/" + receiptID + "/resolution"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.SideEffectReceipt
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.SideEffectReceipt
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	receipt, err := getSideEffectReceiptForUserTx(ctx, tx, scope, receiptID)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM project_memberships WHERE project_id = ? AND user_id = ? AND status = 'active'`, receipt.ProjectID, scope.UserID).Scan(&role); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if role != core.ProjectRoleOwner && role != core.ProjectRoleApprover {
		return core.SideEffectReceipt{}, &core.ForbiddenError{Message: "project members cannot resolve side effects"}
	}
	if receipt.Status != core.SideEffectReceiptUnknown {
		return core.SideEffectReceipt{}, &core.StateConflictError{Status: receipt.Status, Command: input.Decision}
	}
	var existingDecision string
	_ = tx.QueryRowContext(ctx, `SELECT decision FROM side_effect_resolutions WHERE receipt_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, receiptID).Scan(&existingDecision)
	if existingDecision != "" {
		return core.SideEffectReceipt{}, &core.StateConflictError{Status: existingDecision, Command: input.Decision}
	}
	run, err := getRunContextTx(ctx, tx, receipt.RunID)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	now := formatTime(time.Now())
	resolutionID, err := newID()
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO side_effect_resolutions (id, organization_id, project_id, task_id, run_id, receipt_id, decision, note, actor_user_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, resolutionID, receipt.OrganizationID, receipt.ProjectID, receipt.TaskID, receipt.RunID, receipt.ID, input.Decision, input.Note, scope.UserID, now); err != nil {
		return core.SideEffectReceipt{}, err
	}
	confirmed := input.Decision == core.SideEffectDecisionConfirmApplied
	receiptStatus := core.SideEffectReceiptFailed
	errorCode := "SIDE_EFFECT_" + strings.ToUpper(strings.TrimPrefix(input.Decision, "confirm_"))
	if confirmed {
		receiptStatus = core.SideEffectReceiptSucceeded
		errorCode = "MANUALLY_CONFIRMED"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE side_effect_receipts SET status = ?, error_code = ?, finished_at = ? WHERE id = ? AND status = 'unknown'`, receiptStatus, errorCode, now, receipt.ID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	actionStatus := core.HumanActionRejected
	if confirmed {
		actionStatus = core.HumanActionApproved
	}
	actionResponse, _ := json.Marshal(map[string]any{"decision": input.Decision, "note": input.Note})
	if _, err := tx.ExecContext(ctx, `
		UPDATE human_action_requests
		SET status = ?, response_json = ?, decided_by = ?, decided_at = ?, version = version + 1
		WHERE id = ? AND status IN ('waiting_user', 'reviewing')`,
		actionStatus, string(actionResponse), scope.UserID, now, receipt.ID,
	); err != nil {
		return core.SideEffectReceipt{}, err
	}
	toolStatus := core.StatusBlocked
	stepStatus := core.StatusBlocked
	taskStatus := core.StatusBlocked
	taskError := errorCode
	taskSummary := "副作用结果未确认执行，已由人工终止；如需重试，请 Fork 任务并重新审批。"
	if confirmed {
		toolStatus = core.StatusSucceeded
		stepStatus = core.StatusSucceeded
		taskStatus = core.StatusSucceeded
		taskError = ""
		taskSummary = "外部副作用已由用户人工确认完成。"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = ?, error_code = ?, updated_at = ? WHERE id = ?`, toolStatus, taskError, now, receipt.ToolCallID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	stepOutput, _ := json.Marshal(map[string]any{"manual_resolution": input.Decision, "note": input.Note, "receipt_id": receipt.ID})
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = ?, output_json = ?, error_code = ?, updated_at = ? WHERE id = ? AND status = 'blocked'`, stepStatus, string(stepOutput), taskError, now, receipt.StepID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE step_attempts SET status = ?, error_code = ?, finished_at = ? WHERE id = (SELECT id FROM step_attempts WHERE step_id = ? ORDER BY attempt_number DESC LIMIT 1)`, stepStatus, taskError, now, receipt.StepID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, error_code = ?, result_summary = ?, updated_at = ? WHERE id = ?`, taskStatus, taskError, taskSummary, now, receipt.TaskID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, taskStatus, now, receipt.RunID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	validationID, err := newID()
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	validationStatus := "failed"
	if confirmed {
		validationStatus = "passed"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO validation_results (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, validator, status, input_hash, details_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, validationID, receipt.OrganizationID, receipt.ProjectID, receipt.TaskID, receipt.RunID, receipt.StepID, receipt.ToolCallID, "manual_side_effect_resolution", validationStatus, receipt.InputHash, string(stepOutput), now); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendEvent(ctx, s.queries.WithTx(tx), eventInput{OrganizationID: receipt.OrganizationID, ProjectID: receipt.ProjectID, RunID: receipt.RunID, EventType: "tool.side_effect_resolved", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"receipt_id": receipt.ID, "tool_call_id": receipt.ToolCallID, "decision": input.Decision, "note": input.Note, "validation_status": validationStatus}}); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendEvent(ctx, s.queries.WithTx(tx), eventInput{OrganizationID: receipt.OrganizationID, ProjectID: receipt.ProjectID, RunID: receipt.RunID, EventType: "human_action.resolved", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": receipt.ID, "kind": core.HumanActionSideEffectResolution, "status": actionStatus, "decision": input.Decision}}); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendEvent(ctx, s.queries.WithTx(tx), eventInput{OrganizationID: receipt.OrganizationID, ProjectID: receipt.ProjectID, RunID: receipt.RunID, EventType: "step." + stepStatus, ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": receipt.StepID, "receipt_id": receipt.ID, "decision": input.Decision, "error_code": taskError}}); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendConversationMessage(ctx, tx, s.queries.WithTx(tx), run, "assistant", taskSummary, now); err != nil {
		return core.SideEffectReceipt{}, err
	}
	receipt.Status = receiptStatus
	receipt.ErrorCode = errorCode
	finishedAt, _ := parseTime(now)
	receipt.FinishedAt = &finishedAt
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, receipt); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.SideEffectReceipt{}, err
	}
	return receipt, nil
}

func (s *Store) DecideApproval(ctx context.Context, scope core.Scope, approvalID string, input core.DecideApprovalInput, correlationID string, idempotency core.Idempotency) (core.Approval, error) {
	route := "POST /approvals/" + approvalID + "/decision"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Approval
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Approval{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.Approval
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	approval, err := getApprovalForUser(ctx, tx, scope, approvalID)
	if err != nil {
		return core.Approval{}, err
	}
	var runOriginPlan core.PlannedTool
	var runOriginAuthorizationID string
	var runToolGrantKey, runToolPolicyHash, runToolAuthorizationID string
	if input.Decision == core.ApprovalApproved && approval.ToolName == core.ToolWebFetch {
		if input.Scope != core.ApprovalScopeRunOrigin {
			return core.Approval{}, &core.ValidationError{Field: "scope", Message: "private web_fetch approvals must use the current Run and origin scope"}
		}
		run, runErr := getRunContextTx(ctx, tx, approval.RunID)
		if runErr != nil {
			return core.Approval{}, runErr
		}
		var argumentsJSON string
		if err := tx.QueryRowContext(ctx, `SELECT arguments_json FROM tool_calls WHERE id = ?`, approval.ToolCallID).Scan(&argumentsJSON); err != nil {
			return core.Approval{}, err
		}
		arguments := map[string]any{}
		if err := json.Unmarshal([]byte(argumentsJSON), &arguments); err != nil {
			return core.Approval{}, &core.ValidationError{Field: "frozen_params", Message: "frozen web_fetch parameters are invalid"}
		}
		runOriginPlan, err = s.executionPolicy.EvaluateExecution(ctx,
			application.Principal{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID},
			application.ExecutionRequest{ProjectID: run.ProjectID, Call: core.ToolCall{ID: approval.ToolCallID, Name: approval.ToolName, Arguments: arguments}, ConfigSnapshot: run.ConfigSnapshotJSON},
		)
		if err != nil {
			return core.Approval{}, err
		}
		if runOriginPlan.Decision != core.PolicyAsk || runOriginPlan.ApprovalScope != core.ApprovalScopeRunOrigin ||
			runOriginPlan.ApprovalGrantKey == "" || runOriginPlan.ApprovalPolicyHash == "" || runOriginPlan.ApprovalOrigin == "" {
			return core.Approval{}, &core.StateConflictError{Status: runOriginPlan.Decision, Command: "approve private web origin"}
		}
	} else if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeRunOrigin {
		return core.Approval{}, &core.ValidationError{Field: "scope", Message: "Run origin approval is only valid for private web_fetch calls"}
	}
	if input.Decision == core.ApprovalApproved && strings.HasPrefix(approval.ToolName, "mcp__") {
		switch input.Scope {
		case core.ApprovalScopeOnce:
		case core.ApprovalScopeRunTool:
			run, runErr := getRunContextTx(ctx, tx, approval.RunID)
			if runErr != nil {
				return core.Approval{}, runErr
			}
			var ok bool
			runToolGrantKey, runToolPolicyHash, ok = application.MCPRunToolGrant(run.ConfigSnapshotJSON, approval.ToolName)
			if !ok {
				return core.Approval{}, &core.StateConflictError{Status: "snapshot_mismatch", Command: "approve current Run MCP tool"}
			}
			var storedGrantKey, storedPolicyHash string
			if err := tx.QueryRowContext(ctx, `SELECT approval_grant_key, policy_hash FROM approval_requests WHERE id = ?`, approval.ID).Scan(&storedGrantKey, &storedPolicyHash); err != nil {
				return core.Approval{}, err
			}
			if approval.ApprovalScope != core.ApprovalScopeRunTool || storedGrantKey != runToolGrantKey || storedPolicyHash != runToolPolicyHash {
				return core.Approval{}, &core.StateConflictError{Status: "policy_changed", Command: "approve current Run MCP tool"}
			}
		default:
			return core.Approval{}, &core.ValidationError{Field: "scope", Message: "MCP tools support one-time approval or exact-tool approval for the current Run"}
		}
	} else if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeRunTool {
		return core.Approval{}, &core.ValidationError{Field: "scope", Message: "run_tool approval is only valid for MCP tools"}
	}
	var projectRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM project_memberships WHERE project_id = ? AND user_id = ? AND status = 'active'`, approval.ProjectID, scope.UserID).Scan(&projectRole); err != nil {
		return core.Approval{}, err
	}
	if projectRole != core.ProjectRoleOwner && projectRole != core.ProjectRoleApprover {
		return core.Approval{}, &core.ForbiddenError{Message: "project members cannot decide approvals"}
	}
	if input.Scope == core.ApprovalScopeProject && projectRole != core.ProjectRoleOwner {
		return core.Approval{}, &core.ForbiddenError{Message: "only a project owner can create project approval rules"}
	}
	if approval.Status != core.ApprovalPending {
		if approval.Status != input.Decision {
			return core.Approval{}, &core.StateConflictError{Status: approval.Status, Command: input.Decision}
		}
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, 200, approval); err != nil {
			return core.Approval{}, err
		}
		if err := tx.Commit(); err != nil {
			return core.Approval{}, err
		}
		return approval, nil
	}
	now := formatTime(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = ?, decided_by = ?, decision_note = ?, decided_at = ?
		WHERE id = ? AND status = 'pending'`,
		input.Decision, scope.UserID, input.Note, now, approvalID,
	)
	if err != nil {
		return core.Approval{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return core.Approval{}, &core.StateConflictError{Status: approval.Status, Command: input.Decision}
	}
	humanStatus := core.HumanActionApproved
	if input.Decision == core.ApprovalRejected {
		humanStatus = core.HumanActionRejected
	}
	responseJSON, _ := json.Marshal(map[string]any{"decision": input.Decision, "scope": input.Scope, "note": input.Note})
	if _, err := tx.ExecContext(ctx, `
		UPDATE human_action_requests
		SET status = ?, response_json = ?, decided_by = ?, decided_at = ?, version = version + 1
		WHERE id = ? AND status IN ('waiting_user', 'reviewing')`,
		humanStatus, string(responseJSON), scope.UserID, now, approvalID,
	); err != nil {
		return core.Approval{}, err
	}
	taskStatus := core.StatusQueued
	errorCode := ""
	resultSummary := "已批准冻结的工具参数，等待 Worker 执行。"
	toolStatus := "queued"
	stepStatus := core.StatusQueued
	if input.Decision == core.ApprovalRejected {
		taskStatus = core.StatusBlocked
		errorCode = "APPROVAL_REJECTED"
		resultSummary = "审批被拒绝，风险动作未执行。"
		toolStatus = "rejected"
		stepStatus = core.StatusCanceled
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = ?, error_code = ?, updated_at = ? WHERE id = ?`, toolStatus, errorCode, now, approval.ToolCallID); err != nil {
		return core.Approval{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = ?, error_code = ?, owner_id = '', lease_until = '', updated_at = ? WHERE id = ? AND status = 'waiting_approval'`, stepStatus, errorCode, now, approval.StepID); err != nil {
		return core.Approval{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, error_code = ?, result_summary = ?, updated_at = ? WHERE id = ?`, taskStatus, errorCode, resultSummary, now, approval.TaskID); err != nil {
		return core.Approval{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, taskStatus, now, approval.RunID); err != nil {
		return core.Approval{}, err
	}
	planItemStatus := core.RunPlanItemRunning
	if input.Decision == core.ApprovalRejected {
		planItemStatus = core.RunPlanItemBlocked
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE run_plan_items SET status = ?, updated_at = ?,
		       completed_at = CASE WHEN ? = 'blocked' THEN ? ELSE completed_at END,
		       result_summary = CASE WHEN ? = 'blocked' THEN ? ELSE result_summary END
		WHERE id = (SELECT run_plan_item_id FROM human_action_requests WHERE id = ?)`,
		planItemStatus, now, planItemStatus, now, planItemStatus, resultSummary, approvalID,
	); err != nil {
		return core.Approval{}, err
	}
	q := s.queries.WithTx(tx)
	if input.Decision == core.ApprovalRejected {
		if err := s.blockToolBatchAfterApproval(ctx, tx, q, approval.OrganizationID, approval.ProjectID, approval.RunID, approval.ToolCallID, "APPROVAL_REJECTED", now, scope.UserID, correlationID); err != nil {
			return core.Approval{}, err
		}
	}
	if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeRunOrigin {
		ruleID, err := newID()
		if err != nil {
			return core.Approval{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO run_approval_rules
			(id, organization_id, project_id, run_id, tool_name, grant_type, grant_key,
			 policy_hash, canonical_origin, created_from_approval_id, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, 'run_origin', ?, ?, ?, ?, ?, ?, ?)`,
			ruleID, approval.OrganizationID, approval.ProjectID, approval.RunID, approval.ToolName,
			runOriginPlan.ApprovalGrantKey, runOriginPlan.ApprovalPolicyHash, runOriginPlan.ApprovalOrigin,
			approval.ID, scope.UserID, now, formatTime(approval.ExpiresAt)); err != nil {
			return core.Approval{}, err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM run_approval_rules
			WHERE run_id = ? AND tool_name = ? AND grant_type = 'run_origin'
			  AND grant_key = ? AND policy_hash = ? LIMIT 1`, approval.RunID, approval.ToolName,
			runOriginPlan.ApprovalGrantKey, runOriginPlan.ApprovalPolicyHash).Scan(&runOriginAuthorizationID); err != nil {
			return core.Approval{}, err
		}
	}
	if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeRunTool {
		ruleID, err := newID()
		if err != nil {
			return core.Approval{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO run_approval_rules
			(id, organization_id, project_id, run_id, tool_name, grant_type, grant_key,
			 policy_hash, canonical_origin, created_from_approval_id, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?, 'run_tool', ?, ?, '', ?, ?, ?, ?)`,
			ruleID, approval.OrganizationID, approval.ProjectID, approval.RunID, approval.ToolName,
			runToolGrantKey, runToolPolicyHash, approval.ID, scope.UserID, now, formatTime(approval.ExpiresAt)); err != nil {
			return core.Approval{}, err
		}
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM run_approval_rules
			WHERE run_id = ? AND tool_name = ? AND grant_type = 'run_tool'
			  AND grant_key = ? AND policy_hash = ? LIMIT 1`, approval.RunID, approval.ToolName,
			runToolGrantKey, runToolPolicyHash).Scan(&runToolAuthorizationID); err != nil {
			return core.Approval{}, err
		}
	}
	if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeConversation {
		var conversationID, argumentsJSON string
		if err := tx.QueryRowContext(ctx, `
			SELECT t.conversation_id, tc.arguments_json
			FROM tasks t
			JOIN tool_calls tc ON tc.id = ?
			WHERE t.id = ?`, approval.ToolCallID, approval.TaskID).Scan(&conversationID, &argumentsJSON); err != nil {
			return core.Approval{}, err
		}
		if ruleKey, matchJSON, ok := approvalRuleSignature(approval.ToolName, argumentsJSON); ok {
			ruleID, err := newID()
			if err != nil {
				return core.Approval{}, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO conversation_approval_rules
				(id, organization_id, project_id, conversation_id, tool_name, rule_key, created_from_approval_id, created_by, created_at, expires_at, rule_version, match_json, match_hash, risk_level)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 2, ?, ?, ?)`,
				ruleID, approval.OrganizationID, approval.ProjectID, conversationID, approval.ToolName, ruleKey,
				approval.ID, scope.UserID, now, formatTime(approval.ExpiresAt), matchJSON, ruleKey, approval.RiskLevel); err != nil {
				return core.Approval{}, err
			}
		}
	}
	if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeProject {
		var argumentsJSON string
		if err := tx.QueryRowContext(ctx, `SELECT arguments_json FROM tool_calls WHERE id = ?`, approval.ToolCallID).Scan(&argumentsJSON); err != nil {
			return core.Approval{}, err
		}
		if ruleKey, matchJSON, ok := approvalRuleSignature(approval.ToolName, argumentsJSON); ok {
			ruleID, err := newID()
			if err != nil {
				return core.Approval{}, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO project_approval_rules
				(id, organization_id, project_id, tool_name, rule_key, created_from_approval_id, created_by, created_at, expires_at, revoked_at, revoked_by, rule_version, match_json, match_hash, risk_level)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 2, ?, ?, ?)
				ON CONFLICT(project_id, tool_name, rule_key) DO UPDATE SET
					created_from_approval_id = excluded.created_from_approval_id,
					created_by = excluded.created_by,
					created_at = excluded.created_at,
					expires_at = excluded.expires_at,
					rule_version = excluded.rule_version,
					match_json = excluded.match_json,
					match_hash = excluded.match_hash,
					risk_level = excluded.risk_level,
					revoked_at = '',
					revoked_by = ''`,
				ruleID, approval.OrganizationID, approval.ProjectID, approval.ToolName, ruleKey,
				approval.ID, scope.UserID, now, formatTime(time.Now().UTC().Add(30*24*time.Hour)), matchJSON, ruleKey, approval.RiskLevel); err != nil {
				return core.Approval{}, err
			}
		}
	}
	decisionAuthorizationID := runOriginAuthorizationID
	if runToolAuthorizationID != "" {
		decisionAuthorizationID = runToolAuthorizationID
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: approval.OrganizationID, ProjectID: approval.ProjectID, RunID: approval.RunID, EventType: "approval.decided", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: core.VisibilityPrivate, Payload: map[string]any{"approval_id": approval.ID, "tool_call_id": approval.ToolCallID, "step_id": approval.StepID, "decision": input.Decision, "approval_scope": input.Scope, "input_hash": approval.InputHash, "parameters_changed": false, "target_origin": approval.TargetOrigin, "matched_rule_id": approval.MatchedRuleID, "policy_hash": approval.PolicyHash, "authorization_id": decisionAuthorizationID}}); err != nil {
		return core.Approval{}, err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: approval.OrganizationID, ProjectID: approval.ProjectID, RunID: approval.RunID, EventType: "human_action.resolved", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: core.VisibilityPrivate, Payload: map[string]any{"human_action_id": approval.ID, "kind": core.HumanActionToolApproval, "status": humanStatus, "decision": input.Decision, "scope": input.Scope}}); err != nil {
		return core.Approval{}, err
	}
	updated, err := getApprovalForUser(ctx, tx, scope, approvalID)
	if err != nil {
		return core.Approval{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, 200, updated); err != nil {
		return core.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Approval{}, err
	}
	return updated, nil
}
