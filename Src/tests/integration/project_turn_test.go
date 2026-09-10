package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type projectTurnModelStub struct {
	results []core.ModelResult
	prompts []string
}

func TestTurnAdmissionReplaysBeforeModelAndActiveTaskRouting(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{turnModelResult("answer", "one answer")}}
	service, _, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Admission", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	ask := core.ProjectTurnInput{Content: "explain the project", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, ask, "", "ask-key-0001")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.SubmitProjectTurnStream(t.Context(), project.ID, ask, "", "ask-key-0001", nil, nil)
	if err != nil || replay.Messages[0].ID != first.Messages[0].ID || len(model.prompts) != 1 {
		t.Fatalf("Ask replay invoked model or duplicated messages: %v", err)
	}
	taskInput := core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "implement isolated feature", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}
	task, err := service.SubmitProjectTurn(t.Context(), project.ID, taskInput, "", "task-key")
	if err != nil {
		t.Fatal(err)
	}
	replay, err = service.SubmitProjectTurnStream(t.Context(), project.ID, taskInput, "", "task-key", nil, nil)
	if err != nil || replay.Queued || replay.Task == nil || replay.Task.ID != task.Task.ID {
		t.Fatalf("Task replay routed into queue: %v", err)
	}
	taskInput.Content = "next independent feature"
	queued, err := service.SubmitProjectTurn(t.Context(), project.ID, taskInput, "", "queue-key")
	if err != nil {
		t.Fatal(err)
	}
	replay, err = service.SubmitProjectTurnStream(t.Context(), project.ID, taskInput, "", "queue-key", nil, nil)
	if err != nil || replay.QueueItem == nil || replay.QueueItem.ID != queued.QueueItem.ID {
		t.Fatalf("queue replay duplicated admission: %v", err)
	}
	taskInput.Content = "different input under the same key"
	_, err = service.SubmitProjectTurn(t.Context(), project.ID, taskInput, "", "queue-key")
	var conflict *core.IdempotencyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected content conflict, got %v", err)
	}
}

type streamingProjectTurnModelStub struct {
	classification core.ModelResult
	answer         core.ModelResult
	deltas         []string
}

const validCompactionCheckpoint = `## Goal
- 继续当前对话。

## Constraints & Preferences
- 保留第一轮约束。

## Progress
### Done
- [x] 第一轮已回答。

### In Progress
- [ ] 继续补充。

### Blocked
- (none)

## Key Decisions
- **保留约束**: 后续回答继续使用。

## Next Steps
1. 继续回答。

## Critical Context
- 第一轮约束。`

func (s *streamingProjectTurnModelStub) Complete(context.Context, string, string, int) (core.ModelResult, error) {
	return s.classification, nil
}

func (s *streamingProjectTurnModelStub) StreamTurn(_ context.Context, _, _ string, _ int, _, _ string, onDelta func(string) error) (core.ModelResult, error) {
	for _, delta := range s.deltas {
		if err := onDelta(delta); err != nil {
			return core.ModelResult{}, err
		}
	}
	return s.answer, nil
}

func (s *projectTurnModelStub) Complete(_ context.Context, _, prompt string, _ int) (core.ModelResult, error) {
	s.prompts = append(s.prompts, prompt)
	if len(s.results) == 0 {
		return core.ModelResult{}, errors.New("unexpected model call")
	}
	result := s.results[0]
	s.results = s.results[1:]
	return result, nil
}

