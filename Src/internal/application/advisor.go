package application

import (
	"context"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// AdvisorModel is intentionally smaller than the full runtime gateway. It
// keeps the application layer independent from provider SDKs while allowing a
// configured model to review a bounded evidence summary after the user sees
// and approves the incremental estimate.
type AdvisorModel interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int) (core.ModelResult, error)
}

// ProjectTurnModel is optional so existing offline AdvisorModel test doubles
// remain valid. The production gateway implements it to honor project model
// defaults and fallback policy for conversation turns.
type ProjectTurnModel interface {
	CompleteTurn(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int, model, fallbackModel string) (core.ModelResult, error)
}

type TypedProjectTurnModel interface {
	CompleteMessages(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, model, fallbackModel string) (core.ModelResult, error)
}

type ProviderProjectTurnModel interface {
	CompleteTurnWithModelRef(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int, requested core.ModelRef, fallbackModel string) (core.ModelResult, error)
}

type TypedProviderProjectTurnModel interface {
	CompleteMessagesWithModelRef(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, requested core.ModelRef, fallbackModel string) (core.ModelResult, error)
}

// ProjectTurnStreamModel is implemented by providers that can expose answer
// deltas as they arrive. The final ModelResult remains the audit and usage
// source of truth after all deltas have been emitted.
type ProjectTurnStreamModel interface {
	StreamTurn(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int, model, fallbackModel string, onDelta func(string) error) (core.ModelResult, error)
}

type TypedProjectTurnStreamModel interface {
	StreamMessages(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, model, fallbackModel string, onDelta func(string) error) (core.ModelResult, error)
}

type TypedProviderProjectTurnStreamModel interface {
	StreamMessagesWithModelRef(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, requested core.ModelRef, fallbackModel string, onDelta func(string) error) (core.ModelResult, error)
}

// ChatToolStreamModel extends streaming with tool definitions so the chat mode
// can offer read-only workspace tools to the model without creating a full Task/Run.
type ChatToolStreamModel interface {
	StreamMessagesWithTools(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, requested core.ModelRef, fallbackModel string, toolDefinitions []ChatToolDefinition, onDelta func(string) error) (core.ModelResult, error)
}

// ChatToolDefinition is a minimal tool schema for chat-mode tool use.
type ChatToolDefinition struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// ChatToolExecutor executes a single read-only tool call during a chat turn.
// It returns the tool result text and an error if execution fails.
type ChatToolExecutor interface {
	ExecuteChatTool(ctx context.Context, projectID string, call core.ToolCall) (string, error)
}

// WorkspaceReader exposes structured, read-only workspace results to
// application use cases such as immutable Run baselines and the Changes
// inspector. Mutating calls remain inside the Durable Harness.
type WorkspaceReader interface {
	Execute(ctx context.Context, projectID string, call core.ToolCall) core.ToolExecution
}

// ProjectWorkspaceInitializer establishes the deterministic local baseline
// required by Changes. It is intentionally separate from generic tool
// execution so creating a project never depends on a model-generated command.
type ProjectWorkspaceInitializer interface {
	InitializeProject(ctx context.Context, projectID string) error
}

type AdvisorReportFinalizer interface {
	FinalizeAdvisorReport(ctx context.Context, scope core.Scope, reportID string, model core.ModelResult, modelErr error) (core.AdvisorReport, error)
}
