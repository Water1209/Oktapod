package runtime

import (
	"context"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type reviewerModelStub struct {
	result core.ModelResult
}

func (m reviewerModelStub) Complete(context.Context, ModelRequest) (core.ModelResult, error) {
	return m.result, nil
}

func TestHumanActionReviewFallsBackToUserWithoutReviewerModel(t *testing.T) {
	t.Setenv("OKTAPOD_REVIEWER_MODEL", "")
	worker := &Worker{}
	outcome := worker.executeHumanActionReview(context.Background(), core.ClaimedStep{
		InputJSON: `{"human_action_id":"action-1","title":"运行命令","risk_level":"L2","tool_name":"workspace_run_command","frozen_params":{"program":"go","args":["version"]}}`,
	}, core.RunContext{})
	if outcome.Status != core.StatusSucceeded || outcome.Output["decision"] != "escalate_to_user" {
		t.Fatalf("outcome=%#v", outcome)
	}
}

func TestHumanActionReviewNeverAutoApprovesHighRisk(t *testing.T) {
	t.Setenv("OKTAPOD_REVIEWER_MODEL", "reviewer-test")
	worker := &Worker{model: reviewerModelStub{result: core.ModelResult{Content: `{"decision":"approve","reason":"looks fine"}`}}}
	outcome := worker.executeHumanActionReview(context.Background(), core.ClaimedStep{
		InputJSON: `{"human_action_id":"action-2","title":"开放世界动作","risk_level":"L3","tool_name":"mcp__server__delete","frozen_params":{"target":"all"}}`,
	}, core.RunContext{})
	if outcome.Status != core.StatusSucceeded || outcome.Output["decision"] != "escalate_to_user" {
		t.Fatalf("outcome=%#v", outcome)
	}
}