func TestConversationManualCompactPersistsCheckpointAndKeepsOriginalMessages(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{
		turnModelResult("answer-1", "第一轮回答。"),
		turnModelResult("answer-2", "第二轮回答。"),
		turnModelResult("compact-1", validCompactionCheckpoint),
		turnModelResult("answer-3", "基于摘要继续回答。"),
	}}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	if _, err := service.UpdateConversationContextSettings(t.Context(), core.UpdateConversationContextSettingsInput{AutoCompact: false, TriggerTokens: 200, RetainTokens: 40, SummaryMaxTokens: 30}); err != nil {
		t.Fatalf("configure context: %v", err)
	}
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Compact workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: strings.Repeat("first constraint ", 30), Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "compact-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: strings.Repeat("second detail ", 20), Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "compact-turn-2"); err != nil {
		t.Fatal(err)
	}
	compacted, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "/compact preserve exact constraints", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "compact-turn-3")
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted.Messages) != 2 || !strings.Contains(compacted.Messages[1].Content, "上下文已压缩") {
		t.Fatalf("manual compact result=%#v", compacted)
	}
	messages, err := service.ListMessages(t.Context(), first.Conversation.ID, 0)
	if err != nil || len(messages) != 6 {
		t.Fatalf("original messages len=%d err=%v", len(messages), err)
	}
	if _, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "继续", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "compact-turn-4"); err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) != 4 || !strings.Contains(model.prompts[3], "<conversation-summary>") || !strings.Contains(model.prompts[3], "保留第一轮约束") {
		t.Fatalf("post-compaction prompt=%#v", model.prompts)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_compactions WHERE conversation_id = ?`, first.Conversation.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("compaction checkpoints=%d err=%v", count, err)
	}
	var compactionEntries, missingMessageEntries, throughEntries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_entries WHERE conversation_id = ? AND kind = 'compaction'`, first.Conversation.ID).Scan(&compactionEntries); err != nil || compactionEntries != 1 {
		t.Fatalf("compaction entries=%d err=%v", compactionEntries, err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM messages m
		WHERE m.conversation_id = ?
		  AND NOT EXISTS (
		      SELECT 1 FROM session_entries se
		      WHERE se.conversation_id = m.conversation_id
		        AND se.kind = 'message' AND se.ref_id = m.id
		  )`, first.Conversation.ID).Scan(&missingMessageEntries); err != nil || missingMessageEntries != 0 {
		t.Fatalf("messages without entries=%d err=%v", missingMessageEntries, err)
	}
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM conversation_model_calls
		WHERE conversation_id = ? AND purpose LIKE 'context_compaction:%'
		  AND through_entry_id <> ''`, first.Conversation.ID).Scan(&throughEntries); err != nil || throughEntries != 1 {
		t.Fatalf("compaction model calls with through entry=%d err=%v", throughEntries, err)
	}
	assertConversationCallCount(t, databasePath, first.Conversation.ID, 4)
}

func TestConversationAutoCompactRunsAfterDatabaseThreshold(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{
		turnModelResult("answer-auto-1", "第一轮回答。"),
		turnModelResult("compact-auto-1", validCompactionCheckpoint),
		turnModelResult("answer-auto-2", "第二轮回答。"),
		turnModelResult("compact-auto-2", validCompactionCheckpoint),
		turnModelResult("answer-auto-3", "第三轮回答。"),
	}}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	if _, err := service.UpdateConversationContextSettings(t.Context(), core.UpdateConversationContextSettingsInput{AutoCompact: true, TriggerTokens: 150, RetainTokens: 40, SummaryMaxTokens: 30}); err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Auto compact", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: strings.Repeat("alpha ", 80), Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "auto-compact-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: strings.Repeat("beta ", 80), Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "auto-compact-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "third", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "auto-compact-3"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var trigger string
	if err := db.QueryRow(`SELECT trigger FROM conversation_compactions WHERE conversation_id = ? ORDER BY compacted_through_seq DESC LIMIT 1`, first.Conversation.ID).Scan(&trigger); err != nil || trigger != "auto" {
		t.Fatalf("auto compaction trigger=%q err=%v", trigger, err)
	}
}

func TestConversationContextUsagePrefersLatestMeasuredAssistantCall(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).WithAdvisorModel(&projectTurnModelStub{results: []core.ModelResult{{
		CallID:     "measured-answer",
		Provider:   "stub",
		Model:      "stub-model",
		Content:    "measured",
		StopReason: core.StopReasonStop,
		Usage: core.ModelUsage{
			InputTokens:           90,
			OutputTokens:          10,
			CacheReadTokens:       30,
			ContextTokens:         120,
			UsageSemanticsVersion: "v2",
		},
	}}})
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Measured context", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "measure this context", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "measured-context-turn")
	if err != nil {
		t.Fatal(err)
	}
	usage, err := repository.LatestConversationContextUsage(t.Context(), core.Scope{OrganizationID: testOrgID, UserID: testUserID}, turn.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ContextTokens != 120 || usage.OutputTokens != 10 || usage.CacheReadTokens != 30 || usage.SemanticsVersion != "v2" {
		t.Fatalf("latest measured context usage=%#v", usage)
	}
}

