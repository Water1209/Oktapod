package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (w *Worker) executeHumanActionReview(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	return executeHumanActionReview(ctx, step, run, w.modelInvoker(), w.reviewerSettings)
}

func executeHumanActionReview(ctx context.Context, step core.ClaimedStep, run core.RunContext, invoker ModelInvoker, reviewerSettings func(context.Context, string) (core.AIGovernanceSettings, error)) core.StepOutcome {
	var input struct {
		ActionID       string         `json:"human_action_id"`
		Title          string         `json:"title"`
		Reason         string         `json:"reason"`
		RiskLevel      string         `json:"risk_level"`
		ToolName       string         `json:"tool_name"`
		FrozenParams   map[string]any `json:"frozen_params"`
		InputHash      string         `json:"input_hash"`
		PolicyDecision string         `json:"policy_decision"`
	}
	if json.Unmarshal([]byte(step.InputJSON), &input) != nil || input.ActionID == "" {
		return reviewerEscalation("REVIEW_INPUT_INVALID", "自动审查输入无效。", "invalid_input", nil)
	}
	if input.PolicyDecision != core.PolicyAsk {
		return reviewerEscalation("REVIEW_SCOPE_INVALID", "只有 ExecutionPolicy 判定为 ask 的动作可以进入自动审查。", "policy_not_ask", nil)
	}
	if !strings.EqualFold(input.RiskLevel, "L2") {
		return reviewerEscalation("", "只有工作区内 L2 动作可以自动审查；其他风险等级已转人工。", "risk_requires_human", nil)
	}
	if reasonCode, reason := deterministicReviewerEscalation(input.ToolName, input.FrozenParams); reasonCode != "" {
		return reviewerEscalation("", reason, reasonCode, nil)
	}
	if reviewerSettings == nil {
		return reviewerEscalation("", "未配置独立 Reviewer Model，已转人工审批。", "reviewer_not_configured", nil)
	}
	settings, err := reviewerSettings(ctx, run.OrganizationID)
	if err != nil || !settings.Enabled || strings.TrimSpace(settings.ReviewerProviderID) == "" || strings.TrimSpace(settings.ReviewerModelID) == "" {
		return reviewerEscalation("", "未配置独立 Reviewer Model，已转人工审批。", "reviewer_not_configured", nil)
	}
	payload, _ := json.Marshal(map[string]any{
		"goal":      truncateReviewerText(run.Goal, 600),
		"scope":     map[string]any{"organization_id": run.OrganizationID, "project_id": run.ProjectID, "run_id": run.RunID},
		"plan_item": truncateReviewerText(input.Title, 200), "tool": input.ToolName, "frozen_params_summary": sanitizeReviewerValue(input.FrozenParams),
		"risk_level": input.RiskLevel, "policy_reason": truncateReviewerText(input.Reason, 500),
		"execution_authorization": map[string]any{"input_hash": input.InputHash, "policy_decision": input.PolicyDecision, "policy_version": core.ToolPolicyVersion},
		"reviewer_configuration":  map[string]any{"provider_id": settings.ReviewerProviderID, "model_id": settings.ReviewerModelID, "policy_version": settings.PolicyVersion},
	})
	timeout := time.Duration(settings.TimeoutSeconds) * time.Second
	if timeout < 5*time.Second || timeout > 120*time.Second {
		timeout = 30 * time.Second
	}
	reviewCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := invoker.Complete(reviewCtx, ModelRequest{
		SystemPrompt: "你是 Oktapod 独立安全 Reviewer，没有任何工具、凭据或完整工具输出。只审查 ExecutionPolicy 已判定为 ask 的一个冻结动作。检查数据外传、凭据探测、持久安全弱化、破坏性动作和用户授权充分性。只输出 reviewer-decision-v1 严格 JSON：{\"schema_version\":\"reviewer-decision-v1\",\"decision\":\"approve|reject|escalate_to_user\",\"risk_level\":\"L2\",\"authorization\":\"once|none|human_required\",\"reason_code\":\"短机器码\",\"reason\":\"简短理由\"}。approve 只能使用 authorization=once；任何信息不足、开放世界或超出当前目标 Scope 的动作必须 escalate_to_user。",
		UserPrompt:   string(payload), RequestedModel: core.ModelRef{ProviderID: settings.ReviewerProviderID, ModelID: settings.ReviewerModelID},
		Model: settings.ReviewerModelID, MaxOutputTokens: 256, EnableTools: false,
	})
	if err != nil {
		return reviewerEscalationWithConfiguration("", "自动审查服务不可用，已转人工审批。", "reviewer_unavailable", &result, settings)
	}
	var review struct {
		SchemaVersion string `json:"schema_version"`
		Decision      string `json:"decision"`
		RiskLevel     string `json:"risk_level"`
		Authorization string `json:"authorization"`
		ReasonCode    string `json:"reason_code"`
		Reason        string `json:"reason"`
	}
	content := strings.TrimSpace(result.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &review) != nil || review.SchemaVersion != "reviewer-decision-v1" || review.RiskLevel != "L2" || strings.TrimSpace(review.ReasonCode) == "" || strings.TrimSpace(review.Reason) == "" || !validReviewerAuthorization(review.Decision, review.Authorization) {
		return reviewerEscalationWithConfiguration("", "Reviewer 输出无法按 reviewer-decision-v1 严格契约解析，已转人工。", "reviewer_output_invalid", &result, settings)
	}
	return core.StepOutcome{Status: core.StatusSucceeded, ResultSummary: review.Reason, Output: map[string]any{"schema_version": review.SchemaVersion, "decision": review.Decision, "risk_level": review.RiskLevel, "authorization": review.Authorization, "reason_code": review.ReasonCode, "reason": review.Reason, "reviewer_provider_id": settings.ReviewerProviderID, "reviewer_model_id": settings.ReviewerModelID, "reviewer_policy_version": settings.PolicyVersion}, Model: &result}
}

