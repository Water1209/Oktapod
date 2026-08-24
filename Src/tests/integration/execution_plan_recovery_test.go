package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

type failingPlanSearch struct {
	calls int
}

func (f *failingPlanSearch) Execute(_ context.Context, call core.ToolCall) core.ToolExecution {
	f.calls++
	return core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ErrorCode: "WEB_SEARCH_REQUEST_FAILED", Output: map[string]any{"reason": "upstream timeout"}}
}

func TestExecutionPlanClaimsIndependentToolsInParallel(t *testing.T) {
	t.Parallel()
	provider := executionPlanProvider(t, false)
	defer provider.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Plan User"}, core.Organization{ID: testOrgID, Name: "Plan Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Sequential plan", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Run a two-step plan", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 4, MaxTokens: 32768}, "sequential-plan", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("plan-model-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("model planning worked=%v err=%v", worked, runErr)
	}
	first, err := repository.ClaimNextStep(context.Background(), "tool-worker-1", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil || first.StepType != core.ToolWebSearch {
		t.Fatalf("first planned step=%#v err=%v", first, err)
	}
	second, err := repository.ClaimNextStep(context.Background(), "tool-worker-2", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil || second.StepType != core.ToolWebSearch || second.ID == first.ID {
		t.Fatalf("parallel second planned step=%#v err=%v", second, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundPlan := false
	for _, event := range events {
		if event.EventType == "execution.plan_created" {
			items, _ := event.Payload["items"].([]any)
			foundPlan = len(items) == 2
		}
	}
	if !foundPlan {
		t.Fatalf("two-step execution plan event missing: %#v", events)
	}
}

func TestFailedToolReplansAndContinues(t *testing.T) {
	t.Parallel()
	provider := executionPlanProvider(t, true)
	defer provider.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Recovery User"}, core.Organization{ID: testOrgID, Name: "Recovery Org"}, func() string { return "fake-recovery-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Recovering plan", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Recover after a failed search", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 4, MaxTokens: 32768}, "recover-plan", "")
	if err != nil {
		t.Fatal(err)
	}
	search := &failingPlanSearch{}
	worker := okruntime.NewWorker("recovery-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-recovery-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWebSearchExecutor(search)
	for attempt := 0; attempt < 4; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	recovering, err := service.GetTask(context.Background(), task.ID)
	// barrier_v2 cancels the unstarted sibling in the failed safe group before
	// it queues recovery. The fourth iteration has already produced the next
	// durable frontier, which may be queued or running depending on whether a
	// worker has claimed it, but it must not replay the canceled search.
	if err != nil || (recovering.Status != core.StatusQueued && recovering.Status != core.StatusRunning) || search.calls != 1 {
		t.Fatalf("task=%#v search_calls=%d err=%v", recovering, search.calls, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	failedIndex, recoveryIndex, validationIndex := -1, -1, -1
	for index, event := range events {
		if event.EventType == "step.failed" && event.Payload["error_code"] == "WEB_SEARCH_REQUEST_FAILED" {
			failedIndex = index
		}
		if event.EventType == "tool.recovery_queued" {
			recoveryIndex = index
		}
		if event.EventType == "completion.validation_failed" && event.Payload["reason"] == "WEB_EVIDENCE_MISSING" {
			validationIndex = index
		}
	}
	if failedIndex < 0 || recoveryIndex <= failedIndex || validationIndex <= recoveryIndex {
		t.Fatalf("tool recovery or completion validation was not queued after the failed step: %#v", events)
	}
}

func executionPlanProvider(t *testing.T, recoverAfterFailure bool) *httptest.Server {
	t.Helper()
	modelCalls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-model", "tool_calling": true}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		modelCalls++
		if recoverAfterFailure && modelCalls == 2 {
			if !strings.Contains(string(body), "工具失败恢复 1/5") || strings.Count(string(body), "WEB_SEARCH_REQUEST_FAILED") < 2 {
				t.Errorf("recovery request missing failed receipt or replanning hint: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "recovered", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "已根据失败收据调整方案并完成目标。"}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
			return
		}
		calls := []map[string]any{
			transitionPlanToolCall("execution-plan-control", "收集两项独立证据", "并行取得两项证据，并在失败时依据收据恢复。"),
			{"id": "search-one", "type": "function", "function": map[string]any{"name": core.ToolWebSearch, "arguments": `{"query":"first evidence"}`}},
			{"id": "search-two", "type": "function", "function": map[string]any{"name": core.ToolWebSearch, "arguments": `{"query":"second evidence"}`}},
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "planned", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "先完成两项证据检索，再根据收据继续。", "tool_calls": calls}}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 10}})
	}))
}
