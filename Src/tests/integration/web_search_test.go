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
	"github.com/oktapod/oktapod/Src/internal/adapters/websearch"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestWebSearchToolExecutesAndSynthesizesCitedAnswer(t *testing.T) {
	t.Parallel()
	searchProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Query().Get("q"), "Oktapod current") {
			t.Fatalf("search query=%q", r.URL.Query().Get("q"))
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><div class="result"><a class="result__a" href="https://docs.example.test/oktapod">Oktapod docs</a><a class="result__snippet">Current verified documentation.</a></div></body></html>`))
	}))
	defer searchProvider.Close()

	modelCalls := 0
	modelProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"search-model","tool_calling":true}]}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		modelCalls++
		w.Header().Set("Content-Type", "application/json")
		if modelCalls == 1 {
			if !strings.Contains(string(body), `"name":"web_search"`) {
				t.Fatalf("web_search schema missing: %s", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "search-plan", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "search-call", "type": "function", "function": map[string]any{"name": core.ToolWebSearch, "arguments": `{"query":"Oktapod current","max_results":5}`}}}}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
			return
		}
		if !strings.Contains(string(body), "https://docs.example.test/oktapod") || !strings.Contains(string(body), `"tools"`) {
			t.Fatalf("continuation request missing evidence or executable tool loop: %s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "search-synthesis", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "已基于搜索结果核对当前信息。\n\n来源：\n- [Oktapod docs](https://docs.example.test/oktapod)"}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
	}))
	defer modelProvider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, modelProvider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Search User"}, core.Organization{ID: testOrgID, Name: "Search Org"}, func() string { return "fake-search-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Search project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Research current Oktapod information", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 2, MaxTokens: 8192}, "web-search-task", "")
	if err != nil {
		t.Fatal(err)
	}
	searchClient, err := websearch.New(searchProvider.URL)
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("web-search-worker", repository, openaicompat.New(modelProvider.URL+"/v1", "", func() string { return "fake-search-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWebSearchExecutor(searchClient)
	for attempt := 0; attempt < 3; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded || !strings.Contains(completed.ResultSummary, "docs.example.test") || modelCalls != 2 {
		t.Fatalf("task=%#v model_calls=%d err=%v", completed, modelCalls, err)
	}
	if approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending); err != nil || len(approvals) != 0 {
		t.Fatalf("read-only search unexpectedly requested approval: %#v err=%v", approvals, err)
	}
	artifacts, err := service.ListRunArtifacts(context.Background(), task.RunID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, eventType := range []string{"tool.planned", "tool.completed", "validation.completed", "model.continuation_queued"} {
		if !slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == eventType }) {
			t.Fatalf("missing event %s in %#v", eventType, events)
		}
	}
}
