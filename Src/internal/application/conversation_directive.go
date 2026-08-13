package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type conversationDirectiveAction string

const (
	conversationDirectivePause    conversationDirectiveAction = "pause"
	conversationDirectiveCancel   conversationDirectiveAction = "cancel"
	conversationDirectiveContinue conversationDirectiveAction = "continue"
	conversationDirectiveRetry    conversationDirectiveAction = "retry"
)

type conversationDirective struct {
	Action     conversationDirectiveAction
	ClearQueue bool
	Update     string
}

// resolveConversationDirective recognizes only short, explicit control
// utterances. Business requests that merely mention words such as "停止" or
// "继续" inside a longer sentence remain ordinary turns and can never mutate a
// Run through fuzzy matching.
func resolveConversationDirective(content string) (conversationDirective, bool) {
	if update, matched := resolveContinuationUpdate(content); matched {
		return conversationDirective{Action: conversationDirectiveContinue, Update: update}, true
	}
	if len([]rune(content)) > 64 {
		return conversationDirective{}, false
	}
	normalized := normalizeConversationDirective(content)
	for _, prefix := range []string{"麻烦帮我", "请帮我", "麻烦", "帮我", "请"} {
		if strings.HasPrefix(normalized, prefix) {
			normalized = strings.TrimPrefix(normalized, prefix)
			break
		}
	}
	for _, suffix := range []string{"谢谢", "一下", "吧"} {
		normalized = strings.TrimSuffix(normalized, suffix)
	}

	switch normalized {
	case "暂停当前任务", "暂停这个任务", "暂停任务", "pausethecurrenttask", "pausecurrenttask":
		return conversationDirective{Action: conversationDirectivePause}, true
	case "取消当前任务", "停止当前任务", "终止当前任务", "取消这个任务", "停止这个任务", "cancelthecurrenttask", "cancelcurrenttask", "stopthecurrenttask", "stopcurrenttask":
		return conversationDirective{Action: conversationDirectiveCancel}, true
	case "取消并清空队列", "取消当前任务并清空队列", "停止当前任务并清空队列", "cancelthecurrenttaskandclearthequeue", "cancelcurrenttaskandclearqueue":
		return conversationDirective{Action: conversationDirectiveCancel, ClearQueue: true}, true
	case "继续任务", "继续当前任务", "继续执行当前任务", "继续原任务", "恢复任务", "恢复当前任务", "从上次停止的位置继续当前任务", "resumetask", "resumethecurrenttask", "resumecurrenttask", "continuetask", "continuethecurrenttask", "continuecurrenttask":
		return conversationDirective{Action: conversationDirectiveContinue}, true
	case "重试", "重试任务", "重试当前任务", "重新执行", "重新执行任务", "重新规划任务继续", "重新规划并继续", "重新规划任务并继续", "重新归属任务继续", "从失败处继续", "retry", "retrytask", "retrythecurrenttask", "retrycurrenttask":
		return conversationDirective{Action: conversationDirectiveRetry}, true
	default:
		return conversationDirective{}, false
	}
}

func resolveContinuationUpdate(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{
		"继续当前任务", "恢复当前任务", "继续原任务", "继续执行当前任务", "从上次停止的位置继续当前任务",
		"continue current task", "continue the current task", "resume current task", "resume the current task",
	} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		remainder := strings.TrimLeft(strings.TrimSpace(trimmed[len(prefix):]), "，,。.;；：:！!？? \t\r\n")
		if strings.TrimSpace(remainder) == "" {
			return "", false
		}
		return strings.TrimSpace(remainder), true
	}
	return "", false
}

func normalizeConversationDirective(content string) string {
	normalized := strings.ToLower(strings.TrimSpace(content))
	replacer := strings.NewReplacer(
		" ", "", "\t", "", "\r", "", "\n", "",
		"，", "", ",", "", "。", "", ".", "", "；", "", ";", "",
		"：", "", ":", "", "！", "", "!", "", "？", "", "?", "",
	)
	return replacer.Replace(normalized)
}

