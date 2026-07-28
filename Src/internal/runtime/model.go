package runtime

import (
	"context"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type ModelRequest struct {
	SystemPrompt    string
	UserPrompt      string
	Messages        []core.ModelMessage
	RequestedModel  core.ModelRef
	MaxOutputTokens int
	EnableTools     bool
	Model           string
	FallbackModel   string
	ToolDefinitions []ToolDefinition
	// ToolScopeDeclared means AllowedTools came from one or more selected
	// Skills and must constrain both built-in and capability-provided tools.
	ToolScopeDeclared bool
	AllowedTools      []string
	// ContextPlan records deterministic pruning decisions so the UI and audit
	// trail can explain why only a subset of capability schemas was sent.
	ContextPlan map[string]any
}

// ToolDefinition is a provider-neutral function schema. Adapters may map it
// to OpenAI-compatible tools or another provider's native schema.
type ToolDefinition struct {
	Name            string
	Description     string
	Parameters      map[string]any
	Sequential      bool
	ReadOnlyHint    *bool
	DestructiveHint *bool
	IdempotentHint  *bool
	OpenWorldHint   *bool
}

type ModelGateway interface {
	Complete(ctx context.Context, request ModelRequest) (core.ModelResult, error)
}
