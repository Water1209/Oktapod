package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type MCPToolDescriptor struct {
	CapabilityID   string
	CapabilityName string
	ToolName       string
	Description    string
}

func MCPToolDescriptorFromSnapshot(snapshot, fullToolName string) (MCPToolDescriptor, bool) {
	var config struct {
		Capabilities []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"tools"`
		} `json:"mcp_capabilities"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return MCPToolDescriptor{}, false
	}
	for _, capability := range config.Capabilities {
		for _, tool := range capability.Tools {
			if "mcp__"+capability.ID+"__"+tool.Name != fullToolName {
				continue
			}
			name := strings.TrimSpace(capability.Name)
			if name == "" {
				name = "MCP"
			}
			return MCPToolDescriptor{CapabilityID: capability.ID, CapabilityName: name, ToolName: tool.Name, Description: tool.Description}, true
		}
	}
	return MCPToolDescriptor{}, false
}

func MCPToolDisplayName(snapshot, fullToolName string) string {
	descriptor, ok := MCPToolDescriptorFromSnapshot(snapshot, fullToolName)
	if !ok {
		return fullToolName
	}
	return descriptor.CapabilityName + " · " + descriptor.ToolName
}

// MCPRunToolGrant binds an explicit approval to one frozen MCP tool identity
// in one immutable Run. It does not grant a whole server, conversation, or
// project, so newly installed tools cannot inherit the authorization.
func MCPRunToolGrant(snapshot, fullToolName string) (string, string, bool) {
	if _, ok := MCPToolDescriptorFromSnapshot(snapshot, fullToolName); !ok {
		return "", "", false
	}
	grantSum := sha256.Sum256([]byte("mcp-run-tool/v1\n" + fullToolName))
	policySum := sha256.Sum256([]byte("mcp-run-tool/v1\n" + fullToolName + "\n" + snapshot))
	return "sha256:" + hex.EncodeToString(grantSum[:]), "sha256:" + hex.EncodeToString(policySum[:]), true
}

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
				return "L3", idempotent, "MCP tool declares non-read-only behavior; explicit approval is required"
			}
			if annotations.DestructiveHint != nil && *annotations.DestructiveHint {
				return "L3", idempotent, "Destructive MCP execution always requires explicit manual approval"
			}
			if annotations.IdempotentHint != nil && !*annotations.IdempotentHint {
				return "L3", false, "Non-idempotent MCP execution always requires explicit manual approval"
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
