package core

import (
	"strings"
	"testing"
)

func TestPlanToolFailsClosedForUnsafeInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		call ToolCall
	}{
		{name: "path escape", call: ToolCall{Name: ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "../secret.txt", "content": "x"}}},
		{name: "absolute path", call: ToolCall{Name: ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "/tmp/secret.txt", "content": "x"}}},
		{name: "shell", call: ToolCall{Name: ToolWorkspaceRunCommand, Arguments: map[string]any{"program": "powershell", "args": []any{"-Command", "echo unsafe"}}}},
		{name: "program path", call: ToolCall{Name: ToolWorkspaceRunCommand, Arguments: map[string]any{"program": "tools/run.exe", "args": []any{}}}},
		{name: "network program", call: ToolCall{Name: ToolWorkspaceRunCommand, Arguments: map[string]any{"program": "curl", "args": []any{"https://example.test"}}}},
		{name: "unknown tool", call: ToolCall{Name: "unregistered_network_tool", Arguments: map[string]any{"url": "http://127.0.0.1"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if plan := PlanTool(test.call); plan.Decision != PolicyDeny || plan.InputHash == "" {
				t.Fatalf("plan=%#v, want deterministic deny with input hash", plan)
			}
		})
	}
}

func TestExecutionAuthorizationBindsToolInputScopeRiskAndPolicy(t *testing.T) {
	call := ToolCall{ID: "call-1", Name: ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "result.md", "content": "ok"}}
	planned := PlanTool(call)
	authorization := ExecutionAuthorization{
		ToolIdentity: call.Name, InputHash: planned.InputHash,
		OrganizationID: "org-1", ProjectID: "project-1", RunID: "run-1",
		RiskLevel: planned.RiskLevel, PolicyDecision: planned.Decision, PolicyVersion: ToolPolicyVersion,
	}
	if err := ValidateExecutionAuthorization(planned, authorization); err != nil {
		t.Fatalf("valid authorization rejected: %v", err)
	}
	authorization.InputHash = "sha256:changed"
	if err := ValidateExecutionAuthorization(planned, authorization); err == nil {
		t.Fatal("changed frozen input was authorized")
	}
}

func TestBuiltinToolSpecCarriesExecutionAndCapabilityMetadata(t *testing.T) {
	var read ToolSpec
	for _, spec := range BuiltinToolSpecs() {
		if spec.Name == ToolWorkspaceReadFile {
			read = spec
			break
		}
	}
	if read.Identity != "native:"+ToolWorkspaceReadFile || read.Source != "native" || !read.ReadOnly || read.ExecutorKind == "" || read.ValidatorKind == "" || read.RendererKind == "" {
		t.Fatalf("read ToolSpec metadata incomplete: %#v", read)
	}
	if len(read.Aliases) != 1 || read.Aliases[0] != "read" || len(read.Capabilities) != 1 || read.Capabilities[0] != "repository.read" {
		t.Fatalf("read ToolSpec aliases/capabilities incomplete: %#v", read)
	}
}

func TestConciseToolAliasUsesCanonicalPolicyWithoutChangingInvocationIdentity(t *testing.T) {
	call := ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"path": "README.md"}}
	planned := PlanTool(call)
	if planned.Decision != PolicyAllow || planned.Call.Name != "read" || ResolveToolName(planned.Call.Name) != ToolWorkspaceReadFile {
		t.Fatalf("alias plan=%#v", planned)
	}
}

