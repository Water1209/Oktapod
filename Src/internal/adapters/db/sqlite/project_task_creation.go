package sqlite

import (
	"context"
	"database/sql"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type taskCreationTxKey struct{}

func conversationStartAllowed(ctx context.Context, tx *sql.Tx, scope core.Scope, projectID, conversationID, exceptTask, claimID string, version int64) error {
	// An empty ID creates a new conversation, which has no predecessor.
	if conversationID == "" && claimID == "" {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps s JOIN runs r ON r.id = s.run_id JOIN tasks t ON t.id = r.task_id WHERE s.organization_id = ? AND s.project_id = ? AND t.conversation_id = ? AND s.lease_until <> '' AND julianday(s.lease_until) > julianday('now')`, scope.OrganizationID, projectID, conversationID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return &core.ResourceConflictError{Resource: "project", Reason: "previous execution has not stopped"}
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM side_effect_receipts s JOIN runs r ON r.id = s.run_id JOIN tasks t ON t.id = r.task_id WHERE s.organization_id = ? AND s.project_id = ? AND t.conversation_id = ? AND s.status IN ('started', 'unknown')`, scope.OrganizationID, projectID, conversationID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return &core.ResourceConflictError{Resource: "project", Reason: "side effects require reconciliation"}
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks t WHERE organization_id = ? AND project_id = ? AND conversation_id = ? AND id <> ? AND COALESCE((SELECT r.status FROM runs r WHERE r.task_id = t.id ORDER BY r.created_at DESC, r.id DESC LIMIT 1), t.status) IN ('queued','running','waiting_approval','waiting_input','paused','blocked','unknown')`, scope.OrganizationID, projectID, conversationID, exceptTask).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return &core.ResourceConflictError{Resource: "conversation", Reason: "conversation already has an active task"}
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_message_queue WHERE organization_id = ? AND project_id = ? AND conversation_id = ? AND status = 'claimed' AND kind = 'follow_up' AND id <> ?`, scope.OrganizationID, projectID, conversationID, claimID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return &core.ResourceConflictError{Resource: "conversation", Reason: "conversation task is starting"}
	}
	if claimID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversation_message_queue WHERE id = ? AND organization_id = ? AND project_id = ? AND conversation_id = ? AND status = 'claimed' AND claim_version = ?`, claimID, scope.OrganizationID, projectID, conversationID, version).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return &core.ResourceConflictError{Resource: "project", Reason: "queue claim is no longer current"}
		}
	}
	return nil
}

// The short SQLite write transaction fences baseline capture and Run creation
// together. Model execution never runs under this transaction.
func (s *Store) WithProjectTaskCreation(ctx context.Context, scope core.Scope, projectID string, input core.CreateTaskInput, key core.Idempotency, create func(context.Context) (core.Task, error)) (core.Task, error) {
	s.idempotencyMu.Lock()
	defer s.idempotencyMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET updated_at = updated_at WHERE id = ? AND organization_id = ?`, projectID, scope.OrganizationID); err != nil {
		return core.Task{}, err
	}
	var replay core.Task
	if found, err := replayIdempotency(ctx, tx, scope, "POST /projects/"+projectID+"/tasks", key, &replay); err != nil || found {
		return replay, err
	}
	if err := conversationStartAllowed(ctx, tx, scope, projectID, input.ConversationID, "", input.QueueClaimID, input.QueueClaimVersion); err != nil {
		return core.Task{}, err
	}
	task, err := create(context.WithValue(ctx, taskCreationTxKey{}, tx))
	if err != nil {
		return core.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Task{}, err
	}
	return task, nil
}
