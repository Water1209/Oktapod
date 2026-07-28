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

type AdvisorReportFinalizer interface {
	FinalizeAdvisorReport(ctx context.Context, scope core.Scope, reportID string, model core.ModelResult, modelErr error) (core.AdvisorReport, error)
}