func TestPlanToolUsesAllowAskDenyPrecedence(t *testing.T) {
	t.Parallel()
	write := PlanTool(ToolCall{Name: ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "notes/result.md", "content": "safe"}})
	if write.Decision != PolicyAllow || write.RiskLevel != "L1" || !write.Idempotent {
		t.Fatalf("write plan=%#v", write)
	}
	capture := PlanTool(ToolCall{Name: ToolArtifactCapture, Arguments: map[string]any{"path": "out/report.pdf", "kind": "deliverable"}})
	if capture.Decision != PolicyAllow || capture.RiskLevel != "L1" || !capture.Idempotent {
		t.Fatalf("artifact capture policy=%#v", capture)
	}
	command := PlanTool(ToolCall{Name: ToolWorkspaceRunCommand, Arguments: map[string]any{"program": "go", "args": []any{"test", "./..."}}})
	if command.Decision != PolicyAsk || command.RiskLevel != "L2" || command.Idempotent {
		t.Fatalf("command plan=%#v", command)
	}
	shell := PlanTool(ToolCall{Name: ToolWorkspaceShell, Arguments: map[string]any{"command": "go test ./...", "timeout_seconds": float64(60)}})
	if shell.Decision != PolicyAsk || shell.RiskLevel != "L2" || shell.Idempotent {
		t.Fatalf("shell plan=%#v", shell)
	}
	if command.InputHash == write.InputHash {
		t.Fatal("different frozen inputs produced the same hash")
	}
}

func TestPlanToolMCPCallRequiresApproval(t *testing.T) {
	planned := PlanTool(ToolCall{Name: "mcp__capability-1__search", Arguments: map[string]any{"query": "ok"}})
	if planned.Decision != PolicyAsk || planned.RiskLevel != "L2" || planned.Idempotent {
		t.Fatalf("planned=%#v", planned)
	}
}

func TestPlanToolAllowsBoundedReadOnlyWebSearch(t *testing.T) {
	planned := PlanTool(ToolCall{Name: ToolWebSearch, Arguments: map[string]any{
		"query": "latest Go release",
	}})
	if planned.Decision != PolicyAllow || planned.RiskLevel != "L0" || !planned.Idempotent || planned.InputHash == "" {
		t.Fatalf("planned=%#v", planned)
	}
}

func TestPlanToolAllowsBoundedSkillInvocation(t *testing.T) {
	planned := PlanTool(ToolCall{Name: ToolSkillInvoke, Arguments: map[string]any{"skill": "release-check", "args": "production"}})
	if planned.Decision != PolicyAllow || planned.RiskLevel != "L0" || !planned.Idempotent || planned.InputHash == "" {
		t.Fatalf("planned=%#v", planned)
	}
	for _, arguments := range []map[string]any{{}, {"skill": ""}, {"skill": "release-check", "args": []any{"invalid"}}, {"skill": "release-check", "unknown": true}} {
		if denied := PlanTool(ToolCall{Name: ToolSkillInvoke, Arguments: arguments}); denied.Decision != PolicyDeny {
			t.Fatalf("arguments=%#v planned=%#v", arguments, denied)
		}
	}
}

func TestPlanToolDeniesUnsafeWebSearchArguments(t *testing.T) {
	tests := []map[string]any{
		{"query": "x"},
		{"query": "valid", "max_results": float64(11)},
		{"query": "valid", "allowed_domains": []any{"https://example.com"}},
		{"query": "valid", "allowed_domains": []any{"example.com"}, "blocked_domains": []any{"other.example"}},
	}
	for _, arguments := range tests {
		if planned := PlanTool(ToolCall{Name: ToolWebSearch, Arguments: arguments}); planned.Decision != PolicyDeny {
			t.Fatalf("arguments=%#v planned=%#v", arguments, planned)
		}
	}
}

