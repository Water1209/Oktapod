// Package harness owns the durable boundary around one agent step. It keeps
// persistence, checkpoints, fencing, and terminal commits outside agentloop.
package harness

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type Store interface {
	ClaimNextStep(ctx context.Context, workerID string, now, leaseUntil time.Time) (core.ClaimedStep, error)
	GetRunContext(ctx context.Context, runID string) (core.RunContext, error)
	MarkStepStarted(ctx context.Context, step core.ClaimedStep, run core.RunContext, correlationID string) error
	CommitCheckpoint(ctx context.Context, step core.ClaimedStep, run core.RunContext, kind string, state map[string]any, correlationID string) error
	FinishStep(ctx context.Context, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, correlationID string) error
}

type ClaimedExecution struct {
	Step          core.ClaimedStep
	Run           core.RunContext
	CorrelationID string
}

type Durable struct {
	store Store
}

func New(store Store) *Durable {
	return &Durable{store: store}
}

func (h *Durable) Claim(ctx context.Context, workerID string, now time.Time, lease time.Duration, correlationID string) (ClaimedExecution, error) {
	step, err := h.store.ClaimNextStep(ctx, workerID, now, now.Add(lease))
	if err != nil {
		return ClaimedExecution{}, err
	}
	run, err := h.store.GetRunContext(ctx, step.RunID)
	if err != nil {
		return ClaimedExecution{Step: step, CorrelationID: correlationID}, err
	}
	if err := h.store.MarkStepStarted(ctx, step, run, correlationID); err != nil {
		return ClaimedExecution{Step: step, Run: run, CorrelationID: correlationID}, err
	}
	return ClaimedExecution{Step: step, Run: run, CorrelationID: correlationID}, nil
}

func (h *Durable) CheckpointBeforeExecution(ctx context.Context, execution ClaimedExecution) error {
	return RetryBusy(ctx, func() error {
		checkpointCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return h.store.CommitCheckpoint(checkpointCtx, execution.Step, execution.Run, "before_execution", map[string]any{
			"step_type":         execution.Step.StepType,
			"input_hash_source": "persisted_step_input",
			"attempt_number":    execution.Step.AttemptNumber,
		}, execution.CorrelationID)
	})
}

func (h *Durable) Finish(ctx context.Context, execution ClaimedExecution, outcome core.StepOutcome) error {
	finishCtx := ctx
	cancel := func() {}
	if ctx.Err() != nil {
		finishCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	}
	defer cancel()
	return RetryBusy(finishCtx, func() error {
		return h.store.FinishStep(finishCtx, execution.Step, execution.Run, outcome, execution.CorrelationID)
	})
}

func RetryBusy(ctx context.Context, fn func() error) error {
	const maxAttempts = 5
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := fn(); err != nil {
			lastErr = err
			msg := err.Error()
			retryableDeadline := errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil
			if strings.Contains(msg, "database is locked") || strings.Contains(msg, "SQLITE_BUSY") || retryableDeadline {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(200*(attempt+1)) * time.Millisecond):
					continue
				}
			}
			return err
		}
		return nil
	}
	return lastErr
}