func (s *Service) executeConversationDirective(
	ctx context.Context,
	projectID string,
	input core.ProjectTurnInput,
	content, conversationID, visibility string,
	directive conversationDirective,
	correlationID, idempotencyKey string,
	onRoute func(string) error,
	onDelta func(string) error,
) (core.ProjectTurnResult, error) {
	if conversationID == "" {
		message := "当前新 Thread 没有可控制的任务，请先发送完整目标"
		if directive.Action == conversationDirectiveContinue || directive.Action == conversationDirectiveRetry {
			message = "当前新 Thread 没有可继续的任务，请先发送完整目标"
		}
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "conversation_id", Message: message}
	}
	tasks, err := s.repository.ListTasks(ctx, s.ScopeFor(ctx), projectID, 100)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	var current *core.Task
	for index := range tasks {
		if tasks[index].ConversationID == conversationID {
			candidate := tasks[index]
			current = &candidate
			break
		}
	}
	if current == nil {
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "content", Message: "当前 Thread 还没有任务，请先描述要完成的目标"}
	}
	if onRoute != nil {
		if err := onRoute(TurnRouteTask); err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	if directive.Update != "" {
		return s.continueTaskWithUpdate(ctx, projectID, input, *current, directive.Update, correlationID, idempotencyKey, onDelta)
	}

	command, assistant, successor, err := taskDirectiveTransition(*current, directive)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	if successor {
		return s.continueCanceledTask(ctx, projectID, *current, correlationID, idempotencyKey, onDelta)
	}
	updated := *current
	if command != "" {
		updated, err = s.ControlTask(ctx, current.ID, core.ControlTaskInput{Command: command}, correlationID, idempotencyKey)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	if directive.ClearQueue {
		if err := s.repository.CancelConversationQueueItems(ctx, s.ScopeFor(ctx), conversationID, current.ID); err != nil {
			return core.ProjectTurnResult{}, err
		}
		assistant += "；排队中的后续消息也已撤回。"
	}
	if onDelta != nil {
		if err := onDelta(assistant); err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	turnInput := inputWithTurnDefaults(input, content, conversationID, visibility)
	turnInput.TaskID = updated.ID
	result, err := s.repository.AppendChatTurn(ctx, s.ScopeFor(ctx), projectID, turnInput, assistant, nil, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(struct {
		ProjectID string
		Input     core.ProjectTurnInput
	}{projectID, turnInput})})
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	result.Route = TurnRouteTask
	result.Task = &updated
	return result, nil
}

func (s *Service) continueTaskWithUpdate(
	ctx context.Context,
	projectID string,
	turnInput core.ProjectTurnInput,
	previous core.Task,
	update string,
	correlationID, idempotencyKey string,
	onDelta func(string) error,
) (core.ProjectTurnResult, error) {
	if previous.Status == core.StatusQueued || previous.Status == core.StatusRunning || previous.Status == core.StatusPaused || previous.Status == core.StatusWaitingApproval || previous.Status == core.StatusWaitingInput {
		queuedInput := inputWithTurnDefaults(turnInput, update, previous.ConversationID, previous.Visibility)
		queuedInput.Mode = previous.Mode
		inputJSON, err := json.Marshal(queuedInput)
		if err != nil {
			return core.ProjectTurnResult{}, fmt.Errorf("encode continuation update: %w", err)
		}
		item, err := s.repository.EnqueueConversationTurn(ctx, s.ScopeFor(ctx), projectID, previous.ConversationID, previous.ID, update, string(inputJSON), "steering", core.MessagePresentation{QueueKind: "steering"})
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), previous.ConversationID)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		messages, err := s.repository.ListMessages(ctx, s.ScopeFor(ctx), previous.ConversationID, 0, 200)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		return core.ProjectTurnResult{Route: TurnRouteTask, Conversation: conversation, Messages: messages, Task: &previous, Queued: true, QueueItem: &item}, nil
	}
	run, err := s.repository.GetRunContext(ctx, previous.RunID)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	continuation := continuationTaskInput(previous, run)
	continuation.Goal = previous.Goal + "\n\n用户后续要求：" + strings.TrimSpace(update)
	continuation.UserMessageContent = strings.TrimSpace(turnInput.Content)
	if strings.TrimSpace(turnInput.Acceptance) != "" {
		continuation.Acceptance = strings.TrimSpace(strings.TrimSpace(continuation.Acceptance) + "\n" + strings.TrimSpace(turnInput.Acceptance))
	}
	continuation.Title = previous.Title
	task, err := s.CreateTask(ctx, projectID, continuation, correlationID, idempotencyKey)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	assistant := "已基于原目标、原 Skill 和现有工作区创建后继任务；新要求已并入验收，不会重放已确认成功的副作用。"
	if onDelta != nil {
		if err := onDelta(assistant); err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), task.ConversationID)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	messages, err := s.repository.ListMessages(ctx, s.ScopeFor(ctx), task.ConversationID, 0, 200)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	return core.ProjectTurnResult{Route: TurnRouteTask, Conversation: conversation, Messages: messages, Task: &task}, nil
}

