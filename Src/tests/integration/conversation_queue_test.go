package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func queueTestScope() core.Scope {
	return core.Scope{OrganizationID: testOrgID, UserID: testUserID}
}

func enqueueTurn(t *testing.T, repository interface {
	EnqueueConversationTurn(context.Context, core.Scope, string, string, string, string, string, string) (core.ConversationQueueItem, error)
}, projectID, conversationID, taskID, content, kind string) core.ConversationQueueItem {
	t.Helper()
	inputJSON, err := json.Marshal(core.ProjectTurnInput{ConversationID: conversationID, Content: content, Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate})
	if err != nil {
		t.Fatalf("marshal queue input: %v", err)
	}
	item, err := repository.EnqueueConversationTurn(context.Background(), queueTestScope(), projectID, conversationID, taskID, content, string(inputJSON), kind)
	if err != nil {
		t.Fatalf("enqueue %s: %v", kind, err)
	}
	return item
}

func queuedTurnStarterForTest(repository interface {
	HasActiveTask(context.Context, core.Scope, string) (bool, string, error)
}, service *application.Service) okruntime.QueuedTurnStarter {
	return func(ctx context.Context, scope core.Scope, item core.ConversationQueueItem) (core.Task, error) {
		return service.StartQueuedConversationTurn(ctx, item)
	}
}

