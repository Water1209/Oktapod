package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/agentloop"
)

type continuouslyStreamingLoop struct {
	deltas int
}

func (loop *continuouslyStreamingLoop) Next(ctx context.Context, state agentloop.LoopState) agentloop.LoopEvent {
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return agentloop.LoopEvent{Err: ctx.Err()}
		case <-ticker.C:
			loop.deltas++
			if state.OnDelta != nil {
				_ = state.OnDelta("live")
			}
		}
	}
}

func TestModelInvokerCompletionDeadlineIsNotResetByLiveDeltas(t *testing.T) {
	loop := &continuouslyStreamingLoop{}
	invoker := NewModelInvoker(nil, loop, 25*time.Millisecond)
	_, err := invoker.CompleteLive(t.Context(), ModelRequest{}, func(string) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v, want deadline exceeded", err)
	}
	if loop.deltas < 2 {
		t.Fatalf("deltas=%d, expected a live stream before the total deadline", loop.deltas)
	}
}

func TestModelTurnTimeoutsHonorExplicitThinkingLevel(t *testing.T) {
	timeouts := DefaultModelTurnTimeouts()
	if timeouts.For("off") != 2*time.Minute || timeouts.For("low") != 3*time.Minute || timeouts.For("medium") != 5*time.Minute || timeouts.For("high") != 10*time.Minute {
		t.Fatalf("timeouts=%+v", timeouts)
	}
}
