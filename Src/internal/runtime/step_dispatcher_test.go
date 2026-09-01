package runtime

import (
	"context"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type recordingStepExecutor struct{ name string }

func (executor recordingStepExecutor) Execute(context.Context, core.ClaimedStep, core.RunContext) core.StepOutcome {
	return core.StepOutcome{Status: core.StatusSucceeded, ResultSummary: executor.name}
}

func TestStepDispatcherUsesFixedExecutorOwnership(t *testing.T) {
	dispatcher := StepDispatcher{
		mainAgent: recordingStepExecutor{name: "main"},
		reviewer:  recordingStepExecutor{name: "review"},
		synthesis: recordingStepExecutor{name: "synthesis"},
		agentTeam: recordingStepExecutor{name: "team"},
		tools:     recordingStepExecutor{name: "tool"},
	}
	tests := []struct {
		name string
		step core.ClaimedStep
		run  core.RunContext
		want string
	}{
		{name: "main", step: core.ClaimedStep{StepType: core.StepModelContinue}, want: "main"},
		{name: "review has priority over team mode", step: core.ClaimedStep{StepType: core.StepHumanActionReview}, run: core.RunContext{Mode: core.ModeAgentTeam}, want: "review"},
		{name: "agent team", step: core.ClaimedStep{StepType: core.StepAgentWorker}, run: core.RunContext{Mode: core.ModeAgentTeam}, want: "team"},
		{name: "synthesis", step: core.ClaimedStep{StepType: core.StepModelSynthesis}, want: "synthesis"},
		{name: "durable tool", step: core.ClaimedStep{StepType: core.ToolWorkspaceReadFile}, want: "tool"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := dispatcher.Execute(context.Background(), test.step, test.run)
			if outcome.ResultSummary != test.want {
				t.Fatalf("executor=%q want=%q", outcome.ResultSummary, test.want)
			}
		})
	}
}