func taskDirectiveTransition(task core.Task, directive conversationDirective) (command, assistant string, successor bool, err error) {
	switch directive.Action {
	case conversationDirectiveCancel:
		switch task.Status {
		case core.StatusSucceeded:
			return "", "当前任务已经完成，无需取消。", false, nil
		case core.StatusFailed:
			return "", "当前任务已经失败并停止；可以直接说“重试当前任务”。", false, nil
		case core.StatusCanceled:
			return "", "当前任务已经取消，现有文件修改仍然保留。", false, nil
		default:
			return core.TaskCommandCancel, "当前任务已取消；已完成的文件修改保留，未开始的步骤不会执行。", false, nil
		}
	case conversationDirectivePause:
		switch task.Status {
		case core.StatusQueued, core.StatusRunning:
			return core.TaskCommandPause, "当前任务已暂停；现有修改和安全检查点均已保留。", false, nil
		case core.StatusPaused:
			return "", "当前任务已经暂停；现有修改和安全检查点均已保留。", false, nil
		case core.StatusWaitingApproval, core.StatusWaitingInput:
			return "", "当前任务正在等待你的操作，尚未继续执行；如需终止可说“取消当前任务”。", false, nil
		default:
			return "", "", false, &core.StateConflictError{Status: task.Status, Command: core.TaskCommandPause}
		}
	case conversationDirectiveRetry:
		switch task.Status {
		case core.StatusFailed, core.StatusBlocked:
			return core.TaskCommandRetry, "正在从安全检查点重试当前任务；已确认成功的步骤不会重新执行。", false, nil
		case core.StatusCanceled:
			return "", "", true, nil
		case core.StatusPaused:
			return core.TaskCommandResume, "正在恢复当前任务；将从现有检查点继续。", false, nil
		case core.StatusQueued, core.StatusRunning, core.StatusWaitingApproval, core.StatusWaitingInput:
			return "", "当前任务仍在进行，无需重试。", false, nil
		default:
			return "", "", false, &core.StateConflictError{Status: task.Status, Command: core.TaskCommandRetry}
		}
	case conversationDirectiveContinue:
		switch task.Status {
		case core.StatusPaused:
			return core.TaskCommandResume, "正在恢复当前任务；将从现有检查点继续。", false, nil
		case core.StatusFailed, core.StatusBlocked:
			return core.TaskCommandRetry, "正在从安全检查点继续当前任务；已确认成功的步骤不会重新执行。", false, nil
		case core.StatusCanceled:
			return "", "", true, nil
		case core.StatusQueued, core.StatusRunning:
			return "", "当前任务正在运行，无需恢复。", false, nil
		case core.StatusWaitingApproval, core.StatusWaitingInput:
			return "", "当前任务正在等待你的操作；完成当前请求后会自动继续。", false, nil
		case core.StatusSucceeded:
			return "", "当前任务已经完成；如有新目标可直接继续描述。", false, nil
		default:
			return "", "", false, &core.StateConflictError{Status: task.Status, Command: core.TaskCommandResume}
		}
	default:
		return "", "", false, &core.ValidationError{Field: "content", Message: "task control directive is not supported"}
	}
}

func (s *Service) continueCanceledTask(
	ctx context.Context,
	projectID string,
	previous core.Task,
	correlationID, idempotencyKey string,
	onDelta func(string) error,
) (core.ProjectTurnResult, error) {
	run, err := s.repository.GetRunContext(ctx, previous.RunID)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	continuation := continuationTaskInput(previous, run)
	continuation.Goal = fmt.Sprintf("继续此前已取消的任务。保留并检查当前工作区修改，只完成尚未完成的部分，不重放已确认成功的副作用。原目标：%s", previous.Goal)
	continuation.Title = previous.Title
	task, err := s.CreateTask(ctx, projectID, continuation, correlationID, idempotencyKey)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	assistant := "已创建后继任务；当前文件修改保持不变，新 Run 会先检查现状再继续未完成部分。"
	if onDelta != nil {
		if err := onDelta(assistant); err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), task.ConversationID)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	messages, err := s.repository.ListMessages(ctx, s.ScopeFor(ctx), task.ConversationID, 0, 200)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	return core.ProjectTurnResult{Route: TurnRouteTask, Conversation: conversation, Messages: messages, Task: &task}, nil
}

func continuationTaskInput(previous core.Task, run core.RunContext) core.CreateTaskInput {
	input := core.CreateTaskInput{
		ConversationID:     previous.ConversationID,
		Title:              previous.Title,
		Goal:               previous.Goal,
		Acceptance:         previous.Acceptance,
		Mode:               previous.Mode,
		Visibility:         previous.Visibility,
		Model:              run.RequestedModel.ModelID,
		ProviderID:         run.RequestedModel.ProviderID,
		Thinking:           run.Thinking,
		ContinuationRunIDs: continuationRunIDs(run),
	}
	var snapshot map[string]any
	if json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot) != nil {
		return input
	}
	input.PermissionMode, _ = snapshot["permission_mode"].(string)
	input.SceneOverride, _ = snapshot["scene"].(string)
	input.FallbackModel, _ = snapshot["fallback_model_override"].(string)
	input.AgentID, _ = snapshot["agent_id"].(string)
	input.TeamDefinitionID, _ = snapshot["team_definition_id"].(string)
	input.MaxModelCalls = snapshotInt64(snapshot["max_model_calls"])
	input.MaxTokens = snapshotInt64(snapshot["max_tokens"])
	input.ContextAssetIDs = snapshotStrings(snapshot["context_asset_ids"])
	input.ContextMemoryIDs = snapshotStrings(snapshot["context_memory_ids"])
	if explicit, ok := snapshot["explicit_skill_invocation"].(map[string]any); ok {
		input.SkillID, _ = explicit["skill_id"].(string)
		input.SkillArgs, _ = explicit["skill_args"].(string)
	}
	return input
}

func continuationRunIDs(run core.RunContext) []string {
	var snapshot map[string]any
	_ = json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot)
	ids := snapshotStrings(snapshot["continuation_run_ids"])
	ids = append(ids, run.RunID)
	result := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
		if len(result) == 8 {
			break
		}
	}
	return result
}

func snapshotInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}

func snapshotStrings(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, text)
		}
	}
	return result
}