func reviewerEscalationWithConfiguration(errorCode, reason, reasonCode string, model *core.ModelResult, settings core.AIGovernanceSettings) core.StepOutcome {
	outcome := reviewerEscalation(errorCode, reason, reasonCode, model)
	outcome.Output["reviewer_provider_id"] = settings.ReviewerProviderID
	outcome.Output["reviewer_model_id"] = settings.ReviewerModelID
	outcome.Output["reviewer_policy_version"] = settings.PolicyVersion
	return outcome
}

func reviewerEscalation(errorCode, reason, reasonCode string, model *core.ModelResult) core.StepOutcome {
	return core.StepOutcome{Status: core.StatusSucceeded, ErrorCode: errorCode, ResultSummary: reason, Output: map[string]any{"schema_version": "reviewer-decision-v1", "decision": "escalate_to_user", "risk_level": "L2", "authorization": "human_required", "reason_code": reasonCode, "reason": reason}, Model: model}
}

func validReviewerAuthorization(decision, authorization string) bool {
	switch decision {
	case "approve":
		return authorization == "once"
	case "reject":
		return authorization == "none"
	case "escalate_to_user":
		return authorization == "human_required"
	default:
		return false
	}
}

func deterministicReviewerEscalation(toolName string, params map[string]any) (string, string) {
	encoded, _ := json.Marshal(sanitizeReviewerValue(params))
	probe := strings.ToLower(toolName + " " + string(encoded))
	credentialSignals := []string{"password", "secret", "api_key", "apikey", "authorization", ".ssh", ".aws", "credentials"}
	for _, signal := range credentialSignals {
		if strings.Contains(probe, signal) {
			return "credential_access", "动作涉及凭据或敏感身份材料，必须人工确认。"
		}
	}
	destructiveSignals := []string{"rm -rf", "drop database", "drop table", "format ", "delete all", "chmod 777", "disable security", "git_push", "force push"}
	for _, signal := range destructiveSignals {
		if strings.Contains(probe, signal) {
			return "destructive_or_persistent", "动作可能造成破坏或持久安全弱化，必须人工确认。"
		}
	}
	return "", ""
}

func sanitizeReviewerValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "authorization") {
				result[key] = "[REDACTED]"
				continue
			}
			result[key] = sanitizeReviewerValue(child)
		}
		return result
	case []any:
		limit := len(typed)
		if limit > 24 {
			limit = 24
		}
		result := make([]any, 0, limit)
		for _, child := range typed[:limit] {
			result = append(result, sanitizeReviewerValue(child))
		}
		return result
	case string:
		return truncateReviewerText(typed, 500)
	default:
		return value
	}
}

func truncateReviewerText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
