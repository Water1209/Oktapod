package runtime

import "github.com/oktapod/oktapod/Src/internal/agentloop"

// Compatibility aliases keep adapters and tests source-compatible while the
// provider-neutral model turn is owned by the pure agentloop package.
type ModelRequest = agentloop.ModelRequest
type ToolDefinition = agentloop.ToolDefinition
type ModelGateway = agentloop.ModelGateway
