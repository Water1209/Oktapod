package application

import (
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
