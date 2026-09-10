package application

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type projectQueueRepository interface {
	ClaimProjectFollowUp(context.Context, core.Scope, string, string) (core.ConversationQueueItem, error)
	ConversationBlockingTask(context.Context, core.Scope, string, string) (string, error)
}

func (s *Service) admitProjectTaskTurn(ctx context.Context, projectID string, input core.ProjectTurnInput, presentation core.MessagePresentation) (core.ProjectTurnResult, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	scope := s.ScopeFor(ctx)
	item, err := s.repository.EnqueueConversationTurn(ctx, scope, projectID, input.ConversationID, "", input.Content, string(encoded), "follow_up", presentation, input.Admission)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	var task *core.Task
	blocking := ""
	if queue, ok := s.repository.(projectQueueRepository); ok {
		claimed, claimErr := queue.ClaimProjectFollowUp(ctx, scope, projectID, item.ID)
		if claimErr == nil {
			started, startErr := s.StartQueuedConversationTurn(ctx, claimed)
			if startErr == nil {
				task = &started
				item.Status = "consumed"
			} else {
				_ = s.repository.RequeueConversationQueueItem(ctx, scope, claimed.ID, claimed.ClaimVersion)
			}
		} else if !errors.Is(claimErr, core.ErrNoWork) {
			return core.ProjectTurnResult{}, claimErr
		}
		blocking, err = queue.ConversationBlockingTask(ctx, scope, projectID, item.ConversationID)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	conversation, err := s.repository.GetConversation(ctx, scope, item.ConversationID)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	messages, err := s.repository.ListMessages(ctx, scope, item.ConversationID, 0, 200)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	result := core.ProjectTurnResult{Route: TurnRouteTask, Conversation: conversation, Messages: messages, Task: task}
	if task == nil {
		result.Queued, result.QueueItem, result.QueueReason, result.BlockingTaskID = true, &item, "conversation_busy", blocking
	}
	return result, nil
}