func TestProjectTurnAutoCreatesPendingAgentTurnWithoutClassification(t *testing.T) {
	service, databasePath, closeStore := newProjectTurnService(t, &projectTurnModelStub{})
	defer closeStore()

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Private workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "解释一下项目上下文", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000201", "turn-chat-0001")
	if err != nil {
		t.Fatalf("submit chat turn: %v", err)
	}
	if result.Route != application.TurnRouteTask || result.Task == nil || len(result.Messages) != 1 {
		t.Fatalf("unexpected auto result: %#v", result)
	}
	if result.Task.Mode != core.ModeSingleAgent || result.Task.PresentationKind != core.TaskPresentationPendingAuto {
		t.Fatalf("auto task did not freeze the v2 presentation contract: %#v", result.Task)
	}
	tasks, err := service.ListTasks(t.Context(), project.ID)
	if err != nil || len(tasks) != 1 || tasks[0].ID != result.Task.ID {
		t.Fatalf("auto task list=%#v err=%v", tasks, err)
	}
	assertConversationCallCount(t, databasePath, result.Conversation.ID, 0)
	usage, err := service.GetProjectUsage(t.Context(), project.ID, time.Now().Add(-time.Hour))
	if err != nil || usage.ModelCalls != 0 {
		t.Fatalf("auto routing unexpectedly consumed model usage=%#v err=%v", usage, err)
	}
}

func TestProjectTurnWithSelectedAttachmentAlwaysCreatesFrozenTaskContext(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{
		turnModelResult("classification-chat", `{"route":"chat"}`),
	}}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	service.WithDocumentRuntime(healthyDocumentRuntime{})

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Attachment workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := service.UploadAsset(t.Context(), project.ID, core.UploadAssetInput{Name: "training.pdf", Visibility: core.VisibilityPrivate, MediaType: "application/pdf"}, strings.NewReader("%PDF-1.4\nfixture"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{
		Content:         "帮我总结这个附件",
		Mode:            core.ModeAuto,
		Visibility:      core.VisibilityPrivate,
		ContextAssetIDs: []string{asset.ID},
	}, "00000000-0000-7000-8000-000000000211", "turn-attachment-0001")
	if err != nil {
		t.Fatalf("submit attachment turn: %v", err)
	}
	if result.Route != application.TurnRouteTask || result.Task == nil || result.Task.Mode != core.ModeSingleAgent {
		t.Fatalf("attachment turn did not become a task: %#v", result)
	}
	if len(model.prompts) != 0 {
		t.Fatalf("attachment turn unexpectedly called the chat classifier: %#v", model.prompts)
	}
	messages, err := service.ListMessages(t.Context(), result.Conversation.ID, 0)
	if err != nil || len(messages) != 1 || len(messages[0].Presentation.Attachments) != 1 || messages[0].Presentation.Attachments[0].ID != asset.ID {
		t.Fatalf("attachment presentation was not persisted with the user message: messages=%#v err=%v", messages, err)
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var snapshotJSON string
	if err := db.QueryRow(`SELECT config_snapshot_json FROM runs WHERE id = ?`, result.Task.RunID).Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !containsSnapshotID(snapshot["context_asset_ids"], asset.ID) {
		t.Fatalf("attachment missing from frozen Run context: %#v", snapshot["context_asset_ids"])
	}
}

func TestProjectTurnAutoTaskCreatesTaskWithoutConfirmationState(t *testing.T) {
	service, databasePath, closeStore := newProjectTurnService(t, &projectTurnModelStub{})
	defer closeStore()

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Code workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "修改 README 并运行测试", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000202", "turn-task-0001")
	if err != nil {
		t.Fatalf("submit task turn: %v", err)
	}
	if result.Route != application.TurnRouteTask || result.Task == nil || result.Task.Status != core.StatusQueued || result.Task.PresentationKind != core.TaskPresentationPendingAuto {
		t.Fatalf("unexpected task result: %#v", result)
	}
	if len(result.Messages) != 1 || result.Messages[0].TaskID != result.Task.ID {
		t.Fatalf("task conversation messages=%#v", result.Messages)
	}
	tasks, err := service.ListTasks(t.Context(), project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task list=%#v err=%v", tasks, err)
	}
	assertConversationCallCount(t, databasePath, result.Conversation.ID, 0)
	usage, err := service.GetProjectUsage(t.Context(), project.ID, time.Now().Add(-time.Hour))
	if err != nil || usage.ModelCalls != 0 {
		t.Fatalf("auto task routing usage=%#v err=%v", usage, err)
	}
}

func TestProjectTurnAutoPersistsTaskWhenClassifierIsUnavailable(t *testing.T) {
	service, _, closeStore := newProjectTurnService(t, &projectTurnModelStub{})
	defer closeStore()

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Offline routing", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{
		Content:    "创建一个 HTML 页面，保存文件并测试网页",
		Mode:       core.ModeAuto,
		Visibility: core.VisibilityPrivate,
	}, "00000000-0000-7000-8000-000000000203", "turn-offline-0001")
	if err != nil {
		t.Fatalf("offline auto turn: %v", err)
	}
	if result.Route != application.TurnRouteTask || result.Task == nil || result.Task.ConversationID == "" {
		t.Fatalf("offline turn was not persisted as a resumable task: %#v", result)
	}
	messages, err := service.ListMessages(t.Context(), result.Task.ConversationID, 0)
	if err != nil || len(messages) != 1 || messages[0].Content != "创建一个 HTML 页面，保存文件并测试网页" {
		t.Fatalf("persisted offline messages=%#v err=%v", messages, err)
	}
}

