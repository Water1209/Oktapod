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

func TestFullAccessKeepsMCPAskAndFrozenCapabilityBoundary(t *testing.T) {
	t.Parallel()
	toolCalled := false
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch request.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25"}})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]any{{"name": "confirm", "description": "Confirm a governed action", "inputSchema": map[string]any{"type": "object"}}}}})
		case "tools/call":
			if request.Params["name"] != "confirm" {
				t.Fatalf("unexpected MCP params=%#v", request.Params)
			}
			toolCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "confirmed"}}}})
		default:
			t.Fatalf("unexpected MCP method %q", request.Method)
		}
	}))
	defer mcpServer.Close()

	var capability core.Capability
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "full-access-test-model"}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "mcp__"+capability.ID+"__confirm") {
			t.Fatalf("frozen MCP tool was not exposed to the model: %s", body)
		}
		modelCalls++
		w.Header().Set("Content-Type", "application/json")
		if modelCalls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "full-access-mcp-plan", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "full-access-mcp-call", "type": "function", "function": map[string]any{"name": "mcp__" + capability.ID + "__confirm", "arguments": `{}`}}}}}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5}})
			return
		}
		if !strings.Contains(string(body), "confirmed") {
			t.Fatalf("MCP receipt was not returned to the continuation: %s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "full-access-mcp-final", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "MCP confirmation completed."}}}, "usage": map[string]any{"prompt_tokens": 15, "completion_tokens": 6}})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Full Access User"}, core.Organization{ID: testOrgID, Name: "Full Access Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Full access MCP", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "full-access-mcp-project")
	if err != nil {
		t.Fatal(err)
	}
	capability, err = service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindMCP, Name: "full-access-confirm", ManifestJSON: `{"transport":"streamable_http","url":"` + mcpServer.URL + `"}`}, "full-access-mcp-capability")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewCapability(context.Background(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "endpoint reviewed"}, "full-access-mcp-review", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConnectMCP(context.Background(), capability.ID, "full-access-mcp-connect"); err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Use the confirmed MCP action", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, PermissionMode: core.PermissionModeFullAccess}, "full-access-mcp-task", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("full-access-mcp-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("planning worked=%v err=%v", worked, runErr)
	}
	approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("full access MCP must require one approval: %#v err=%v", approvals, err)
	}
	if _, err := service.DecideApproval(context.Background(), approvals[0].ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Scope: core.ApprovalScopeOnce}, "full-access-mcp-decision", ""); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("worker attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded || !toolCalled {
		t.Fatalf("task=%#v tool_called=%v err=%v", completed, toolCalled, err)
	}
	if approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending); err != nil || len(approvals) != 0 {
		t.Fatalf("approved MCP remained pending=%#v err=%v", approvals, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == "approval.requested" }) {
		t.Fatalf("full access MCP did not emit approval event: %#v", events)
	}
	if !slices.ContainsFunc(events, func(event core.RunEvent) bool {
		return event.EventType == "tool.planned" && event.Payload["policy_decision"] == core.PolicyAsk
	}) {
		t.Fatalf("MCP ask was not preserved under full access: %#v", events)
	}
}

func TestFullAccessCannotBypassCommandHardDeny(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "full-access-test-model"}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "full-access-deny", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "denied-shell-call", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceRunCommand, "arguments": `{"program":"powershell","args":["-Command","Get-ChildItem"]}`}}}}}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5}})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Full Access User"}, core.Organization{ID: testOrgID, Name: "Full Access Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Full access hard deny", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "full-access-deny-project")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Attempt a shell command", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, PermissionMode: core.PermissionModeFullAccess}, "full-access-deny-task", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("full-access-deny-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("worker worked=%v err=%v", worked, runErr)
	}
	blocked, err := service.GetTask(context.Background(), task.ID)
	if err != nil || blocked.Status != core.StatusBlocked || blocked.ErrorCode != "TOOL_POLICY_DENIED" {
		t.Fatalf("hard deny task=%#v err=%v", blocked, err)
	}
	if approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending); err != nil || len(approvals) != 0 {
		t.Fatalf("hard deny should not create an approvable request: %#v err=%v", approvals, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(event core.RunEvent) bool {
		return event.EventType == "tool.planned" && event.Payload["policy_decision"] == core.PolicyDeny
	}) {
		t.Fatalf("hard deny was not persisted: %#v", events)
	}
}
