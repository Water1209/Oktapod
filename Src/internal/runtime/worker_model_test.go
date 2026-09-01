package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestRetryOnBusyRetriesLocalDatabaseDeadline(t *testing.T) {
	attempts := 0
	err := retryOnBusy(context.Background(), func() error {
		attempts++
		if attempts == 1 {
			return context.DeadlineExceeded
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestToolReceiptBeginFailureDistinguishesUnknownSideEffectFromPersistenceFailure(t *testing.T) {
	unknown := toolReceiptBeginFailure(errors.New("SIDE_EFFECT_STATUS_UNKNOWN: receipt is incomplete"), "unknown")
	if unknown.Status != core.StatusBlocked || unknown.ErrorCode != "SIDE_EFFECT_STATUS_UNKNOWN" || unknown.Output["retryable"] != false {
		t.Fatalf("unknown side effect outcome=%+v", unknown)
	}

	transient := toolReceiptBeginFailure(errors.New("database is locked (SQLITE_BUSY)"), "must not be used")
	if transient.Status != core.StatusFailed || transient.ErrorCode != "TOOL_RECEIPT_BEGIN_FAILED" || transient.Output["retryable"] != true {
		t.Fatalf("transient receipt failure outcome=%+v", transient)
	}
}

func TestModelOverrideWinsOverAgentModel(t *testing.T) {
	model, fallback := agentModelsFromSnapshot(`{"model_override":"project-model","fallback_model_override":"project-fallback","agent":{"model":"agent-model","fallback_model":"agent-fallback"}}`)
	if model != "project-model" || fallback != "project-fallback" {
		t.Fatalf("model=%q fallback=%q", model, fallback)
	}
}

func TestAgentModelRemainsDefaultWhenNoOverride(t *testing.T) {
	model, fallback := agentModelsFromSnapshot(`{"agent":{"model":"agent-model","fallback_model":"agent-fallback"}}`)
	if model != "agent-model" || fallback != "agent-fallback" {
		t.Fatalf("model=%q fallback=%q", model, fallback)
	}
}

func TestProjectModelRemainsDefaultWhenNoTaskOrAgentOverride(t *testing.T) {
	model, fallback := agentModelsFromSnapshot(`{"project_default_model":"project-model","project_fallback_model":"project-fallback"}`)
	if model != "project-model" || fallback != "project-fallback" {
		t.Fatalf("model=%q fallback=%q", model, fallback)
	}
}

func TestThinkingLevelComesFromImmutableRunSnapshot(t *testing.T) {
	if got := thinkingLevelFromSnapshot(`{"thinking_level":"high"}`); got != "high" {
		t.Fatalf("thinking level=%q", got)
	}
	if got := thinkingLevelFromSnapshot(`{"thinking_level":"unexpected"}`); got != "auto" {
		t.Fatalf("invalid thinking level=%q", got)
	}
}

func TestMainAgentAutoThinkingIsActionFirstOnlyForVersionedToolRuns(t *testing.T) {
	requested, effective := mainAgentThinkingLevel(`{"thinking_level":"auto","thinking_policy_version":"task_action_v1"}`, true)
	if requested != "auto" || effective != "auto" || !mainAgentActionFirst(`{"thinking_level":"auto","thinking_policy_version":"task_action_v1"}`, true) {
		t.Fatalf("requested=%q effective=%q", requested, effective)
	}

	_, effective = mainAgentThinkingLevel(`{"thinking_level":"auto"}`, true)
	if effective != "auto" {
		t.Fatalf("historical effective=%q, want auto", effective)
	}
	if mainAgentActionFirst(`{"thinking_level":"auto"}`, true) {
		t.Fatal("historical run unexpectedly enabled action-first")
	}

	_, effective = mainAgentThinkingLevel(`{"thinking_level":"high","thinking_policy_version":"task_action_v1"}`, true)
	if effective != "high" {
		t.Fatalf("explicit effective=%q, want high", effective)
	}
	if mainAgentActionFirst(`{"thinking_level":"high","thinking_policy_version":"task_action_v1"}`, true) {
		t.Fatal("explicit high unexpectedly enabled action-first")
	}

	_, effective = mainAgentThinkingLevel(`{"thinking_level":"auto","thinking_policy_version":"task_action_v1"}`, false)
	if effective != "auto" {
		t.Fatalf("answer effective=%q, want auto", effective)
	}
	if mainAgentActionFirst(`{"thinking_level":"auto","thinking_policy_version":"task_action_v1"}`, false) {
		t.Fatal("answer turn unexpectedly enabled action-first")
	}
}

func TestExplicitSearchBudgetCountsSucceededHistoryAndProposedCalls(t *testing.T) {
	history := []map[string]any{
		{"kind": "tool_result", "status": core.StatusSucceeded, "tool_name": "mcp__tavily__tavily_search"},
		{"kind": "tool_result", "status": core.StatusSucceeded, "tool_name": "mcp__tavily__tavily_research"},
		{"kind": "tool_result", "status": core.StatusSucceeded, "tool_name": core.ToolWebSearch},
	}
	if !searchCallBudgetExceeded(history, []core.ToolCall{{Name: "mcp__tavily__tavily_search"}}, 3) {
		t.Fatal("fourth search was not rejected")
	}
	if searchCallBudgetExceeded(history, []core.ToolCall{{Name: core.ToolWorkspaceWriteFile}}, 3) {
		t.Fatal("non-search action consumed the search budget")
	}
	if !searchCallBudgetExceededWithPrior(nil, []core.ToolCall{{Name: "mcp__tavily__tavily_search"}}, 3, 3) {
		t.Fatal("successor run did not inherit the predecessor search budget")
	}
	if !searchCallBudgetExceededWithPrior(nil, []core.ToolCall{{Name: core.ToolWebSearch}}, 0, 0) {
		t.Fatal("explicit zero budget did not block a new search")
	}
	definitions := excludeSearchToolDefinitions([]ToolDefinition{{Name: core.ToolWorkspaceReadFile}, {Name: core.ToolWebSearch}, {Name: "mcp__tavily__tavily_research"}})
	if len(definitions) != 1 || definitions[0].Name != core.ToolWorkspaceReadFile {
		t.Fatalf("exhausted search tools remained advertised: %#v", definitions)
	}
	names := excludeSearchToolNames([]string{core.ToolWorkspaceGrep, "mcp__tavily__tavily_extract"})
	if len(names) != 1 || names[0] != core.ToolWorkspaceGrep {
		t.Fatalf("exhausted search names remained allowed: %#v", names)
	}
}

func TestModelCompatibilityRetryRaisesOutputLimit(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{input: `{}`, want: 8192},
		{input: `{"model_compat_retry_count":1}`, want: 16384},
		{input: `{"model_compat_retry_count":2}`, want: 32768},
		{input: `{"model_compat_retry_count":99}`, want: 32768},
	}
	for _, test := range tests {
		if got := modelMaxOutputTokens(test.input); got != test.want {
			t.Fatalf("modelMaxOutputTokens(%s)=%d, want %d", test.input, got, test.want)
		}
	}
}

func TestSecondTruncationRetryForcesChunkedWrites(t *testing.T) {
	hint := modelCompatibilityRetryHint(`{"model_compat_retry_count":2,"model_compat_retry_reason":"PROVIDER_TOOL_ARGUMENTS_TRUNCATED"}`)
	if hint == "" || !containsAll(hint, "禁止在单个工具参数中生成整篇长文", "逐段补全") {
		t.Fatalf("unexpected retry hint: %q", hint)
	}
}

func TestPlanRegressionRetryPreservesCompletedItems(t *testing.T) {
	hint := modelCompatibilityRetryHint(`{"model_retry_count":1,"model_retry_reason":"RUN_PLAN_STATUS_REGRESSION"}`)
	if hint == "" || !containsAll(hint, "已完成项", "completed", "同一完整清单") {
		t.Fatalf("unexpected plan regression hint: %q", hint)
	}
}

func TestReasoningTruncationRetryCanFinishFromExistingEvidence(t *testing.T) {
	hint := modelCompatibilityRetryHint(`{"model_retry_count":1,"model_retry_reason":"PROVIDER_REASONING_TRUNCATED"}`)
	if hint == "" || !containsAll(hint, "交付物与验证", "禁止为了总结重复读写", "不超过 800 字") {
		t.Fatalf("unexpected reasoning truncation hint: %q", hint)
	}
}

func TestDeliverableRecoveryAcceptsEditArtifactsAndContinuationEvidence(t *testing.T) {
	hint := completionRecoveryHint(`{"completion_recovery_reason":"DELIVERABLE_MISSING"}`)
	if hint == "" || !containsAll(hint, "续跑来源链", "workspace_edit_file", "交付 Artifact") {
		t.Fatalf("unexpected deliverable recovery hint: %q", hint)
	}
}

func TestAttachmentRecoveryRequiresFrozenAssetStaging(t *testing.T) {
	hint := completionRecoveryHint(`{"completion_recovery_reason":"ATTACHMENT_NOT_READ"}`)
	if hint == "" || !containsAll(hint, "asset_stage", "只读路径", "不得改查空的项目 Workspace") {
		t.Fatalf("unexpected attachment recovery hint: %q", hint)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
