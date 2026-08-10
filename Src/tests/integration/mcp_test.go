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

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestMCPLifecycleRequiresEnableAndPersistsHealth(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if request.Method == "initialize" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]string{{"name": "search"}}}})
	}))
	defer server.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "MCP project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "mcp-project")
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindMCP, Name: "local-search", ManifestJSON: `{"transport":"streamable_http","url":"` + server.URL + `"}`}, "mcp-capability")
	if err != nil {
		t.Fatal(err)
	}
	if capability.Status != core.CapabilityStatusQuarantined {
		t.Fatalf("MCP should start quarantined: %#v", capability)
	}
	if _, err := service.ConnectMCP(context.Background(), capability.ID, "mcp-connect-blocked"); err == nil {
		t.Fatal("quarantined MCP connected without approval")
	}
	if _, err := service.ReviewCapability(context.Background(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "MCP endpoint reviewed"}, "mcp-review", "mcp-review"); err != nil {
		t.Fatal(err)
	}
	health, err := service.ConnectMCP(context.Background(), capability.ID, "mcp-connect")
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != core.MCPStatusConnected || health.ToolCount != 1 {
		t.Fatalf("health=%#v", health)
	}
	disabled, err := service.SetCapabilityStatus(context.Background(), capability.ID, core.CapabilityStatusDisabled, "mcp-disable")
	if err != nil || disabled.Status != core.CapabilityStatusDisabled || disabled.Availability != core.ResourceAvailabilityDisabled {
		t.Fatalf("disable MCP=%#v err=%v", disabled, err)
	}
	if _, err := service.ConnectMCP(context.Background(), capability.ID, "mcp-connect-disabled"); err == nil {
		t.Fatal("disabled MCP connected")
	}
	enabled, err := service.SetCapabilityStatus(context.Background(), capability.ID, core.CapabilityStatusEnabled, "mcp-reenable")
	if err != nil || enabled.Status != core.CapabilityStatusEnabled {
		t.Fatalf("re-enable MCP=%#v err=%v", enabled, err)
	}
	health, err = service.ConnectMCP(context.Background(), capability.ID, "mcp-reconnect")
	if err != nil || health.Status != core.MCPStatusConnected || health.ToolCount != 1 {
		t.Fatalf("reconnect MCP health=%#v err=%v", health, err)
	}
	activeTask, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Keep MCP snapshot active", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "mcp-active-task", "mcp-active-task")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteCapability(context.Background(), capability.ID, "mcp-delete-active"); err == nil {
		t.Fatal("active Run did not block MCP deletion")
	}
	if _, err := service.ControlTask(context.Background(), activeTask.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "mcp-cancel-active", "mcp-cancel-active"); err != nil {
		t.Fatal(err)
	}
	health, err = service.DisconnectMCP(context.Background(), capability.ID, "mcp-disconnect")
	if err != nil || health.Status != core.MCPStatusDisabled {
		t.Fatalf("disconnect health=%#v err=%v", health, err)
	}
	if err := service.DeleteCapability(context.Background(), capability.ID, "mcp-delete-idle"); err != nil {
		t.Fatalf("delete idle MCP: %v", err)
	}

	// Project-shared MCP health is readable by an active member, but connection
	// lifecycle remains an owner operation.
	teamProject, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Shared MCP project", Kind: core.ProjectKindTeam, Scene: core.SceneCode}, "mcp-shared-project")
	if err != nil {
		t.Fatal(err)
	}
	sharedCapability, err := service.CreateCapability(context.Background(), teamProject.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindMCP, Name: "shared-search", ManifestJSON: `{"transport":"streamable_http","url":"` + server.URL + `"}`}, "mcp-shared-capability")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewCapability(context.Background(), sharedCapability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "shared MCP endpoint reviewed"}, "mcp-shared-review", "mcp-shared-review"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConnectMCP(context.Background(), sharedCapability.ID, "mcp-shared-connect"); err != nil {
		t.Fatal(err)
	}
	member, err := service.AddProjectMember(context.Background(), teamProject.ID, core.AddProjectMemberInput{Email: "mcp-member@example.test", DisplayName: "MCP Member", Role: core.ProjectRoleMember}, "mcp-shared-member", "mcp-shared-member")
	if err != nil {
		t.Fatal(err)
	}
	memberService := application.NewService(repository, core.ProfileTeam, "https://example.invalid/v1", core.Actor{ID: member.UserID, DisplayName: member.DisplayName}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	if memberHealth, err := memberService.GetMCPHealth(context.Background(), sharedCapability.ID); err != nil || memberHealth.Status != core.MCPStatusConnected {
		t.Fatalf("member MCP health=%#v err=%v", memberHealth, err)
	}
}

