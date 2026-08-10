package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/adapters/websearch"
	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
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
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Research current Oktapod information", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 2, MaxTokens: 32768}, "web-search-task", "")
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

func TestDocumentTaskCannotCompleteAfterResearchWithoutFileReceipt(t *testing.T) {
	t.Parallel()
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "document-model", "tool_calling": true}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		modelCalls++
		if modelCalls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "research", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "先检索证据。", "tool_calls": []map[string]any{{"id": "search-call", "type": "function", "function": map[string]any{"name": core.ToolWebSearch, "arguments": `{"query":"physical ai products"}`}}}}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "false-finish", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "研究证据已经完整，现在开始形成 Markdown 文件。"}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Document User"}, core.Organization{ID: testOrgID, Name: "Document Org"}, func() string { return "fake-document-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Document project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "使用tavily工具帮我检索物理AI的产品调研报告，形成md文件", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 4, MaxTokens: 16384}, "document-completion-gate", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("document-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-document-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWebSearchExecutor(semanticPlanSearch{})
	for attempt := 0; attempt < 3; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	current, err := service.GetTask(context.Background(), task.ID)
	if err != nil || current.Status != core.StatusRunning {
		t.Fatalf("task falsely completed without a file receipt: %#v err=%v", current, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(event core.RunEvent) bool {
		return event.EventType == "completion.validation_failed" && event.Payload["reason"] == "DELIVERABLE_MISSING"
	}) {
		t.Fatalf("missing deliverable completion failure: %#v", events)
	}
}

func TestMutationTaskCannotCompleteFromAClaimAlone(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "mutation-model", "tool_calling": true}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "false-mutation", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "登录错误已经修复。"}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Mutation User"}, core.Organization{ID: testOrgID, Name: "Mutation Org"}, func() string { return "fake-mutation-key" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Mutation project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "修复登录错误", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 3, MaxTokens: 8192}, "mutation-completion-gate", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("mutation-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-mutation-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(t.Context()); runErr != nil || !worked {
		t.Fatalf("worker worked=%v err=%v", worked, runErr)
	}
	assertCompletionValidationFailure(t, service, task, "WORKSPACE_MUTATION_MISSING")
}

func TestWorkspaceCompletionRequiresDeclaredPostconditionEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		goal       string
		acceptance string
		path       string
		content    string
		reason     string
	}{
		{name: "verification after mutation", goal: "创建 config.txt 文件并运行测试验证", path: "config.txt", content: "enabled=true\n", reason: "VERIFICATION_MISSING"},
		{name: "minimum report length", goal: "创建 Markdown 报告并保存到 short.md", acceptance: "报告不少于100字", path: "short.md", content: "# 短报告\n\n内容不足。\n", reason: "DELIVERABLE_TOO_SHORT"},
		{name: "report citations", goal: "创建一份完整 Markdown 报告并保留可点击来源链接", acceptance: "报告不少于100字", path: "uncited.md", content: "# 完整报告\n\n" + strings.Repeat("这是没有来源链接的分析内容。", 20), reason: "DELIVERABLE_CITATIONS_MISSING"},
		{name: "incomplete report marker", goal: "创建一份完整 Markdown 报告", acceptance: "报告不少于100字", path: "unfinished.md", content: "# 未完成报告\n\n" + strings.Repeat("这是仍在写作中的分析内容。", 20) + "\n\n<!-- OKTAPOD_CONTINUE -->\n", reason: "DELIVERABLE_INCOMPLETE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			arguments, err := json.Marshal(map[string]any{"path": tt.path, "content": tt.content})
			if err != nil {
				t.Fatal(err)
			}
			modelCalls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/models" {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "workspace-gate-model", "tool_calling": true}}})
					return
				}
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				modelCalls++
				if modelCalls == 1 {
					_ = json.NewEncoder(w).Encode(map[string]any{"id": "workspace-write", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "写入文件。", "tool_calls": []map[string]any{{"id": "write-call", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": string(arguments)}}}}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 6}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "false-final", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "任务已经完成。"}}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 5}})
			}))
			defer provider.Close()

			repository, databasePath := newRepository(t)
			defer repository.Close()
			bootstrapRepository(t, repository)
			service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Workspace Gate User"}, core.Organization{ID: testOrgID, Name: "Workspace Gate Org"}, func() string { return "fake-workspace-key" })
			project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Workspace gate project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
			if err != nil {
				t.Fatal(err)
			}
			task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: tt.goal, Acceptance: tt.acceptance, Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 4, MaxTokens: 16384}, "workspace-completion-gate-"+tt.name, "")
			if err != nil {
				t.Fatal(err)
			}
			executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
			if err != nil {
				t.Fatal(err)
			}
			worker := okruntime.NewWorker("workspace-gate-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-workspace-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
			for attempt := 0; attempt < 3; attempt++ {
				if worked, runErr := worker.RunOnce(t.Context()); runErr != nil || !worked {
					t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
				}
			}
			assertCompletionValidationFailure(t, service, task, tt.reason)
		})
	}
}

