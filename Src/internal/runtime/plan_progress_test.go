package runtime

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestContinuationTurnUsesFrozenPlanProgressReminder(t *testing.T) {
	assembler := NewAgentTurnAssembler(nil)
	run := core.RunContext{
		RunID: "plan-progress-run", Goal: "write the report", Acceptance: "deliver verified output",
		Mode: core.ModeSingleAgent, ConfigSnapshotJSON: `{"schema_version":2,"scene":"code","permission_mode":"ask"}`,
	}
	step := core.ClaimedStep{
		StepType:  core.StepModelContinue,
		InputJSON: `{"history":[{"kind":"assistant","content":"工具已执行"}],"stage":"continue","source_step_id":"step-1"}`,
		RunID:     run.RunID,
	}

	withoutPlan, failure := assembler.Assemble(context.Background(), step, run)
	if failure != nil {
		t.Fatalf("assembly without plan failed: %#v", *failure)
	}
	if strings.Contains(withoutPlan.Request.UserPrompt, "必须单独调用 update_plan") {
		t.Fatal("continuation without a published plan must not report plan progress")
	}

	step.InputJSON = `{"history":[{"kind":"assistant","content":"工具已执行"}],"stage":"continue","source_step_id":"step-1","active_plan_total":3,"active_plan_remaining":2}`
	first, failure := assembler.Assemble(context.Background(), step, run)
	if failure != nil {
		t.Fatalf("assembly with frozen plan failed: %#v", *failure)
	}
	second, failure := assembler.Assemble(context.Background(), step, run)
	if failure != nil {
		t.Fatalf("repeated assembly failed: %#v", *failure)
	}
	if !strings.Contains(first.Request.UserPrompt, "必须单独调用 update_plan") {
		t.Fatal("continuation with unfinished frozen plan must report progress")
	}
	if !reflect.DeepEqual(first.Request, second.Request) {
		t.Fatal("same continuation Step assembled differently")
	}

	planExecuteStep := step
	planExecuteStep.InputJSON = `{"history":[{"kind":"assistant","content":"工具已执行"}],"stage":"plan_execute","source_step_id":"step-1","active_plan_total":3,"active_plan_remaining":2}`
	planExecute, failure := assembler.Assemble(context.Background(), planExecuteStep, run)
	if failure != nil {
		t.Fatalf("plan_execute assembly failed: %#v", *failure)
	}
	if strings.Count(planExecute.Request.UserPrompt, "必须单独调用 update_plan") > 1 {
		t.Fatal("plan_execute turn duplicated the update_plan instruction")
	}
}
