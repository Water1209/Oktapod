package runtime

import "testing"

func TestTransitionPlanReplacementVisibility(t *testing.T) {
	tests := []struct {
		name      string
		inputJSON string
		visible   bool
	}{
		{name: "initial", inputJSON: `{"stage":"execute"}`, visible: true},
		{name: "bound continuation", inputJSON: `{"stage":"continue","plan_revision_id":"plan-1","active_plan_total":3}`, visible: false},
		{name: "bound plan execute", inputJSON: `{"stage":"plan_execute","plan_revision_id":"plan-1","active_plan_total":3}`, visible: false},
		{name: "steering", inputJSON: `{"stage":"steering_replan","plan_revision_id":"plan-1","active_plan_total":3}`, visible: true},
		{name: "permission", inputJSON: `{"stage":"permission_replan","plan_revision_id":"plan-1","active_plan_total":3}`, visible: true},
		{name: "completion recovery", inputJSON: `{"stage":"completion_recovery","plan_revision_id":"plan-1","active_plan_total":3}`, visible: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := transitionPlanReplacementVisible(test.inputJSON); got != test.visible {
				t.Fatalf("visible=%v, want %v", got, test.visible)
			}
		})
	}
}