func TestSingleReportLengthCannotBeSatisfiedByFragmentSum(t *testing.T) {
	t.Parallel()
	fragmentOne, fragmentTwo := "# 分片一\n\n"+strings.Repeat("甲", 70), "# 分片二\n\n"+strings.Repeat("乙", 70)
	firstArguments, _ := json.Marshal(map[string]any{"path": "part-1.md", "content": fragmentOne})
	secondArguments, _ := json.Marshal(map[string]any{"path": "part-2.md", "content": fragmentTwo})
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "single-report-model", "tool_calling": true}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		modelCalls++
		message := map[string]any{"role": "assistant", "content": "两个分片合计已经超过要求。"}
		if modelCalls == 1 {
			message = map[string]any{"role": "assistant", "content": "写入两个分片。", "tool_calls": []map[string]any{
				{"id": "write-fragment-one", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": string(firstArguments)}},
				{"id": "write-fragment-two", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": string(secondArguments)}},
			}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("single-report-%d", modelCalls), "choices": []map[string]any{{"message": message}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
	}))
	defer provider.Close()

	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Single Report User"}, core.Organization{ID: testOrgID, Name: "Single Report Org"}, func() string { return "fake-single-report-key" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Single report project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "创建一份完整 Markdown 分析报告", Acceptance: "报告不少于100字", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 6, MaxTokens: 65536}, "single-report-fragment-gate", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("single-report-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-single-report-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
	for attempt := 0; attempt < 4; attempt++ {
		if worked, runErr := worker.RunOnce(t.Context()); runErr != nil || !worked {
			t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	assertCompletionValidationFailure(t, service, task, "DELIVERABLE_TOO_SHORT")
}

func TestEditedDeliverableUsesVerifiedLatestContent(t *testing.T) {
	t.Parallel()
	initial := "# 报告\n\n短内容。\n"
	expanded := strings.Repeat("经过验证的研究内容。", 30)
	preimageHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(initial)))
	writeArguments, _ := json.Marshal(map[string]any{"path": "report.md", "content": initial})
	editArguments, _ := json.Marshal(map[string]any{"path": "report.md", "preimage_hash": preimageHash, "old_text": "短内容。", "new_text": expanded})
	readArguments, _ := json.Marshal(map[string]any{"path": "report.md"})
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "edit-gate-model", "tool_calling": true}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		modelCalls++
		var message map[string]any
		switch modelCalls {
		case 1:
			message = map[string]any{"role": "assistant", "content": "创建报告。", "tool_calls": []map[string]any{{"id": "write-edit-report", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": string(writeArguments)}}}}
		case 2:
			message = map[string]any{"role": "assistant", "content": "扩充报告。", "tool_calls": []map[string]any{{"id": "edit-report", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceEditFile, "arguments": string(editArguments)}}}}
		case 3:
			message = map[string]any{"role": "assistant", "content": "读取并验证最终文件。", "tool_calls": []map[string]any{{"id": "read-report", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceReadFile, "arguments": string(readArguments)}}}}
		default:
			message = map[string]any{"role": "assistant", "content": "报告已经写入、扩充并验证。"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("edit-gate-%d", modelCalls), "choices": []map[string]any{{"message": message}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
	}))
	defer provider.Close()

	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Edit Gate User"}, core.Organization{ID: testOrgID, Name: "Edit Gate Org"}, func() string { return "fake-edit-key" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Edit gate project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "创建 Markdown 报告并验证最终文件", Acceptance: "报告不少于100字", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 6, MaxTokens: 32768}, "edited-deliverable-gate", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("edit-gate-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-edit-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
	for attempt := 0; attempt < 7; attempt++ {
		if worked, runErr := worker.RunOnce(t.Context()); runErr != nil || !worked {
			t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	completed, err := service.GetTask(t.Context(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		t.Fatalf("edited deliverable task=%#v err=%v", completed, err)
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(databasePath), "workspaces", project.ID, "report.md"))
	if err != nil || !strings.Contains(string(content), expanded) {
		t.Fatalf("edited report was not preserved: size=%d err=%v", len(content), err)
	}
}

func TestNaturalLanguageContinuationCompletesFromSourceArtifactAndCurrentVerification(t *testing.T) {
	t.Parallel()
	report := "# 已完成报告\n\n这是由来源任务写入并保留给自然语言续跑任务的交付内容。\n"
	writeArguments, _ := json.Marshal(map[string]any{"path": "report.md", "content": report})
	readArguments, _ := json.Marshal(map[string]any{"path": "report.md"})
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "continuation-artifact-model", "tool_calling": true}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		modelCalls++
		var message map[string]any
		switch modelCalls {
		case 1:
			message = map[string]any{"role": "assistant", "content": "创建报告。", "tool_calls": []map[string]any{{"id": "write-source-report", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": string(writeArguments)}}}}
		case 2:
			message = map[string]any{"role": "assistant", "content": "来源任务已经完成报告。"}
		case 3:
			message = map[string]any{"role": "assistant", "content": "核对来源任务的报告。", "tool_calls": []map[string]any{{"id": "verify-source-report", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceReadFile, "arguments": string(readArguments)}}}}
		default:
			message = map[string]any{"role": "assistant", "content": "已复用来源任务交付物并完成当前核验。"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": fmt.Sprintf("continuation-artifact-%d", modelCalls), "choices": []map[string]any{{"message": message}}, "usage": map[string]any{"prompt_tokens": 20, "completion_tokens": 8}})
	}))
	defer provider.Close()

	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Continuation Artifact User"}, core.Organization{ID: testOrgID, Name: "Continuation Artifact Org"}, func() string { return "fake-continuation-key" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Continuation artifact project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "创建 Markdown 报告并保存到 report.md", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 6, MaxTokens: 32768}, "continuation-artifact-source", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("continuation-artifact-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-continuation-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
	for attempt := 0; attempt < 3; attempt++ {
		if worked, runErr := worker.RunOnce(t.Context()); runErr != nil || !worked {
			t.Fatalf("source attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	completedSource, err := service.GetTask(t.Context(), source.ID)
	if err != nil || completedSource.Status != core.StatusSucceeded {
		t.Fatalf("source task=%#v err=%v", completedSource, err)
	}

	turn, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{
		ConversationID: source.ConversationID,
		Content:        "继续当前任务。现有报告已经完成，核对 report.md 后直接交付，禁止联网",
		Mode:           core.ModeAuto,
		Visibility:     core.VisibilityPrivate,
	}, "continuation-artifact-turn", "continuation-artifact-turn")
	if err != nil || turn.Task == nil || turn.Task.ID == source.ID {
		t.Fatalf("continuation turn=%#v err=%v", turn, err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if worked, runErr := worker.RunOnce(t.Context()); runErr != nil || !worked {
			t.Fatalf("continuation attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	completed, err := service.GetTask(t.Context(), turn.Task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		events, _ := service.ListRunEvents(t.Context(), turn.Task.RunID, 0)
		t.Fatalf("continuation task=%#v model_calls=%d events=%#v err=%v", completed, modelCalls, events, err)
	}
	if modelCalls != 4 {
		t.Fatalf("model calls=%d, want 4", modelCalls)
	}
}

func assertCompletionValidationFailure(t *testing.T, service *application.Service, task core.Task, reason string) {
	t.Helper()
	current, err := service.GetTask(t.Context(), task.ID)
	if err != nil || current.Status != core.StatusRunning {
		t.Fatalf("task falsely completed: task=%#v err=%v", current, err)
	}
	events, err := service.ListRunEvents(t.Context(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(event core.RunEvent) bool {
		return event.EventType == "completion.validation_failed" && event.Payload["reason"] == reason
	}) {
		t.Fatalf("missing completion failure %s: %#v", reason, events)
	}
}