// backdateQueueAttempt moves an item's claimed_at two hours into the past so
// tests can cross the lease/retry window without sleeping.
func backdateQueueAttempt(t *testing.T, databasePath, itemID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stale := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE conversation_message_queue SET claimed_at = ? WHERE id = ?`, stale, itemID); err != nil {
		t.Fatalf("backdate queue attempt: %v", err)
	}
}

func newQueueMockProvider(t *testing.T, requests *[]string, firstToolCall bool) *httptest.Server {
	t.Helper()
	calls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-queue-test"}}})
		case "/v1/chat/completions":
			calls++
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read request: %v", err)
			}
			*requests = append(*requests, string(body))
			w.Header().Set("Content-Type", "application/json")
			if firstToolCall && calls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "provider-queue-1",
					"choices": []map[string]any{{"message": map[string]any{
						"role":    "assistant",
						"content": "先写第一个文件。",
						"tool_calls": []map[string]any{
							{"id": "provider-write-1", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": `{"path":"notes/first.txt","content":"first\n"}`}},
						},
					}}},
					"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 6},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "provider-queue-final",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "已按引导完成。"}}},
				"usage":   map[string]any{"prompt_tokens": 12, "completion_tokens": 6},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestConversationTurnQueuesWhileTaskActive(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Queue project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "第一个任务", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	queued, err := service.SubmitProjectTurn(context.Background(), project.ID, core.ProjectTurnInput{ConversationID: task.ConversationID, Content: "第二条消息排队", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "queued-turn-1")
	if err != nil {
		t.Fatalf("submit queued turn: %v", err)
	}
	if !queued.Queued || queued.QueueItem == nil || queued.Task == nil || queued.Task.ID != task.ID {
		t.Fatalf("queued result=%#v", queued)
	}
	pending, err := repository.ListConversationQueue(context.Background(), queueTestScope(), task.ConversationID, "pending")
	if err != nil || len(pending) != 1 || pending[0].Kind != "follow_up" || pending[0].Content != "第二条消息排队" {
		t.Fatalf("pending queue=%#v err=%v", pending, err)
	}
	steered, err := service.SubmitProjectTurn(context.Background(), project.ID, core.ProjectTurnInput{ConversationID: task.ConversationID, Content: "当前步骤后立即调整", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, QueueKind: "steering"}, "", "queued-steering-1")
	if err != nil || !steered.Queued || steered.QueueItem == nil || steered.QueueItem.Kind != "steering" {
		t.Fatalf("steering queue result=%#v err=%v", steered, err)
	}
	pending, err = repository.ListConversationQueue(context.Background(), queueTestScope(), task.ConversationID, "pending")
	if err != nil || len(pending) != 2 || pending[1].Kind != "steering" {
		t.Fatalf("pending queue after steering=%#v err=%v", pending, err)
	}
	canceled, err := service.CancelConversationQueueItem(context.Background(), task.ConversationID, pending[0].ID)
	if err != nil || canceled.Status != "canceled" {
		t.Fatalf("cancel queue item=%#v err=%v", canceled, err)
	}
	pending, err = repository.ListConversationQueue(context.Background(), queueTestScope(), task.ConversationID, "pending")
	if err != nil || len(pending) != 1 || pending[0].ID != steered.QueueItem.ID {
		t.Fatalf("pending queue after withdrawal=%#v err=%v", pending, err)
	}
	hasActive, activeID, err := repository.HasActiveTask(context.Background(), queueTestScope(), task.ConversationID)
	if err != nil || !hasActive || activeID != task.ID {
		t.Fatalf("hasActive=%v id=%s err=%v", hasActive, activeID, err)
	}
}

func TestConversationDoesNotTreatStaleTaskStatusAsActive(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Status truth project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "会阻塞的任务", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE runs SET status = 'blocked' WHERE id = ?`, task.RunID); err != nil {
		t.Fatalf("block run: %v", err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status = 'running' WHERE id = ?`, task.ID); err != nil {
		t.Fatalf("leave stale task: %v", err)
	}
	hasActive, activeID, err := repository.HasActiveTask(context.Background(), queueTestScope(), task.ConversationID)
	if err != nil {
		t.Fatalf("has active: %v", err)
	}
	if hasActive || activeID != "" {
		t.Fatalf("terminal Run must be authoritative, active=%v id=%q", hasActive, activeID)
	}
}

func TestConversationSteeringReplansAfterAtomicStep(t *testing.T) {
	t.Parallel()
	var requests []string
	provider := newQueueMockProvider(t, &requests, true)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Steering project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "原始目标：写报告", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	steering := enqueueTurn(t, repository, project.ID, task.ConversationID, task.ID, "把报告重点改成物理 AI", "steering")

	worker := okruntime.NewWorker("steering-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	afterSteer, err := service.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if afterSteer.Status != core.StatusRunning {
		t.Fatalf("task should be reactivated after steering, got %s", afterSteer.Status)
	}
	consumed, err := repository.ListConversationQueue(context.Background(), queueTestScope(), task.ConversationID, "consumed")
	if err != nil || len(consumed) != 1 || consumed[0].ID != steering.ID {
		t.Fatalf("consumed queue=%#v err=%v", consumed, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var replanCount, canceledCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM steps WHERE run_id = ? AND step_type = 'model_continue' AND input_json LIKE '%steering_replan%'`, afterSteer.RunID).Scan(&replanCount); err != nil || replanCount != 1 {
		t.Fatalf("replan steps=%d err=%v", replanCount, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM steps WHERE run_id = ? AND status = 'canceled' AND error_code = 'PLAN_SUPERSEDED'`, afterSteer.RunID).Scan(&canceledCount); err != nil || canceledCount < 1 {
		t.Fatalf("superseded steps=%d err=%v", canceledCount, err)
	}

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if completed.Status != core.StatusSucceeded || completed.ResultSummary != "已按引导完成。" {
		t.Fatalf("completed=%s summary=%q", completed.Status, completed.ResultSummary)
	}
	if len(requests) != 2 || !strings.Contains(requests[1], "把报告重点改成物理 AI") || !strings.Contains(requests[1], "重新规划") {
		t.Fatalf("steering was not included in the replan prompt: requests=%d", len(requests))
	}
	events, err := service.ListRunEvents(context.Background(), completed.RunID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.EventType] = true
	}
	if !seen["conversation.steered"] || !seen["model.replan_queued"] {
		t.Fatalf("missing steering events: %#v", seen)
	}
	messages, err := service.ListMessages(context.Background(), task.ConversationID, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(messages) != 2 || messages[1].Content != "已按引导完成。" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestConversationFollowUpStartsSequentially(t *testing.T) {
	t.Parallel()
	var requests []string
	provider := newQueueMockProvider(t, &requests, false)
	defer provider.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Follow-up project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	first, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "第一个任务", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	enqueueTurn(t, repository, project.ID, first.ConversationID, first.ID, "第二个任务", "follow_up")
	enqueueTurn(t, repository, project.ID, first.ConversationID, first.ID, "第三个任务", "follow_up")

	worker := okruntime.NewWorker("follow-up-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).
		WithQueuedTurnStarter(queuedTurnStarterForTest(repository, service))

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("run first task: %v", err)
	}
	pending, err := repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "pending")
	if err != nil || len(pending) != 1 || pending[0].Content != "第三个任务" {
		t.Fatalf("pending after first=%#v err=%v", pending, err)
	}
	consumed, err := repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "consumed")
	if err != nil || len(consumed) != 1 || consumed[0].Content != "第二个任务" {
		t.Fatalf("consumed after first=%#v err=%v", consumed, err)
	}
	tasks, err := service.ListTasks(context.Background(), project.ID)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks after first=%d err=%v", len(tasks), err)
	}
	var secondTask core.Task
	for _, candidate := range tasks {
		if candidate.ID != first.ID {
			secondTask = candidate
		}
	}
	if secondTask.Goal != "第二个任务" || secondTask.ConversationID != first.ConversationID {
		t.Fatalf("second task=%#v", secondTask)
	}
	hasActive, activeID, err := repository.HasActiveTask(context.Background(), queueTestScope(), first.ConversationID)
	if err != nil || !hasActive || activeID != secondTask.ID {
		t.Fatalf("active after first=%v %s err=%v", hasActive, activeID, err)
	}

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("run second task: %v", err)
	}
	tasks, err = service.ListTasks(context.Background(), project.ID)
	if err != nil || len(tasks) != 3 {
		t.Fatalf("tasks after second=%d err=%v", len(tasks), err)
	}
	var thirdTask core.Task
	for _, candidate := range tasks {
		if candidate.ID != first.ID && candidate.ID != secondTask.ID {
			thirdTask = candidate
		}
	}
	if thirdTask.Goal != "第三个任务" {
		t.Fatalf("third task=%#v", thirdTask)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("run third task: %v", err)
	}
	pending, err = repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after third=%#v err=%v", pending, err)
	}
	final, err := service.GetTask(context.Background(), thirdTask.ID)
	if err != nil || final.Status != core.StatusSucceeded {
		t.Fatalf("third task status=%s err=%v", final.Status, err)
	}
}

func TestTerminalSteeringSuccessorPreservesGoalAndExplicitSkill(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Steering successor", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "research-method", ManifestJSON: `{"description":"research deeply","instructions":"Collect evidence, then verify the deliverable.","permissions":["workspace.read"]}`}, "steering-successor-skill")
	if err != nil {
		t.Fatal(err)
	}
	skill, err = service.ReviewCapability(context.Background(), skill.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "trusted test skill"}, "steering-successor-review", "steering-successor-review")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "研究 Physical AI 并交付 30000 字报告", Acceptance: "必须真实写入文件", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, SkillID: skill.ID, SkillArgs: "primary sources"}, "steering-successor-first", "")
	if err != nil {
		t.Fatal(err)
	}
	item := enqueueTurn(t, repository, project.ID, first.ConversationID, first.ID, "停止继续检索，立即写报告", "steering")
	if _, err := service.ControlTask(context.Background(), first.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "steering-successor-cancel", "steering-successor-cancel"); err != nil {
		t.Fatal(err)
	}
	second, err := service.StartQueuedConversationTurn(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Goal, first.Goal) || !strings.Contains(second.Goal, item.Content) || second.Acceptance != first.Acceptance {
		t.Fatalf("steering successor lost source contract: %#v", second)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var inputJSON string
	if err := db.QueryRow(`SELECT s.input_json FROM steps s JOIN runs r ON r.id = s.run_id WHERE r.task_id = ? ORDER BY s.position LIMIT 1`, second.ID).Scan(&inputJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inputJSON, skill.ID) || !strings.Contains(inputJSON, "primary sources") {
		t.Fatalf("steering successor did not inherit explicit Skill: %s", inputJSON)
	}
}

func TestNaturalLanguageContinuationUpdatePreservesTerminalTaskContract(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Natural continuation", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "research-method", ManifestJSON: `{"description":"research deeply","instructions":"Collect evidence, write, and verify.","permissions":["workspace.read"]}`}, "natural-continuation-skill")
	if err != nil {
		t.Fatal(err)
	}
	skill, err = service.ReviewCapability(context.Background(), skill.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "trusted test skill"}, "natural-continuation-review", "natural-continuation-review")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "研究 Physical AI 并交付报告", Acceptance: "报告必须写入工作区", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, SkillID: skill.ID, SkillArgs: "primary sources"}, "natural-continuation-first", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ControlTask(context.Background(), first.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "natural-continuation-cancel", "natural-continuation-cancel"); err != nil {
		t.Fatal(err)
	}
	result, err := service.SubmitProjectTurn(context.Background(), project.ID, core.ProjectTurnInput{ConversationID: first.ConversationID, Content: "继续当前任务。不要再检索，补足现有报告并核对总字数", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate}, "natural-continuation-turn", "natural-continuation-turn")
	if err != nil {
		t.Fatal(err)
	}
	if result.Task == nil || !strings.Contains(result.Task.Goal, first.Goal) || !strings.Contains(result.Task.Goal, "不要再检索") || result.Task.Acceptance != first.Acceptance {
		t.Fatalf("continuation lost source contract: %#v", result.Task)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var inputJSON, configSnapshotJSON string
	if err := db.QueryRow(`SELECT s.input_json, r.config_snapshot_json FROM steps s JOIN runs r ON r.id = s.run_id WHERE r.task_id = ? ORDER BY s.position LIMIT 1`, result.Task.ID).Scan(&inputJSON, &configSnapshotJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inputJSON, skill.ID) || !strings.Contains(inputJSON, "primary sources") {
		t.Fatalf("natural-language continuation lost explicit Skill: %s", inputJSON)
	}
	if !strings.Contains(configSnapshotJSON, first.RunID) || !strings.Contains(configSnapshotJSON, "continuation_run_ids") {
		t.Fatalf("natural-language continuation lost evidence provenance: %s", configSnapshotJSON)
	}
}

func TestConversationFollowUpFailureRequeuesAndIdleDrainRetries(t *testing.T) {
	t.Parallel()
	var requests []string
	provider := newQueueMockProvider(t, &requests, false)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	first, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "第一个任务", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	item := enqueueTurn(t, repository, project.ID, first.ConversationID, first.ID, "短暂失败后恢复的后续任务", "follow_up")

	attempts := 0
	delegate := queuedTurnStarterForTest(repository, service)
	starter := func(ctx context.Context, scope core.Scope, queued core.ConversationQueueItem) (core.Task, error) {
		attempts++
		if attempts == 1 {
			return core.Task{}, errors.New("transient task creation failure")
		}
		return delegate(ctx, scope, queued)
	}
	// A one-hour lease doubles as the retry backoff: after the failure below the
	// idle drain must not retry until the window has elapsed.
	worker := okruntime.NewWorker("retry-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).
		WithQueuedTurnStarter(starter).
		WithLease(time.Hour)

	// The first task completes; the follow-up start fails and must return to
	// pending instead of being consumed or lost.
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("run first task: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("starter attempts=%d, want 1", attempts)
	}
	pending, err := repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "pending")
	if err != nil || len(pending) != 1 || pending[0].ID != item.ID {
		t.Fatalf("pending after failure=%#v err=%v", pending, err)
	}
	consumed, err := repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "consumed")
	if err != nil || len(consumed) != 0 {
		t.Fatalf("consumed after failure=%#v err=%v", consumed, err)
	}
	tasks, err := service.ListTasks(context.Background(), project.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks after failure=%d err=%v", len(tasks), err)
	}

	// Within the backoff window the idle drain must not hot-retry the failure.
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("backoff run: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("starter retried during backoff: attempts=%d", attempts)
	}

	// Change the project model before the retry so the follow-up proves it
	// freezes the latest project configuration, not the old task's snapshot.
	updatedModel := "model-queue-test-v2"
	if _, err := service.UpdateProjectSettings(context.Background(), project.ID, core.UpdateProjectSettingsInput{DefaultModel: &updatedModel}, "queue-retry-model"); err != nil {
		t.Fatalf("update project model: %v", err)
	}
	// Backdate the attempt marker so the retry window counts as elapsed.
	backdateQueueAttempt(t, databasePath, item.ID)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("idle drain run: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("starter attempts after idle drain=%d, want 2", attempts)
	}
	pending, err = repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after retry=%#v err=%v", pending, err)
	}
	consumed, err = repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "consumed")
	if err != nil || len(consumed) != 1 || consumed[0].ID != item.ID {
		t.Fatalf("consumed after retry=%#v err=%v", consumed, err)
	}
	tasks, err = service.ListTasks(context.Background(), project.ID)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks after retry=%d err=%v", len(tasks), err)
	}
	var secondTask core.Task
	for _, candidate := range tasks {
		if candidate.ID != first.ID {
			secondTask = candidate
		}
	}
	if secondTask.Goal != "短暂失败后恢复的后续任务" || secondTask.ConversationID != first.ConversationID {
		t.Fatalf("retried task=%#v", secondTask)
	}

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("run retried task: %v", err)
	}
	if len(requests) != 2 || !strings.Contains(requests[1], "model-queue-test-v2") {
		t.Fatalf("follow-up did not use latest project model: requests=%d", len(requests))
	}
	if !strings.Contains(requests[1], "第一个任务") || !strings.Contains(requests[1], "已按引导完成") || !strings.Contains(requests[1], "短暂失败后恢复的后续任务") {
		t.Fatalf("follow-up lost compacted conversation context: %s", requests[1])
	}
}

func TestConversationIdleDrainRecoversClaimedItems(t *testing.T) {
	t.Parallel()
	var requests []string
	provider := newQueueMockProvider(t, &requests, false)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Crash recovery project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	first, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "第一个任务", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatalf("create first task: %v", err)
	}
	worker := okruntime.NewWorker("crash-recovery-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).
		WithQueuedTurnStarter(queuedTurnStarterForTest(repository, service)).
		WithLease(time.Hour)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("run first task: %v", err)
	}

	// Simulate a worker crash between claim and consume: the item stays claimed
	// and would be stranded without the idle drain's stale-claim reset.
	item := enqueueTurn(t, repository, project.ID, first.ConversationID, first.ID, "崩溃前未消费的消息", "follow_up")
	if _, err := repository.ClaimNextFollowUp(context.Background(), queueTestScope(), first.ConversationID); err != nil {
		t.Fatalf("manual claim: %v", err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("fresh claim must not be reset: %v", err)
	}
	claimed, err := repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "claimed")
	if err != nil || len(claimed) != 1 || claimed[0].ID != item.ID {
		t.Fatalf("freshly claimed item must survive the drain: %#v err=%v", claimed, err)
	}

	// The claim ages past the lease: the next idle run must reset and start it.
	backdateQueueAttempt(t, databasePath, item.ID)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	consumed, err := repository.ListConversationQueue(context.Background(), queueTestScope(), first.ConversationID, "consumed")
	if err != nil || len(consumed) != 1 || consumed[0].ID != item.ID {
		t.Fatalf("consumed after recovery=%#v err=%v", consumed, err)
	}
	tasks, err := service.ListTasks(context.Background(), project.ID)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks after recovery=%d err=%v", len(tasks), err)
	}
}

func TestConversationSteeringPromotionScopedToConversation(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Scope project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "范围校验任务", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	item := enqueueTurn(t, repository, project.ID, task.ConversationID, task.ID, "排队消息", "follow_up")
	if _, err := service.PromoteConversationQueueToSteering(context.Background(), "019f0000-0000-7000-8000-000000000000", item.ID); err == nil {
		t.Fatal("promote in another conversation should fail")
	}
	promoted, err := service.PromoteConversationQueueToSteering(context.Background(), task.ConversationID, item.ID)
	if err != nil || promoted.Kind != "steering" {
		t.Fatalf("promoted=%#v err=%v", promoted, err)
	}
}

func TestConversationAbortDropsSteeringAndPreservesNextTurn(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Abort queue", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "active work", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	enqueueTurn(t, repository, project.ID, task.ConversationID, task.ID, "当前步骤后调整", "steering")
	next := enqueueTurn(t, repository, project.ID, task.ConversationID, task.ID, "取消后继续处理", "follow_up")

	if _, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "abort-queue", "abort-queue"); err != nil {
		t.Fatal(err)
	}
	pending, err := service.ListConversationQueue(context.Background(), task.ConversationID, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != next.ID || pending[0].Kind != "follow_up" {
		t.Fatalf("abort pending queue=%#v; expected only preserved Next Turn", pending)
	}
}
