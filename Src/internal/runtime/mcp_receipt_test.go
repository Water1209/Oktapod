package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type beginCountingRepository struct {
	WorkerRepository
	beginCalls int
}

func (repository *beginCountingRepository) BeginToolExecution(context.Context, core.ClaimedStep, core.RunContext, core.ToolCall, string, bool, string) (string, error) {
	repository.beginCalls++
	return "", errors.New("unexpected receipt begin")
}

func TestMCPMissingCredentialFailsBeforeReceiptBegin(t *testing.T) {
	call := core.ToolCall{ID: "mcp-call-missing-secret", Name: "mcp__cap-search__search", Arguments: map[string]any{"query": "determinism"}}
	planned := core.PlanTool(call)
	repository := &beginCountingRepository{}
	worker := NewWorker("mcp-preflight-worker", repository, nil, nil)
	outcome := worker.toolRuntime().Execute(context.Background(), core.ClaimedStep{
		ID: "step-mcp-missing-secret", StepType: call.Name, InputJSON: `{"tool_call_id":"mcp-call-missing-secret","name":"mcp__cap-search__search","arguments":{"query":"determinism"},"input_hash":"` + planned.InputHash + `","idempotent":true}`,
	}, core.RunContext{ConfigSnapshotJSON: `{"mcp_capabilities":[{"id":"cap-search","manifest_json":"{\"transport\":\"streamable_http\",\"url\":\"https://mcp.invalid/mcp\",\"auth\":{\"type\":\"bearer\"}}","credential_ref":"secret:mcp:cap-search","tools":[{"name":"search"}]}]}`})
	if outcome.Status != core.StatusBlocked || outcome.ErrorCode != "MCP_CREDENTIAL_MISSING" {
		t.Fatalf("outcome=%#v", outcome)
	}
	if repository.beginCalls != 0 || outcome.ToolExecution != nil {
		t.Fatalf("receipt began before MCP preflight: calls=%d execution=%#v", repository.beginCalls, outcome.ToolExecution)
	}
}

func TestToolRuntimeStrictDecoderRejectsEveryToolFamilyBeforeReceipt(t *testing.T) {
	tests := []struct {
		name      string
		stepType  string
		errorCode string
	}{
		{name: "skill", stepType: core.ToolSkillInvoke, errorCode: "SKILL_INVOKE_INPUT_INVALID"},
		{name: "web", stepType: core.ToolWebSearch, errorCode: "WEB_SEARCH_INPUT_INVALID"},
		{name: "workspace", stepType: core.ToolWorkspaceReadFile, errorCode: "TOOL_INPUT_INVALID"},
		{name: "mcp", stepType: "mcp__cap-search__search", errorCode: "MCP_TOOL_INPUT_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &beginCountingRepository{}
			runtime := ToolRuntime{ledger: repository}
			outcome := runtime.Execute(context.Background(), core.ClaimedStep{
				ID: "strict-" + test.name, StepType: test.stepType,
				InputJSON: `{"tool_call_id":"call","name":"` + test.stepType + `","arguments":{},"input_hash":"sha256:test","idempotent":true,"unexpected":true}`,
			}, core.RunContext{})
			if outcome.Status != core.StatusBlocked || outcome.ErrorCode != test.errorCode || repository.beginCalls != 0 {
				t.Fatalf("outcome=%#v begin_calls=%d", outcome, repository.beginCalls)
			}
		})
	}
}

func TestToolRuntimeCanceledBeforeDispatchDoesNotBeginReceipt(t *testing.T) {
	repository := &beginCountingRepository{}
	runtime := ToolRuntime{ledger: repository}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome := runtime.Execute(ctx, core.ClaimedStep{ID: "canceled-before-dispatch", StepType: core.ToolWebSearch}, core.RunContext{})
	if outcome.Status != core.StatusFailed || outcome.ErrorCode != "TASK_CANCELED" || repository.beginCalls != 0 {
		t.Fatalf("outcome=%#v begin_calls=%d", outcome, repository.beginCalls)
	}
}

func TestMCPCallErrorSeparatesCancelTimeoutAndFailure(t *testing.T) {
	if code, _, retryable := classifyMCPCallError(context.Canceled, context.Canceled); code != "TOOL_CANCELED" || retryable {
		t.Fatalf("cancel code=%s retryable=%v", code, retryable)
	}
	if code, _, retryable := classifyMCPCallError(context.DeadlineExceeded, context.DeadlineExceeded); code != "MCP_CALL_TIMEOUT" || !retryable {
		t.Fatalf("timeout code=%s retryable=%v", code, retryable)
	}
	if code, reason, retryable := classifyMCPCallError(errors.New("remote failed"), nil); code != "MCP_CALL_FAILED" || reason != "remote failed" || retryable {
		t.Fatalf("failure code=%s reason=%q retryable=%v", code, reason, retryable)
	}
}

func TestRuntimeDependencyFailureTargetsCapabilityAndKeepsDependencyID(t *testing.T) {
	output := map[string]any{}
	decorateRuntimeDependencyFailure(output, "mcp-capability", []application.RuntimeDependency{{
		ID: "chromium", Kind: "browser", Provider: "playwright", Name: "chromium", DisplayName: "Chromium",
		Version: "1.2.3", Installable: true,
	}})
	repair := output["repair"].(map[string]any)
	if output["repair_capability_id"] != "mcp-capability" || repair["capability_id"] != "mcp-capability" {
		t.Fatalf("repair target=%#v", output)
	}
	dependencies := output["runtime_dependencies"].([]map[string]any)
	if len(dependencies) != 1 || dependencies[0]["id"] != "chromium" {
		t.Fatalf("runtime dependencies=%#v", dependencies)
	}
}

func TestRuntimeDependencyFailureMarkerIsProviderNeutralAndBounded(t *testing.T) {
	marker := runtimeDependencyFailureMarker([]application.RuntimeDependency{
		{Provider: "playwright", ID: "chromium", Description: "raw output must not be persisted"},
		{Provider: "generic", ID: "runtime"},
	})
	if marker != "RUNTIME_DEPENDENCY_MISSING provider=playwright id=chromium\nRUNTIME_DEPENDENCY_MISSING provider=generic id=runtime" {
		t.Fatalf("marker=%q", marker)
	}
}
