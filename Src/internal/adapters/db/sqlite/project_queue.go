package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) QueueItemScope(ctx context.Context, item core.ConversationQueueItem) (core.Scope, error) {
	var scope core.Scope
	err := s.db.QueryRowContext(ctx, `SELECT q.organization_id, c.owner_user_id FROM conversation_message_queue q JOIN conversations c ON c.id = q.conversation_id WHERE q.id = ? AND q.organization_id = ? AND q.project_id = ? AND q.conversation_id = ?`, item.ID, item.OrganizationID, item.ProjectID, item.ConversationID).Scan(&scope.OrganizationID, &scope.UserID)
	return scope, err
}

// A canceled worker acknowledges that execution has returned before its old
// lease can be discarded. Unknown receipts still require human resolution.
func (s *Store) AcknowledgeStoppedStep(ctx context.Context, step core.ClaimedStep) error {
	_, err := s.db.ExecContext(ctx, `UPDATE steps SET lease_until = '' WHERE id = ? AND organization_id = ? AND status = 'canceled' AND owner_id = '' AND fence_token = ?`, step.ID, step.OrganizationID, step.FenceToken+1)
	return err
}

func (s *Store) ClaimProjectFollowUp(ctx context.Context, scope core.Scope, projectID, itemID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var created, claimed, consumed string
	err := s.db.QueryRowContext(ctx, `UPDATE conversation_message_queue SET status = 'claimed', claimed_at = ?, claim_version = claim_version + 1
WHERE id = (
 SELECT q.id FROM conversation_message_queue q
 WHERE q.organization_id = ? AND q.project_id = ? AND q.status = 'pending'
 AND (? = '' OR q.id = ?)
 AND NOT EXISTS (SELECT 1 FROM conversation_message_queue earlier WHERE earlier.conversation_id = q.conversation_id AND earlier.status IN ('pending','claimed') AND (earlier.created_at < q.created_at OR (earlier.created_at = q.created_at AND earlier.id < q.id)))
 AND NOT EXISTS (SELECT 1 FROM steps held JOIN runs held_run ON held_run.id = held.run_id JOIN tasks held_task ON held_task.id = held_run.task_id WHERE held.organization_id = q.organization_id AND held_task.conversation_id = q.conversation_id AND held.lease_until <> '' AND julianday(held.lease_until) > julianday('now'))
 AND NOT EXISTS (SELECT 1 FROM side_effect_receipts unsettled JOIN runs effect_run ON effect_run.id = unsettled.run_id JOIN tasks effect_task ON effect_task.id = effect_run.task_id WHERE unsettled.organization_id = q.organization_id AND effect_task.conversation_id = q.conversation_id AND unsettled.status IN ('started', 'unknown'))
 AND NOT EXISTS (SELECT 1 FROM tasks t WHERE t.organization_id = q.organization_id AND t.conversation_id = q.conversation_id AND COALESCE((SELECT r.status FROM runs r WHERE r.task_id = t.id ORDER BY r.created_at DESC, r.id DESC LIMIT 1), t.status) IN ('queued','running','waiting_approval','waiting_input','paused','blocked','unknown'))
 AND NOT EXISTS (SELECT 1 FROM conversation_message_queue c WHERE c.organization_id = q.organization_id AND c.conversation_id = q.conversation_id AND c.status = 'claimed')
 ORDER BY q.created_at, q.id LIMIT 1
)
RETURNING id, organization_id, project_id, conversation_id, COALESCE(active_task_id,''), content, input_json, kind, status, created_at, claimed_at, consumed_at, message_id, claim_version`, formatTime(time.Now()), scope.OrganizationID, projectID, itemID, itemID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &created, &claimed, &consumed, &item.MessageID, &item.ClaimVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return item, core.ErrNoWork
	}
	if err != nil {
		return item, err
	}
	item.CreatedAt, _ = parseTime(created)
	item.ClaimedAt, _ = parseTime(claimed)
	item.ConsumedAt, _ = parseTime(consumed)
	return item, nil
}

func (s *Store) ConversationBlockingTask(ctx context.Context, scope core.Scope, projectID, conversationID string) (string, error) {
	if _, err := s.GetProject(ctx, scope, projectID); err != nil {
		return "", err
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT t.id FROM tasks t WHERE t.organization_id = ? AND t.project_id = ? AND t.conversation_id = ? AND (
 COALESCE((SELECT r.status FROM runs r WHERE r.task_id = t.id ORDER BY r.created_at DESC, r.id DESC LIMIT 1), t.status) IN ('queued','running','waiting_approval','waiting_input','paused','blocked','unknown')
 OR EXISTS (SELECT 1 FROM steps s JOIN runs r ON r.id = s.run_id WHERE r.task_id = t.id AND s.lease_until <> '' AND julianday(s.lease_until) > julianday('now'))
 OR EXISTS (SELECT 1 FROM side_effect_receipts receipt JOIN runs r ON r.id = receipt.run_id WHERE r.task_id = t.id AND receipt.status IN ('started','unknown'))
) ORDER BY t.created_at, t.id LIMIT 1`, scope.OrganizationID, projectID, conversationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}