func TestProjectTurnRejectsContextFreeExplicitRecoveryIntent(t *testing.T) {
	service, _, closeStore := newProjectTurnService(t, &projectTurnModelStub{})
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Empty thread", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "继续当前任务", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "", "empty-recovery")
	if err == nil || !strings.Contains(err.Error(), "没有可继续") {
		t.Fatalf("context-free recovery error=%v", err)
	}
	conversations, listErr := service.ListConversations(t.Context(), project.ID)
	if listErr != nil || len(conversations) != 0 {
		t.Fatalf("context-free recovery created conversation=%#v err=%v", conversations, listErr)
	}
	_, err = service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "继续", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "", "empty-bare-recovery")
	if err == nil || !strings.Contains(err.Error(), "没有可继续") {
		t.Fatalf("context-free bare recovery error=%v", err)
	}
}

func TestProjectTurnRecoveryIntentRetriesLatestFailedTaskInPlace(t *testing.T) {
	model := &projectTurnModelStub{}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	defer closeStore()

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Research workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "调研 token 云平台形成 md 文档", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "recovery-task", "")
	if err != nil {
		t.Fatal(err)
	}
	markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusFailed, core.StatusFailed)

	var route string
	var deltas []string
	result, err := service.SubmitProjectTurnStream(t.Context(), project.ID, core.ProjectTurnInput{
		ConversationID: task.ConversationID,
		Content:        "重新规划任务，继续",
		Mode:           core.ModeAuto,
		Visibility:     core.VisibilityPrivate,
	}, "00000000-0000-7000-8000-000000000204", "turn-recovery-0001", func(value string) error {
		route = value
		return nil
	}, func(value string) error {
		deltas = append(deltas, value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if route != application.TurnRouteTask || result.Route != application.TurnRouteTask || result.Task == nil {
		t.Fatalf("route=%q result=%#v", route, result)
	}
	if result.Task.ID != task.ID || result.Task.RunID != task.RunID || result.Task.Status != core.StatusQueued {
		t.Fatalf("recovery created or changed task identity: %#v", result.Task)
	}
	if got := strings.Join(deltas, ""); got != "正在从安全检查点重试当前任务；已确认成功的步骤不会重新执行。" {
		t.Fatalf("recovery acknowledgement=%q", got)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("recovery messages=%#v", result.Messages)
	}
	for _, message := range result.Messages {
		if message.TaskID != task.ID {
			t.Fatalf("recovery message is not attributed to task: %#v", message)
		}
	}
	tasks, err := service.ListTasks(t.Context(), project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("recovery created another task: tasks=%#v err=%v", tasks, err)
	}
	if len(model.prompts) != 0 {
		t.Fatalf("recovery unexpectedly called the chat model: prompts=%#v", model.prompts)
	}

	// Retrying the same HTTP request after the task has already moved to queued
	// must replay the same turn instead of falling through to chat routing.
	replayed, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{
		ConversationID: task.ConversationID,
		Content:        "重新规划任务，继续",
		Mode:           core.ModeAuto,
		Visibility:     core.VisibilityPrivate,
	}, "00000000-0000-7000-8000-000000000204", "turn-recovery-0001")
	if err != nil || replayed.Route != application.TurnRouteTask || replayed.Task == nil || replayed.Task.ID != task.ID {
		t.Fatalf("replayed recovery=%#v err=%v", replayed, err)
	}
	messages, err := service.ListMessages(t.Context(), task.ConversationID, 0)
	if err != nil || len(messages) != 3 {
		t.Fatalf("replayed recovery duplicated messages=%#v err=%v", messages, err)
	}
}

func TestProjectTurnNaturalLanguagePauseAndResumeBypassModel(t *testing.T) {
	model := &projectTurnModelStub{}
	service, _, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Directive workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "实现并验证任务控制", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "directive-task", "")
	if err != nil {
		t.Fatal(err)
	}
	paused, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: task.ConversationID, Content: "请暂停当前任务", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "directive-pause", "directive-pause")
	if err != nil || paused.Task == nil || paused.Task.ID != task.ID || paused.Task.Status != core.StatusPaused || paused.Queued {
		t.Fatalf("pause result=%#v err=%v", paused, err)
	}
	resumed, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: task.ConversationID, Content: "继续", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "directive-resume", "directive-resume")
	if err != nil || resumed.Task == nil || resumed.Task.ID != task.ID || resumed.Task.Status != core.StatusQueued || resumed.Queued {
		t.Fatalf("resume result=%#v err=%v", resumed, err)
	}
	if len(model.prompts) != 0 {
		t.Fatalf("task controls unexpectedly called model: %#v", model.prompts)
	}
}

