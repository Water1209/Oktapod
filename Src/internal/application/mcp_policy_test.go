package application

import "testing"

func TestMCPRunToolGrantUsesFrozenExactToolIdentity(t *testing.T) {
	snapshot := `{"mcp_capabilities":[{"id":"cap-playwright","name":"@playwright/mcp","tools":[{"name":"browser_navigate","description":"Navigate"},{"name":"browser_evaluate","description":"Evaluate"}]}]}`
	descriptor, ok := MCPToolDescriptorFromSnapshot(snapshot, "mcp__cap-playwright__browser_navigate")
	if !ok || descriptor.CapabilityName != "@playwright/mcp" || descriptor.ToolName != "browser_navigate" {
		t.Fatalf("descriptor=%#v ok=%v", descriptor, ok)
	}
	grantKey, policyHash, ok := MCPRunToolGrant(snapshot, "mcp__cap-playwright__browser_navigate")
	if !ok || grantKey == "" || policyHash == "" || grantKey == policyHash {
		t.Fatalf("grant=%q policy=%q ok=%v", grantKey, policyHash, ok)
	}
	otherGrant, otherPolicy, ok := MCPRunToolGrant(snapshot, "mcp__cap-playwright__browser_evaluate")
	if !ok || otherGrant == grantKey || otherPolicy == policyHash {
		t.Fatalf("exact-tool grant did not change: grant=%q policy=%q", otherGrant, otherPolicy)
	}
	if _, _, ok := MCPRunToolGrant(snapshot, "mcp__cap-playwright__missing"); ok {
		t.Fatal("missing frozen tool unexpectedly received a Run grant")
	}
}
