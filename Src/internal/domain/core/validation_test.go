package core

import (
	"encoding/json"
	"testing"
)

func TestValidateCreateAgentConfigScopesAndBudgets(t *testing.T) {
	base := CreateAgentInput{Name: "reviewer", Version: "1.0.0", Instructions: "review", ConfigJSON: `{"allowed_capabilities":["git.read"],"memory_scope":"project","budget":{"max_model_calls":20,"max_tokens":50000}}`}
	if err := ValidateCreateAgent(base); err != nil {
		t.Fatalf("valid agent config rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		json string
	}{
		{"bad capabilities", `{"allowed_capabilities":"git.read"}`},
		{"bad memory scope", `{"memory_scope":"organization"}`},
		{"bad budget", `{"budget":{"max_model_calls":0}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			input.ConfigJSON = tc.json
			if err := ValidateCreateAgent(input); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateSkillCapabilitySelectionPolicy(t *testing.T) {
	input := CreateCapabilityInput{
		Kind:         CapabilityKindSkill,
		Name:         "release-check",
		Version:      "1.0.0",
		ScopeType:    CapabilityScopeProject,
		ManifestJSON: `{"description":"release","required-capabilities":["repository.read"],"preferred-tools":["grep"],"fallback-policy":"same-capability"}`,
	}
	if err := ValidateCreateCapability(input); err != nil {
		t.Fatalf("valid skill constraints rejected: %v", err)
	}
	input.ManifestJSON = `{"description":"release","fallback-policy":"any-tool"}`
	if err := ValidateCreateCapability(input); err == nil {
		t.Fatal("unsafe fallback policy was accepted")
	}
}

func TestCapabilityPermissionDiffRedactsValuesAndSorts(t *testing.T) {
	got := CapabilityPermissionDiff(`{"tools":[{"name":"send"},{"name":"search"}],"permissions":["workspace.write"],"network":{"url":"https://secret.invalid"},"env":{"TOKEN":"do-not-show"}}`)
	want := []string{"field:env", "field:network", "permissions:workspace.write", "tool:search", "tool:send"}
	if len(got) != len(want) {
		t.Fatalf("diff=%#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("diff=%#v, want %#v", got, want)
		}
	}
}

func TestValidateMemorySourceRefs(t *testing.T) {
	if err := ValidateMemorySourceRefs(`["event-1","artifact-2"]`); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMemorySourceRefs(`{"id":"not-an-array"}`); err == nil {
		t.Fatal("object source refs unexpectedly accepted")
	}
	refs := make([]string, 33)
	if err := ValidateMemorySourceRefs(mustJSON(refs)); err == nil {
		t.Fatal("more than 32 source refs unexpectedly accepted")
	}
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TestValidateDecideApprovalScope(t *testing.T) {
	once := DecideApprovalInput{Decision: ApprovalApproved}
	if err := ValidateDecideApproval(once); err != nil {
		t.Fatalf("default approval scope rejected: %v", err)
	}
	if err := ValidateDecideApproval(DecideApprovalInput{Decision: ApprovalApproved, Scope: ApprovalScopeConversation}); err != nil {
		t.Fatalf("conversation approval scope rejected: %v", err)
	}
	if err := ValidateDecideApproval(DecideApprovalInput{Decision: ApprovalRejected, Scope: ApprovalScopeConversation}); err == nil {
		t.Fatal("conversation scope unexpectedly accepted for rejection")
	}
	if err := ValidateDecideApproval(DecideApprovalInput{Decision: ApprovalApproved, Scope: ApprovalScopeProject}); err == nil {
		t.Fatal("unconfirmed project approval scope unexpectedly accepted")
	}
	if err := ValidateDecideApproval(DecideApprovalInput{Decision: ApprovalApproved, Scope: ApprovalScopeProject, Confirmed: true}); err != nil {
		t.Fatalf("confirmed project approval scope rejected: %v", err)
	}
	if err := ValidateDecideApproval(DecideApprovalInput{Decision: ApprovalApproved, Scope: ApprovalScopeRunTool}); err != nil {
		t.Fatalf("current Run tool approval scope rejected: %v", err)
	}
}
