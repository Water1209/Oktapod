package integration_test

import (
	"context"
	"database/sql"
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

func TestProjectTurnAutoChatDoesNotCreateTask(t *testing.T) {
	service, databasePath, closeStore := newProjectTurnService(t, &projectTurnModelStub{results: []core.ModelResult{
		turnModelResult("classification-chat", `{"route":"chat"}`),
		turnModelResult("answer-chat", "这是一个纯会话回答。"),
	}})
	defer closeStore()

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Private workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "解释一下项目上下文", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000201", "turn-chat-0001")
	if err != nil {
		t.Fatalf("submit chat turn: %v", err)
	}
	if result.Route != application.TurnRouteChat || result.Task != nil || len(result.Messages) != 2 {
		t.Fatalf("unexpected chat result: %#v", result)
	}
	for _, message := range result.Messages {
		if message.TaskID != "" {
			t.Fatalf("chat message unexpectedly linked to task: %#v", message)
		}
	}
	tasks, err := service.ListTasks(t.Context(), project.ID)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("chat created tasks=%#v err=%v", tasks, err)
	}
	assertConversationCallCount(t, databasePath, result.Conversation.ID, 2)
	usage, err := service.GetProjectUsage(t.Context(), project.ID, time.Now().Add(-time.Hour))
	if err != nil || usage.ModelCalls != 2 || usage.InputTokens != 20 {
		t.Fatalf("chat usage=%#v err=%v", usage, err)
	}
}

func TestProjectTurnAutoTaskCreatesTaskWithoutConfirmationState(t *testing.T) {
	service, databasePath, closeStore := newProjectTurnService(t, &projectTurnModelStub{results: []core.ModelResult{
		turnModelResult("classification-task", `{"route":"task"}`),
	}})
	defer closeStore()

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Code workspace", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "修改 README 并运行测试", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000202", "turn-task-0001")
	if err != nil {
		t.Fatalf("submit task turn: %v", err)
	}
	if result.Route != application.TurnRouteTask || result.Task == nil || result.Task.Status != core.StatusQueued {
		t.Fatalf("unexpected task result: %#v", result)
	}
	if len(result.Messages) != 1 || result.Messages[0].TaskID != result.Task.ID {
		t.Fatalf("task conversation messages=%#v", result.Messages)
	}
	tasks, err := service.ListTasks(t.Context(), project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("task list=%#v err=%v", tasks, err)
	}
	assertConversationCallCount(t, databasePath, result.Conversation.ID, 1)
	usage, err := service.GetProjectUsage(t.Context(), project.ID, time.Now().Add(-time.Hour))
	if err != nil || usage.ModelCalls != 1 {
		t.Fatalf("task classification usage=%#v err=%v", usage, err)
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

func TestProjectTurnRejectsContextFreeRecoveryIntent(t *testing.T) {
	service, _, closeStore := newProjectTurnService(t, &projectTurnModelStub{})
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Empty thread", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "继续", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "", "empty-recovery")
	if err == nil || !strings.Contains(err.Error(), "没有可继续") {
		t.Fatalf("context-free recovery error=%v", err)
	}
	conversations, listErr := service.ListConversations(t.Context(), project.ID)
	if listErr != nil || len(conversations) != 0 {
		t.Fatalf("context-free recovery created conversation=%#v err=%v", conversations, listErr)
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
	if got := strings.Join(deltas, ""); got != "正在继续当前任务；原执行计划、Skill 和已完成进度保持不变。" {
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

func TestProjectTurnStreamEmitsRouteAndDeltasBeforePersisting(t *testing.T) {
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
	if route != application.TurnRouteChat || strings.Join(deltas, "") != "分段回答完成。" {
		t.Fatalf("route=%q deltas=%#v", route, deltas)
	}
	if len(result.Messages) != 2 || result.Messages[1].Content != "分段回答完成。" {
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
