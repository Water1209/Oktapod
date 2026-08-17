package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type reviewerModelStub struct {
	result core.ModelResult
	err    error
	seen   *ModelRequest
}

func (m reviewerModelStub) Complete(_ context.Context, request ModelRequest) (core.ModelResult, error) {
	if m.seen != nil {
		*m.seen = request
	}
	return m.result, m.err
}

func TestHumanActionReviewFallsBackToUserWithoutReviewerModel(t *testing.T) {
	worker := &Worker{}
	outcome := worker.executeHumanActionReview(context.Background(), core.ClaimedStep{
		InputJSON: `{"human_action_id":"action-1","title":"运行命令","risk_level":"L2","tool_name":"workspace_run_command","frozen_params":{"program":"go","args":["version"]}}`,
	}, core.RunContext{})
	if outcome.Status != core.StatusSucceeded || outcome.Output["decision"] != "escalate_to_user" {
		t.Fatalf("outcome=%#v", outcome)
	}
}

func TestHumanActionReviewNeverAutoApprovesHighRisk(t *testing.T) {
	worker := &Worker{model: reviewerModelStub{result: core.ModelResult{Content: `{"decision":"approve","reason":"looks fine"}`}}}
	outcome := worker.executeHumanActionReview(context.Background(), core.ClaimedStep{
		InputJSON: `{"human_action_id":"action-2","title":"开放世界动作","risk_level":"L3","tool_name":"mcp__server__delete","frozen_params":{"target":"all"}}`,
	}, core.RunContext{})
	if outcome.Status != core.StatusSucceeded || outcome.Output["decision"] != "escalate_to_user" {
		t.Fatalf("outcome=%#v", outcome)
	}
}

func TestHumanActionReviewApprovesOnlyVersionedSingleAuthorization(t *testing.T) {
	var request ModelRequest
	worker := &Worker{model: reviewerModelStub{seen: &request, result: core.ModelResult{Content: `{"schema_version":"reviewer-decision-v1","decision":"approve","risk_level":"L2","authorization":"once","reason_code":"bounded_workspace_command","reason":"参数和范围明确"}`}}, reviewerSettings: configuredReviewerSettings}
	outcome := worker.executeHumanActionReview(context.Background(), core.ClaimedStep{
		InputJSON: `{"human_action_id":"action-3","title":"运行测试","reason":"工作区内受限命令","risk_level":"L2","tool_name":"workspace_run_command","input_hash":"sha256:test","policy_decision":"ask","frozen_params":{"program":"go","args":["test","./..."]}}`,
	}, core.RunContext{OrganizationID: "org-1", ProjectID: "project-1", RunID: "run-1", Goal: "验证项目"})
	if outcome.Output["decision"] != "approve" || outcome.Output["authorization"] != "once" {
		t.Fatalf("outcome=%#v", outcome)
	}
	if request.EnableTools || !strings.Contains(request.UserPrompt, `"policy_decision":"ask"`) || !strings.Contains(request.UserPrompt, `"project_id":"project-1"`) {
		t.Fatalf("review request=%#v", request)
	}
	if !strings.Contains(request.UserPrompt, `"policy_version":7`) || outcome.Output["reviewer_provider_id"] != "provider-test" || outcome.Output["reviewer_model_id"] != "reviewer-test" || outcome.Output["reviewer_policy_version"] != 7 {
		t.Fatalf("reviewer configuration was not frozen: request=%s output=%#v", request.UserPrompt, outcome.Output)
	}
}

func TestHumanActionReviewFailsClosedForUnavailableOrInvalidOutput(t *testing.T) {
	input := core.ClaimedStep{InputJSON: `{"human_action_id":"action-4","risk_level":"L2","tool_name":"workspace_run_command","policy_decision":"ask","frozen_params":{"program":"go","args":["version"]}}`}
	for name, model := range map[string]reviewerModelStub{
		"unavailable": {err: errors.New("timeout")},
		"invalid":     {result: core.ModelResult{Content: `{"decision":"approve"}`}},
	} {
		t.Run(name, func(t *testing.T) {
			outcome := (&Worker{model: model, reviewerSettings: configuredReviewerSettings}).executeHumanActionReview(context.Background(), input, core.RunContext{})
			if outcome.Output["decision"] != "escalate_to_user" || outcome.Output["authorization"] != "human_required" {
				t.Fatalf("outcome=%#v", outcome)
			}
		})
	}
}

func configuredReviewerSettings(_ context.Context, _ string) (core.AIGovernanceSettings, error) {
	return core.AIGovernanceSettings{Enabled: true, ReviewerProviderID: "provider-test", ReviewerModelID: "reviewer-test", TimeoutSeconds: 30, FailClosed: true, PolicyVersion: 7}, nil
}

func TestHumanActionReviewEscalatesCredentialAccessBeforeModel(t *testing.T) {
	worker := &Worker{model: reviewerModelStub{result: core.ModelResult{Content: `{"schema_version":"reviewer-decision-v1","decision":"approve","risk_level":"L2","authorization":"once","reason_code":"ok","reason":"ok"}`}}}
	outcome := worker.executeHumanActionReview(context.Background(), core.ClaimedStep{
		InputJSON: `{"human_action_id":"action-5","risk_level":"L2","tool_name":"workspace_shell","policy_decision":"ask","frozen_params":{"command":"read .ssh credentials"}}`,
	}, core.RunContext{})
	if outcome.Output["decision"] != "escalate_to_user" || outcome.Output["reason_code"] != "credential_access" {
		t.Fatalf("outcome=%#v", outcome)
	}
}