func TestProjectTurnNaturalLanguageCancelCanClearQueue(t *testing.T) {
	model := &projectTurnModelStub{}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Cancel workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "执行主任务", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "cancel-directive-task", "")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: task.ConversationID, Content: "完成后生成总结", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, QueueKind: "follow_up"}, "cancel-directive-queue", "cancel-directive-queue")
	if err != nil || !queued.Queued {
		t.Fatalf("queue result=%#v err=%v", queued, err)
	}
	canceled, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: task.ConversationID, Content: "取消当前任务并清空队列", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "cancel-directive", "cancel-directive")
	if err != nil || canceled.Task == nil || canceled.Task.Status != core.StatusCanceled || canceled.Queued {
		t.Fatalf("cancel result=%#v err=%v", canceled, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_message_queue WHERE conversation_id = ? AND status = 'pending'`, task.ConversationID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("pending queue=%d", pending)
	}
	if len(model.prompts) != 0 {
		t.Fatalf("cancel unexpectedly called model: %#v", model.prompts)
	}
}

func TestProjectTurnContinueCanceledTaskCreatesSuccessorRun(t *testing.T) {
	model := &projectTurnModelStub{}
	service, _, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Successor workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "创建页面并完成浏览器验证", Acceptance: "页面可访问", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "successor-task", "")
	if err != nil {
		t.Fatal(err)
	}
	previous, err = service.ControlTask(t.Context(), previous.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "successor-cancel", "successor-cancel")
	if err != nil || previous.Status != core.StatusCanceled {
		t.Fatalf("cancel previous=%#v err=%v", previous, err)
	}
	continued, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: previous.ConversationID, Content: "恢复当前任务", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "successor-continue", "successor-continue")
	if err != nil || continued.Task == nil {
		t.Fatalf("continue result=%#v err=%v", continued, err)
	}
	if continued.Task.ID == previous.ID || continued.Task.RunID == previous.RunID || continued.Task.ConversationID != previous.ConversationID || continued.Task.Status != core.StatusQueued {
		t.Fatalf("successor identity=%#v previous=%#v", continued.Task, previous)
	}
	if !strings.Contains(continued.Task.Goal, previous.Goal) || !strings.Contains(continued.Task.Goal, "保留并检查当前工作区修改") {
		t.Fatalf("successor goal=%q", continued.Task.Goal)
	}
	if len(model.prompts) != 0 {
		t.Fatalf("continuation unexpectedly called model: %#v", model.prompts)
	}
}

func TestProjectTurnNaturalLanguageMCPInstallReturnsReviewDraft(t *testing.T) {
	model := &projectTurnModelStub{}
	service, _, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Resource workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "请安装 https://example.com/mcp MCP", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "resource-turn", "resource-turn")
	if err != nil {
		t.Fatal(err)
	}
	if result.Route != application.TurnRouteResource || result.ResourceInstallDraft == nil {
		t.Fatalf("result=%#v", result)
	}
	draft := result.ResourceInstallDraft
	if draft.MCPDefinition == nil || draft.MCPDefinition.Transport.Type != "streamable_http" || draft.Status != "validated" {
		t.Fatalf("draft=%#v", draft)
	}
	if len(model.prompts) != 0 {
		t.Fatalf("resource install unexpectedly called model: %#v", model.prompts)
	}
}

func TestProjectTurnAutoStreamDefersDeltasToDurableRunSSE(t *testing.T) {
	model := &streamingProjectTurnModelStub{
		classification: turnModelResult("classification-stream", `{"route":"chat"}`),
		answer:         turnModelResult("answer-stream", "分段回答完成。"),
		deltas:         []string{"分段", "回答", "完成。"},
	}
	service, _, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Streaming workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	var route string
	var deltas []string
	result, err := service.SubmitProjectTurnStream(t.Context(), project.ID, core.ProjectTurnInput{Content: "分段回答", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000203", "turn-stream-0001", func(value string) error {
		route = value
		return nil
	}, func(value string) error {
		deltas = append(deltas, value)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if route != application.TurnRouteTask || len(deltas) != 0 {
		t.Fatalf("route=%q deltas=%#v", route, deltas)
	}
	if result.Task == nil || result.Task.PresentationKind != core.TaskPresentationPendingAuto || len(result.Messages) != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func newProjectTurnService(t *testing.T, model application.AdvisorModel) (*application.Service, string, func()) {
	t.Helper()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	repository, err := sqlite.Open(t.Context(), databasePath, filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	if err := repository.Bootstrap(t.Context(), core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}); err != nil {
		repository.Close()
		t.Fatalf("bootstrap repository: %v", err)
	}
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).WithAdvisorModel(model)
	return service, databasePath, func() { _ = repository.Close() }
}

func turnModelResult(id, content string) core.ModelResult {
	return core.ModelResult{CallID: id, Provider: "stub", Endpoint: "local", Model: "stub-model", Content: content, Usage: core.ModelUsage{InputTokens: 10, OutputTokens: 3, Estimated: true}}
}

func assertConversationCallCount(t *testing.T, databasePath, conversationID string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open verification database: %v", err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_model_calls WHERE conversation_id = ?`, conversationID).Scan(&got); err != nil {
		t.Fatalf("count conversation calls: %v", err)
	}
	if got != want {
		t.Fatalf("conversation model calls=%d want=%d", got, want)
	}
}
