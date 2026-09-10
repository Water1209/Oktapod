package sqlite

import (
	"context"
	"database/sql"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) ReplayProjectTurn(ctx context.Context, scope core.Scope, projectID string, key core.Idempotency) (core.ProjectTurnResult, bool, error) {
	var result core.ProjectTurnResult
	found, err := replayIdempotency(ctx, s.db, scope, "POST /projects/"+projectID+"/turns", key, &result)
	return result, found, err
}

func taskTurnReceipt(ctx context.Context, tx *sql.Tx, scope core.Scope, task core.Task) (core.ProjectTurnResult, error) {
	conversation, err := getConversation(ctx, tx, scope, task.ConversationID)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	messages, err := listMessages(ctx, tx, scope, task.ConversationID, 0, 200)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	return core.ProjectTurnResult{Route: "task", Conversation: conversation, Messages: messages, Task: &task}, nil
}
