package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestMalformedToolArgumentsQueueAuditedModelRetry(t *testing.T) {
	t.Parallel()
	modelCalls := 0
	outputLimits := []int{}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "compat-model", "tool_calling": true}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		modelCalls++
		var requestBody struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(body, &requestBody); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		outputLimits = append(outputLimits, requestBody.MaxTokens)
		if modelCalls <= 3 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "malformed-call",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "bad-tool", "type": "function", "function": map[string]any{"name": core.ToolWebSearch, "arguments": `{"query":"cut`}}}}}},
				"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 4},
			})
			return
		}
		if !strings.Contains(string(body), "function.arguments") || !strings.Contains(string(body), "模型交互重试 3/3") || !strings.Contains(string(body), "逐段补全") {
			t.Fatalf("retry request missing repair guidance: %s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "recovered-call",
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "已恢复并完成任务，没有执行不完整工具调用。"}}},
			"usage":   map[string]any{"prompt_tokens": 14, "completion_tokens": 6},
		})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Compat User"}, core.Organization{ID: testOrgID, Name: "Compat Org"}, func() string { return "fake-compat-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Compatibility project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Complete after malformed tool arguments", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 5, MaxTokens: 131072}, "compat-retry-task", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("compat-retry-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-compat-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	for attempt := 0; attempt < 4; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded || modelCalls != 4 {
		t.Fatalf("task=%#v model_calls=%d err=%v", completed, modelCalls, err)
	}
	if got, want := outputLimits, []int{2048, 4096, 8192, 8192}; !slices.Equal(got, want) {
		t.Fatalf("output limits=%v, want %v", got, want)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	failedIndex, retryIndex, retryEvents := -1, -1, 0
	for index, event := range events {
		if event.EventType == "step.failed" && event.Payload["error_code"] == "PROVIDER_TOOL_ARGUMENTS_TRUNCATED" {
			failedIndex = index
		}
		if event.EventType == "model.retry_queued" {
			retryIndex = index
			retryEvents++
		}
	}
	if failedIndex < 0 || retryIndex <= failedIndex || retryEvents != 3 {
		t.Fatalf("retry was not auditable after the failed step: %#v", events)
	}
}
