package sqlite

import (
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestParseTransitionPlanItemsRejectsSemanticDuplicates(t *testing.T) {
	_, code := parseTransitionPlanItems(core.ToolCall{Arguments: map[string]any{"items": []any{
		map[string]any{"title": "实现并验证页面", "description": "完成页面实现。"},
		map[string]any{"title": "实现-并 验证页面", "description": "重复同一目标。"},
	}}})
	if code != "RUN_PLAN_DUPLICATE_ITEM" {
		t.Fatalf("code=%q, want RUN_PLAN_DUPLICATE_ITEM", code)
	}
}

func TestModelStageAllowsPlanReplacementOnlyForExplicitReplan(t *testing.T) {
	for _, stage := range []string{"continue", "plan_execute", "tool_repair"} {
		if modelStageAllowsPlanReplacement(stage) {
			t.Fatalf("ordinary stage %q allowed plan replacement", stage)
		}
	}
	for _, stage := range []string{"plan", "steering_replan", "permission_replan", "recovery", "completion_recovery"} {
		if !modelStageAllowsPlanReplacement(stage) {
			t.Fatalf("explicit stage %q denied plan replacement", stage)
		}
	}
}
