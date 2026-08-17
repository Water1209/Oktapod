package application

import (
	"encoding/json"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// MCPPolicyFromSnapshot derives the execution contract from the immutable Run
// snapshot. Planning, durable authorization, and ToolRuntime preflight must all
// call this function so risk and idempotency cannot drift between phases.
func MCPPolicyFromSnapshot(snapshot, fullToolName string) (string, bool, string) {
	var config struct {
		Capabilities []struct {
			ID    string `json:"id"`
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnlyHint    *bool `json:"readOnlyHint"`
					DestructiveHint *bool `json:"destructiveHint"`
					IdempotentHint  *bool `json:"idempotentHint"`
					OpenWorldHint   *bool `json:"openWorldHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"mcp_capabilities"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return "L3", false, "MCP tool metadata is missing; high-risk manual approval is required"
	}
	for _, capability := range config.Capabilities {
		for _, tool := range capability.Tools {
			if "mcp__"+capability.ID+"__"+tool.Name != fullToolName {
				continue
			}
			annotations := tool.Annotations
			idempotent := annotations.IdempotentHint != nil && *annotations.IdempotentHint
			if annotations.ReadOnlyHint != nil && !*annotations.ReadOnlyHint {
				return "L3", idempotent, "MCP tool declares non-read-only behavior; one-time manual approval is required"
			}
			if annotations.DestructiveHint != nil && *annotations.DestructiveHint {
				return "L3", idempotent, "Destructive MCP execution always requires one-time manual approval"
			}
			if annotations.IdempotentHint != nil && !*annotations.IdempotentHint {
				return "L3", false, "Non-idempotent MCP execution always requires one-time manual approval"
			}
			if annotations.ReadOnlyHint == nil || annotations.DestructiveHint == nil || annotations.IdempotentHint == nil || annotations.OpenWorldHint == nil {
				if core.IsSearchEvidenceTool(fullToolName) {
					return "L2", true, "Enabled frozen MCP search is treated as read-only and idempotent despite incomplete optional annotations"
				}
				return "L3", false, "MCP annotations are incomplete; high-risk manual approval is required"
			}
			if !*annotations.ReadOnlyHint || *annotations.DestructiveHint || !*annotations.IdempotentHint {
				return "L3", idempotent, "MCP tool is not safely read-only and idempotent; one-time manual approval is required"
			}
			if *annotations.OpenWorldHint {
				return "L2", true, "Read-only idempotent MCP access to external data uses a durable receipt"
			}
			return "L2", true, "Bounded read-only MCP execution uses a durable receipt"
		}
	}
	return "L3", false, "MCP tool is absent from the frozen capability snapshot; high-risk manual approval is required"
}
