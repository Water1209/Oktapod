package agentloop

import (
	"context"
	"errors"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type modelStub struct {
	result core.ModelResult
	err    error
}

func (s modelStub) Complete(context.Context, ModelRequest) (core.ModelResult, error) {
	return s.result, s.err
}

func TestLoopProjectsProviderResultWithoutPersistenceDependencies(t *testing.T) {
	tests := []struct {
		name   string
		result core.ModelResult
		err    error
		want   EventKind
	}{
		{name: "completed", result: core.ModelResult{Content: "done"}, want: EventTurnCompleted},
		{name: "tool batch", result: core.ModelResult{ToolCalls: []core.ToolCall{{ID: "call-1", Name: "read"}}}, want: EventToolBatchProposed},
		{name: "context limit", result: core.ModelResult{StopReason: core.StopReasonLength}, want: EventContextLimit},
		{name: "failed", err: errors.New("provider failed"), want: EventTurnFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := New(modelStub{result: tt.result, err: tt.err}).Next(context.Background(), LoopState{})
			if event.Kind != tt.want {
				t.Fatalf("kind=%q want=%q", event.Kind, tt.want)
			}
		})
	}
}
