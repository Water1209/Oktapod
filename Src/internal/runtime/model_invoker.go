package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/oktapod/oktapod/Src/internal/agentloop"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// ModelInvoker owns exactly one provider-neutral model turn. It is the single
// place that applies the total completion deadline and classifies a live but
// non-completing provider stream. Request assembly and durable persistence stay
// outside this boundary.
type ModelInvoker struct {
	model    ModelGateway
	loop     agentloop.AgentLoop
	timeouts ModelTurnTimeouts
}

func NewModelInvoker(model ModelGateway, loop agentloop.AgentLoop, timeout time.Duration) ModelInvoker {
	return ModelInvoker{model: model, loop: loop, timeouts: uniformModelTurnTimeouts(timeout)}
}

func NewModelInvokerWithTimeouts(model ModelGateway, loop agentloop.AgentLoop, timeouts ModelTurnTimeouts) ModelInvoker {
	return ModelInvoker{model: model, loop: loop, timeouts: timeouts}
}

func (invoker ModelInvoker) Complete(ctx context.Context, request ModelRequest) (core.ModelResult, error) {
	return invoker.CompleteLive(ctx, request, nil)
}

func (invoker ModelInvoker) CompleteLive(ctx context.Context, request ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	loop := invoker.loop
	if loop == nil {
		loop = agentloop.New(invoker.model)
	}
	callCtx := ctx
	cancel := func() {}
	timeout := invoker.timeouts.For(request.ThinkingLevel)
	if timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	started := time.Now()
	event := loop.Next(callCtx, agentloop.LoopState{Request: request, OnDelta: onDelta})
	if event.Err != nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		event.Result.ErrorCode = "PROVIDER_UNAVAILABLE"
		event.Result.StopReason = core.StopReasonError
		if event.Result.Latency <= 0 {
			event.Result.Latency = time.Since(started)
		}
		return event.Result, &modelTurnTimeoutFailure{timeout: timeout}
	}
	return event.Result, event.Err
}

type ModelTurnTimeouts struct {
	Auto   time.Duration
	Off    time.Duration
	Low    time.Duration
	Medium time.Duration
	High   time.Duration
}

func DefaultModelTurnTimeouts() ModelTurnTimeouts {
	return ModelTurnTimeouts{
		Auto: 2 * time.Minute, Off: 2 * time.Minute, Low: 3 * time.Minute,
		Medium: 5 * time.Minute, High: 10 * time.Minute,
	}
}

func uniformModelTurnTimeouts(timeout time.Duration) ModelTurnTimeouts {
	return ModelTurnTimeouts{Auto: timeout, Off: timeout, Low: timeout, Medium: timeout, High: timeout}
}

func (timeouts ModelTurnTimeouts) For(level string) time.Duration {
	selected := timeouts.Auto
	switch level {
	case "off":
		selected = timeouts.Off
	case "low":
		selected = timeouts.Low
	case "medium":
		selected = timeouts.Medium
	case "high":
		selected = timeouts.High
	}
	if selected <= 0 {
		return 2 * time.Minute
	}
	return selected
}

type modelTurnTimeoutFailure struct {
	timeout time.Duration
}

func (f *modelTurnTimeoutFailure) Error() string {
	return "model turn exceeded its completion deadline"
}
func (f *modelTurnTimeoutFailure) Unwrap() error             { return context.DeadlineExceeded }
func (f *modelTurnTimeoutFailure) ModelFailureKind() string  { return "model_turn_timeout" }
func (f *modelTurnTimeoutFailure) ModelFailurePhase() string { return "model_turn" }
func (f *modelTurnTimeoutFailure) ModelFailureTimeout() bool { return true }
