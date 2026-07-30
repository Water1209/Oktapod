package harness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type storeStub struct {
	step           core.ClaimedStep
	run            core.RunContext
	started        bool
	checkpointKind string
	finished       bool
}

func (s *storeStub) ClaimNextStep(context.Context, string, time.Time, time.Time) (core.ClaimedStep, error) {
	return s.step, nil
}
func (s *storeStub) GetRunContext(context.Context, string) (core.RunContext, error) {
	return s.run, nil
}
func (s *storeStub) MarkStepStarted(context.Context, core.ClaimedStep, core.RunContext, string) error {
	s.started = true
	return nil
}
func (s *storeStub) CommitCheckpoint(_ context.Context, _ core.ClaimedStep, _ core.RunContext, kind string, _ map[string]any, _ string) error {
	s.checkpointKind = kind
	return nil
}
func (s *storeStub) FinishStep(context.Context, core.ClaimedStep, core.RunContext, core.StepOutcome, string) error {
	s.finished = true
	return nil
}

func TestDurableOwnsClaimCheckpointAndTerminalCommit(t *testing.T) {
	store := &storeStub{
		step: core.ClaimedStep{ID: "step-1", RunID: "run-1", StepType: core.StepModelSynthesis},
		run:  core.RunContext{RunID: "run-1"},
	}
	h := New(store)
	execution, err := h.Claim(context.Background(), "worker-1", time.Now(), time.Minute, "correlation-1")
	if err != nil || !store.started || execution.Run.RunID != "run-1" {
		t.Fatalf("claim failed: execution=%+v started=%v err=%v", execution, store.started, err)
	}
	if err := h.CheckpointBeforeExecution(context.Background(), execution); err != nil || store.checkpointKind != "before_execution" {
		t.Fatalf("checkpoint kind=%q err=%v", store.checkpointKind, err)
	}
	if err := h.Finish(context.Background(), execution, core.StepOutcome{Status: core.StatusSucceeded}); err != nil || !store.finished {
		t.Fatalf("finish=%v err=%v", store.finished, err)
	}
}

func TestRetryBusyDoesNotRetryNonDatabaseFailure(t *testing.T) {
	attempts := 0
	err := RetryBusy(context.Background(), func() error {
		attempts++
		return errors.New("validation failed")
	})
	if err == nil || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}
