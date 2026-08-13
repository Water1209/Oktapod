package application

import (
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestClassifyTaskModeUsesDeterministicRules(t *testing.T) {
	tests := []struct {
		name  string
		input core.CreateTaskInput
		mode  string
	}{
		{name: "simple answer", input: core.CreateTaskInput{Goal: "解释一下 fencing token"}, mode: core.ModeAnswer},
		{name: "workspace task", input: core.CreateTaskInput{Goal: "修改文件并运行测试"}, mode: core.ModeSingleAgent},
		{name: "acceptance task", input: core.CreateTaskInput{Goal: "给出结果", Acceptance: "必须通过测试"}, mode: core.ModeSingleAgent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, reason := classifyTaskMode(test.input)
			if mode != test.mode || reason == "" {
				t.Fatalf("mode=%s reason=%s want=%s", mode, reason, test.mode)
			}
		})
	}
}

func TestTaskInboxReasonDoesNotPresentFailedTaskAsSuccessful(t *testing.T) {
	task := core.Task{
		Status:        core.StatusFailed,
		ResultSummary: "工具执行完成，并通过确定性校验。",
	}

	reason := taskInboxReason(task)
	if !strings.Contains(reason, "任务已停止") || !strings.Contains(reason, "不会自动继续") {
		t.Fatalf("reason=%q must explain the terminal failure", reason)
	}
	if strings.Contains(reason, "工具执行完成") {
		t.Fatalf("reason=%q must not repeat the contradictory success summary", reason)
	}
}

func TestTaskInboxActionExcludesTerminalFailure(t *testing.T) {
	kind, reason := taskInboxAction(core.Task{Status: core.StatusFailed, ErrorCode: "TOOL_EXECUTION_FAILED"})
	if kind != "" || reason != "" {
		t.Fatalf("terminal failure must not create pending inbox action: kind=%q reason=%q", kind, reason)
	}

	kind, reason = taskInboxAction(core.Task{Status: core.StatusBlocked, ErrorCode: "MODEL_CREDENTIAL_MISSING"})
	if kind != "task_recovery" || !strings.Contains(reason, "模型配置或凭据") {
		t.Fatalf("blocked task must remain actionable: kind=%q reason=%q", kind, reason)
	}
}

func TestRepeatedActivitySuggestionsRequireThreeOccurrences(t *testing.T) {
	got := repeatedActivitySuggestions(map[string]int{
		"task.failed":    2,
		"task.succeeded": 3,
		"tool.completed": 5,
	})
	if len(got) != 2 || got[0] != "task.succeeded 出现 3 次，可评估沉淀为确定性规则/Workflow/Skill" || got[1] != "tool.completed 出现 5 次，可评估沉淀为确定性规则/Workflow/Skill" {
		t.Fatalf("suggestions=%v", got)
	}
}