func TestPlanToolClassifiesWorkspaceAndGitRegistry(t *testing.T) {
	hash := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		call       ToolCall
		decision   string
		idempotent bool
	}{
		{ToolCall{Name: ToolWorkspaceReadFile, Arguments: map[string]any{"path": "README.md", "limit_lines": float64(50)}}, PolicyAllow, true},
		{ToolCall{Name: ToolWorkspaceListFiles, Arguments: map[string]any{"recursive": true, "limit": float64(20)}}, PolicyAllow, true},
		{ToolCall{Name: ToolWorkspaceFindFiles, Arguments: map[string]any{"pattern": "*.go"}}, PolicyAllow, true},
		{ToolCall{Name: ToolWorkspaceGrep, Arguments: map[string]any{"query": "TODO"}}, PolicyAllow, true},
		{ToolCall{Name: ToolWorkspaceEditFile, Arguments: map[string]any{"path": "README.md", "preimage_hash": hash, "old_text": "old", "new_text": "new"}}, PolicyAllow, true},
		{ToolCall{Name: ToolWorkspaceShell, Arguments: map[string]any{"command": "go test ./...", "timeout_seconds": float64(120)}}, PolicyAsk, false},
		{ToolCall{Name: ToolGitStatus, Arguments: map[string]any{}}, PolicyAllow, true},
		{ToolCall{Name: ToolGitDiff, Arguments: map[string]any{"staged": false}}, PolicyAllow, true},
		{ToolCall{Name: ToolGitLog, Arguments: map[string]any{"limit": float64(10)}}, PolicyAllow, true},
		{ToolCall{Name: ToolGitConflicts, Arguments: map[string]any{}}, PolicyAllow, true},
		{ToolCall{Name: ToolGitBranch, Arguments: map[string]any{"name": "feature/test", "expected_status_hash": hash}}, PolicyAsk, false},
		{ToolCall{Name: ToolGitCommit, Arguments: map[string]any{"message": "test commit", "expected_status_hash": hash}}, PolicyAsk, false},
		{ToolCall{Name: ToolGitCommitPaths, Arguments: map[string]any{"message": "selected", "paths": []any{"README.md"}, "expected_status_hash": hash}}, PolicyAsk, false},
		{ToolCall{Name: ToolGitRestore, Arguments: map[string]any{"path": "README.md", "expected_status_hash": hash}}, PolicyAsk, false},
		{ToolCall{Name: ToolGitRestoreHunk, Arguments: map[string]any{"path": "README.md", "hunk_index": float64(0), "expected_status_hash": hash}}, PolicyAsk, false},
	}
	for _, test := range tests {
		planned := PlanTool(test.call)
		if planned.Decision != test.decision || planned.Idempotent != test.idempotent {
			t.Fatalf("%s plan=%#v", test.call.Name, planned)
		}
	}
}

func TestPlanToolClassifiesFixedDocumentProtocolAndRejectsExecutablePayloads(t *testing.T) {
	tests := []struct {
		call ToolCall
		risk string
	}{
		{ToolCall{Name: ToolDocumentOpen, Arguments: map[string]any{"source_ref": "asset:00000000-0000-7000-8000-000000000001", "mode": "structure"}}, "L0"},
		{ToolCall{Name: ToolDocumentRead, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "limit": float64(20)}}, "L0"},
		{ToolCall{Name: ToolDocumentRender, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1)}}, "L0"},
		{ToolCall{Name: ToolDocumentCreate, Arguments: map[string]any{"format": "docx", "specification": map[string]any{"title": "Report"}}}, "L1"},
		{ToolCall{Name: ToolDocumentApply, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "operations": []any{map[string]any{"op": "replace_text", "find": "old", "replace": "new"}}}}, "L1"},
		{ToolCall{Name: ToolDocumentApply, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "operations": []any{map[string]any{"op": "update_paragraph", "index": float64(2), "text": "updated"}}}}, "L1"},
		{ToolCall{Name: ToolDocumentApply, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "operations": []any{map[string]any{"op": "insert_table", "index": float64(2), "rows": []any{[]any{"Name", "Value"}, []any{"Runtime", "ok"}}}}}}, "L1"},
		{ToolCall{Name: ToolDocumentApply, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "operations": []any{map[string]any{"op": "delete_paragraph", "paragraph": float64(2)}}}}, "L1"},
		{ToolCall{Name: ToolDocumentExport, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "name": "report.docx", "format": "docx", "kind": "deliverable"}}, "L1"},
	}
	for _, test := range tests {
		planned := PlanTool(test.call)
		if planned.Decision != PolicyAllow || planned.RiskLevel != test.risk || !planned.Idempotent || planned.InputHash == "" {
			t.Fatalf("document tool %s planned=%#v", test.call.Name, planned)
		}
	}
	for _, call := range []ToolCall{
		{Name: ToolDocumentOpen, Arguments: map[string]any{"source_ref": "workspace:../secret.pdf", "mode": "full"}},
		{Name: ToolDocumentCreate, Arguments: map[string]any{"format": "pdf", "specification": map[string]any{"script": "open('secret')"}}},
		{Name: ToolDocumentApply, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "operations": []any{map[string]any{"op": "shell", "command": "calc.exe"}}}},
		{Name: ToolDocumentApply, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "operations": []any{map[string]any{"op": "update_paragraph", "text": "missing index"}}}},
		{Name: ToolDocumentApply, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "operations": []any{map[string]any{"op": "insert_table", "rows": []any{[]any{}}}}}},
		{Name: ToolDocumentExport, Arguments: map[string]any{"document_ref": "document:00000000-0000-7000-8000-000000000001", "expected_revision": float64(1), "name": "../report.pdf", "format": "pdf", "kind": "deliverable"}},
	} {
		if planned := PlanTool(call); planned.Decision != PolicyDeny {
			t.Fatalf("unsafe document tool payload was not denied: call=%#v planned=%#v", call, planned)
		}
	}
}