func TestMCPToolCallFlowsThroughModelApprovalAndReceipt(t *testing.T) {
	t.Parallel()
	var capabilityToolCalled bool
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
		case "server/discover":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]any{{"name": "search", "description": "Search test data", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}})
		case "tools/call":
			if request.Params["name"] != "search" {
				t.Fatalf("unexpected params=%#v", request.Params)
			}
			capabilityToolCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"content": []map[string]any{{"type": "text", "text": "mcp result"}}, "structuredContent": map[string]any{"ok": true}}})
		default:
			t.Fatalf("unexpected MCP method %s", request.Method)
		}
	}))
	defer mcpServer.Close()

	var capability core.Capability
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "mcp-test-model"}}})
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		name := ""
		for _, tool := range payload.Tools {
			if strings.HasPrefix(tool.Function.Name, "mcp__") {
				name = tool.Function.Name
			}
		}
		if name == "" {
			t.Fatalf("MCP tool was not exposed to model: %s", string(body))
		}
		if strings.Contains(string(body), "Reject unknown MCP tool") {
			name = "mcp__" + capability.ID + "__unknown"
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "provider-mcp-unknown", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "mcp-call-unknown", "type": "function", "function": map[string]any{"name": name, "arguments": `{"query":"ok"}`}}}}}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5}})
			return
		}
		modelCalls++
		if modelCalls > 1 {
			if !strings.Contains(string(body), "mcp result") {
				t.Fatalf("MCP continuation did not contain verified tool output: %s", string(body))
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "provider-mcp-final", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "MCP result verified and task completed."}}}, "usage": map[string]any{"prompt_tokens": 16, "completion_tokens": 6}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "provider-mcp-1", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "mcp-call-1", "type": "function", "function": map[string]any{"name": name, "arguments": `{"query":"ok"}`}}}}}}, "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5}})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-mcp-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "MCP task", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "mcp-task-project")
	if err != nil {
		t.Fatal(err)
	}
	capability, err = service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindMCP, Name: "test-mcp", ManifestJSON: `{"transport":"streamable_http","url":"` + mcpServer.URL + `"}`}, "mcp-task-capability")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewCapability(context.Background(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "MCP endpoint reviewed"}, "mcp-task-review", "mcp-task-review"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ConnectMCP(context.Background(), capability.ID, "mcp-task-connect"); err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Use MCP", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 2, MaxTokens: 8192}, "mcp-task-create", "mcp-task-idempotency")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("mcp-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-mcp-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("model step worked=%v err=%v", worked, err)
	}
	approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%#v err=%v", approvals, err)
	}
	if _, err := service.DecideApproval(context.Background(), approvals[0].ID, core.DecideApprovalInput{Decision: core.ApprovalApproved}, "mcp-task-approve", "mcp-task-approval"); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("MCP step worked=%v err=%v", worked, err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("MCP continuation worked=%v err=%v", worked, err)
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != core.StatusSucceeded || !capabilityToolCalled {
		t.Fatalf("task=%#v mcp_called=%v", completed, capabilityToolCalled)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenToolCompleted := false
	for _, event := range events {
		if event.EventType == "tool.completed" {
			seenToolCompleted = true
		}
	}
	if !seenToolCompleted {
		t.Fatalf("MCP tool.completed event missing: %#v", events)
	}

	unknownTask, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Reject unknown MCP tool", Mode: core.ModeSingleAgent, Visibility: core.VisibilityProject, MaxModelCalls: 2, MaxTokens: 8192}, "mcp-unknown-create", "mcp-unknown-idempotency")
	if err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("unknown MCP model step worked=%v err=%v", worked, err)
	}
	unknownApprovals, err := service.ListApprovals(context.Background(), core.ApprovalPending)
	if err != nil || len(unknownApprovals) != 1 {
		t.Fatalf("unknown MCP approvals=%#v err=%v", unknownApprovals, err)
	}
	if _, err := service.DecideApproval(context.Background(), unknownApprovals[0].ID, core.DecideApprovalInput{Decision: core.ApprovalApproved}, "mcp-unknown-approve", "mcp-unknown-approval"); err != nil {
		t.Fatal(err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("unknown MCP execution worked=%v err=%v", worked, err)
	}
	unknownCompleted, err := service.GetTask(context.Background(), unknownTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unknownCompleted.Status != core.StatusBlocked || unknownCompleted.ErrorCode != "MCP_TOOL_NOT_ALLOWED" {
		t.Fatalf("unknown MCP task=%#v", unknownCompleted)
	}
}
