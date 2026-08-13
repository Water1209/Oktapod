package sqlite

import (
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestToolEventPresentationForWorkspaceFile(t *testing.T) {
	input := toolInputSummary(core.ToolWorkspaceWriteFile, map[string]any{"path": "reports/final.md", "content": "hello"})
	if input != "reports/final.md · 5 B" {
		t.Fatalf("unexpected input summary: %q", input)
	}
	output := toolOutputSummary(core.ToolWorkspaceWriteFile, "succeeded", "", 0, map[string]any{"path": "reports/final.md", "size_bytes": float64(5)})
	if output != "成功 · reports/final.md · 5 B" {
		t.Fatalf("unexpected output summary: %q", output)
	}
}

func TestToolEventPresentationRedactsCommandSecrets(t *testing.T) {
	input := toolInputSummary(core.ToolWorkspaceRunCommand, map[string]any{
		"program": "tool",
		"args":    []any{"--token=private-value", "run"},
	})
	if strings.Contains(input, "private-value") || !strings.Contains(input, "[REDACTED]") {
		t.Fatalf("command input was not redacted: %q", input)
	}
	excerpt := toolOutputExcerpt(core.ToolWorkspaceRunCommand, map[string]any{"stdout": "Authorization: Bearer abcdefgh\nOK"})
	if strings.Contains(excerpt, "abcdefgh") || !strings.Contains(excerpt, "[REDACTED]") {
		t.Fatalf("command output was not redacted: %q", excerpt)
	}
}

func TestConversationApprovalRuleKeyScope(t *testing.T) {
	key, ok := conversationApprovalRuleKey(core.ToolWorkspaceRunCommand, `{"program":"GO","args":["version"]}`)
	if !ok || !strings.HasPrefix(key, "sha256:") {
		t.Fatalf("command rule key=%q ok=%v", key, ok)
	}
	sameKey, sameOK := conversationApprovalRuleKey(core.ToolWorkspaceRunCommand, `{"args":["version"],"program":"GO"}`)
	if !sameOK || sameKey != key {
		t.Fatalf("canonical signature changed: first=%q second=%q", key, sameKey)
	}
	differentKey, differentOK := conversationApprovalRuleKey(core.ToolWorkspaceRunCommand, `{"program":"GO","args":["env"]}`)
	if !differentOK || differentKey == key {
		t.Fatalf("different command arguments reused signature: first=%q second=%q", key, differentKey)
	}
	key, ok = conversationApprovalRuleKey("mcp__capability-1__search", `{}`)
	if ok || key != "" {
		t.Fatalf("MCP rule must never be reusable: key=%q ok=%v", key, ok)
	}
	if key, ok := conversationApprovalRuleKey(core.ToolWorkspaceWriteFile, `{"path":"notes/a.md"}`); ok || key != "" {
		t.Fatalf("write file unexpectedly reusable: key=%q", key)
	}
}

func TestMCPPolicyRequiresCompleteSafeAnnotations(t *testing.T) {
	missing := `{"mcp_capabilities":[{"id":"cap-1","tools":[{"name":"search","annotations":{"readOnlyHint":true}}]}]}`
	if risk, idempotent, _ := mcpPolicyFromSnapshot(missing, "mcp__cap-1__search"); risk != "L2" || !idempotent {
		t.Fatalf("missing annotations risk=%s idempotent=%v", risk, idempotent)
	}
	missingMutation := `{"mcp_capabilities":[{"id":"cap-1","tools":[{"name":"confirm","annotations":{}}]}]}`
	if risk, idempotent, _ := mcpPolicyFromSnapshot(missingMutation, "mcp__cap-1__confirm"); risk != "L3" || idempotent {
		t.Fatalf("missing mutation annotations risk=%s idempotent=%v", risk, idempotent)
	}
	safe := `{"mcp_capabilities":[{"id":"cap-1","tools":[{"name":"search","annotations":{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false}}]}]}`
	if risk, idempotent, _ := mcpPolicyFromSnapshot(safe, "mcp__cap-1__search"); risk != "L2" || !idempotent {
		t.Fatalf("safe annotations risk=%s idempotent=%v", risk, idempotent)
	}
	openWorld := `{"mcp_capabilities":[{"id":"cap-1","tools":[{"name":"search","annotations":{"readOnlyHint":true,"destructiveHint":false,"idempotentHint":true,"openWorldHint":true}}]}]}`
	if risk, idempotent, _ := mcpPolicyFromSnapshot(openWorld, "mcp__cap-1__search"); risk != "L2" || !idempotent {
		t.Fatalf("open-world annotation risk=%s", risk)
	}
	destructive := `{"mcp_capabilities":[{"id":"cap-1","tools":[{"name":"search","annotations":{"readOnlyHint":false,"destructiveHint":true,"idempotentHint":false,"openWorldHint":true}}]}]}`
	if risk, idempotent, _ := mcpPolicyFromSnapshot(destructive, "mcp__cap-1__search"); risk != "L3" || idempotent {
		t.Fatalf("destructive annotation risk=%s idempotent=%v", risk, idempotent)
	}
}

func TestFullAccessAutoAllowsOnlyNonL3Asks(t *testing.T) {
	if !fullAccessAutoAllows(true, core.PlannedTool{Decision: core.PolicyAsk, RiskLevel: "L2"}) {
		t.Fatal("full access should auto-allow an ordinary L2 request")
	}
	if fullAccessAutoAllows(true, core.PlannedTool{Decision: core.PolicyAsk, RiskLevel: "L3"}) {
		t.Fatal("full access must retain L3 approval")
	}
	if fullAccessAutoAllows(false, core.PlannedTool{Decision: core.PolicyAsk, RiskLevel: "L2"}) {
		t.Fatal("ask mode must retain L2 approval")
	}
	if fullAccessAutoAllows(true, core.PlannedTool{Decision: core.PolicyAsk, RiskLevel: "L0"}) {
		t.Fatal("full access must not bypass explicit human-input checkpoints")
	}
}