func TestDocumentApplySchemaListsFixedOperationsAndRecoveryAliases(t *testing.T) {
	var apply ToolSpec
	for _, spec := range BuiltinToolSpecs() {
		if spec.Name == ToolDocumentApply {
			apply = spec
			break
		}
	}
	properties := apply.Parameters["properties"].(map[string]any)
	operations := properties["operations"].(map[string]any)
	items := operations["items"].(map[string]any)
	itemProperties := items["properties"].(map[string]any)
	op := itemProperties["op"].(map[string]any)
	enum := op["enum"].([]string)
	for _, expected := range []string{"update_paragraph", "insert_table", "delete_paragraph"} {
		found := false
		for _, actual := range enum {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("document_apply schema omitted %q: %#v", expected, enum)
		}
	}
	if !strings.Contains(apply.PromptHint, "insert_table") || !strings.Contains(apply.Guidelines[1], "update_paragraph") {
		t.Fatalf("document_apply recovery guidance is incomplete: %#v", apply)
	}
}

func TestPlanToolDeniesWorkspaceCASAndGitPreconditionErrors(t *testing.T) {
	tests := []ToolCall{
		{Name: ToolWorkspaceReadFile, Arguments: map[string]any{"path": "../secret"}},
		{Name: ToolWorkspaceEditFile, Arguments: map[string]any{"path": "a", "preimage_hash": "bad", "old_text": "x", "new_text": "y"}},
		{Name: ToolWorkspaceShell, Arguments: map[string]any{"command": "", "timeout_seconds": float64(301)}},
		{Name: ToolGitBranch, Arguments: map[string]any{"name": "-unsafe", "expected_status_hash": "bad"}},
		{Name: ToolGitCommit, Arguments: map[string]any{"message": "", "expected_status_hash": "bad"}},
		{Name: ToolGitCommitPaths, Arguments: map[string]any{"message": "x", "paths": []any{"../secret"}, "expected_status_hash": "bad"}},
		{Name: ToolGitRestore, Arguments: map[string]any{"path": "../secret", "expected_status_hash": "bad"}},
		{Name: ToolGitRestoreHunk, Arguments: map[string]any{"path": "../secret", "hunk_index": float64(-1), "expected_status_hash": "bad"}},
	}
	for _, call := range tests {
		if planned := PlanTool(call); planned.Decision != PolicyDeny {
			t.Fatalf("%s plan=%#v", call.Name, planned)
		}
	}
}
