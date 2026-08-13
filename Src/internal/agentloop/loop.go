// Package agentloop owns the provider-neutral, persistence-free model turn.
// It deliberately knows nothing about queues, leases, approvals, databases,
// HTTP, MCP transports, or concrete tool executors.
package agentloop

import (
	"context"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type ModelRequest struct {
	SystemPrompt      string
	UserPrompt        string
	Messages          []core.ModelMessage
	RequestedModel    core.ModelRef
	ThinkingLevel     string
	MaxOutputTokens   int
	EnableTools       bool
	Model             string
	FallbackModel     string
	ToolDefinitions   []ToolDefinition
	ToolScopeDeclared bool
	AllowedTools      []string
	ContextPlan       map[string]any
}

type ToolDefinition struct {
	Name            string
	Description     string
	PromptHint      string
	Guidelines      []string
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

type EventKind string

const (
	EventTurnCompleted     EventKind = "turn_completed"
	EventTurnFailed        EventKind = "turn_failed"
	EventToolBatchProposed EventKind = "tool_batch_proposed"
	EventContextLimit      EventKind = "context_limit_reached"
)

type LoopState struct {
	Request ModelRequest
}

type LoopEvent struct {
	Kind   EventKind
	Result core.ModelResult
	Err    error
}

type AgentLoop interface {
	Next(ctx context.Context, state LoopState) LoopEvent
}

type Loop struct {
	model ModelGateway
}

func New(model ModelGateway) *Loop {
	return &Loop{model: model}
}

func (l *Loop) Next(ctx context.Context, state LoopState) LoopEvent {
	result, err := l.model.Complete(ctx, state.Request)
	if err != nil {
		return LoopEvent{Kind: EventTurnFailed, Result: result, Err: err}
	}
	if result.StopReason == core.StopReasonLength {
		return LoopEvent{Kind: EventContextLimit, Result: result}
	}
	if len(result.ToolCalls) > 0 {
		return LoopEvent{Kind: EventToolBatchProposed, Result: result}
	}
	return LoopEvent{Kind: EventTurnCompleted, Result: result}
}
