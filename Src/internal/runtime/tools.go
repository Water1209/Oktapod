package runtime

import (
	"context"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type WorkspaceExecutor interface {
	Execute(ctx context.Context, projectID string, call core.ToolCall) core.ToolExecution
}

type WebSearchExecutor interface {
	Execute(ctx context.Context, call core.ToolCall) core.ToolExecution
}

type WebAccess interface {
	Search(context.Context, core.WebSearchRequest) (core.WebSearchResult, error)
	Fetch(context.Context, core.WebFetchRequest) (core.WebFetchResult, error)
	EvaluateFetch(snapshotJSON, rawURL string) core.WebFetchPolicyDecision
}

func builtinToolDefinitions() []ToolDefinition {
	specs := core.BuiltinToolSpecs()
	definitions := make([]ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		definitions = append(definitions, ToolDefinition{
			Name: spec.Name, Description: spec.Description, PromptHint: spec.PromptHint, Guidelines: append([]string(nil), spec.Guidelines...),
			Parameters: spec.Parameters, Sequential: spec.Sequential,
		})
	}
	return definitions
}

// ModelBudgetReserver is intentionally optional so lightweight test
// repositories and alternative runtimes can opt into pre-call reservation
// without widening the core Repository contract.
type ModelBudgetReserver interface {
	ReserveModelBudget(ctx context.Context, run core.RunContext, step core.ClaimedStep, estimatedTokens int64, correlationID string) error
}
