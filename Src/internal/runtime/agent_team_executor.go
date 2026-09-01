package runtime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (w *Worker) executeAgentTeam(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	return executeAgentTeam(ctx, step, run, w.modelInvoker())
}

func executeAgentTeam(ctx context.Context, step core.ClaimedStep, run core.RunContext, invoker ModelInvoker) core.StepOutcome {
	var input struct {
		Role            string   `json:"role"`
		Stage           string   `json:"stage"`
		CoordinatorPlan string   `json:"coordinator_plan"`
		WorkerResults   []string `json:"worker_results"`
	}
	if err := json.Unmarshal([]byte(step.InputJSON), &input); err != nil {
		return core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "AGENT_TEAM_INPUT_INVALID", ResultSummary: "Agent Team 持久化输入无法解析。", Output: map[string]any{"retryable": false}}
	}
	system := "你是 Oktapod Agent Team 的 " + input.Role + "。只完成分派给该角色的工作，明确事实、假设和风险；不得伪造工具执行或泄露密钥。"
	parts := []string{"总目标：" + run.Goal}
	maxTokens := 1536
	switch step.StepType {
	case core.StepAgentCoordinatorPlan:
		system = "你是 Oktapod Agent Team 的 Coordinator。请为 Researcher、Builder、Reviewer 划分互补子任务，写出依赖和最终验收方法。不要自己伪造三个角色的结果。"
		maxTokens = 1024
	case core.StepAgentWorker:
		parts = append(parts, "Coordinator 计划："+input.CoordinatorPlan, "你的角色："+input.Role)
	case core.StepAgentCoordinatorSynthesis:
		system = "你是 Oktapod Agent Team 的 Coordinator。基于三个 Worker 的已提交结果进行冲突消解和最终合成；不得补写不存在的执行证据。输出用户可直接验收的最终结果。"
		parts = append(parts, "Worker 结果：\n"+strings.Join(input.WorkerResults, "\n\n"))
		maxTokens = core.DefaultModelOutputTokens
	default:
		return core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "AGENT_TEAM_STEP_UNKNOWN", ResultSummary: "Agent Team 步骤类型未知。", Output: map[string]any{"retryable": false}}
	}
	if strings.TrimSpace(run.Acceptance) != "" {
		parts = append(parts, "验收标准："+run.Acceptance)
	}
	modelName, fallbackModel := agentModelsFromSnapshot(run.ConfigSnapshotJSON)
	requestedModel := modelRefFromSnapshot(run.ConfigSnapshotJSON, modelName)
	result, err := invoker.Complete(ctx, ModelRequest{SystemPrompt: system, UserPrompt: strings.Join(parts, "\n\n"), RequestedModel: requestedModel, ThinkingLevel: thinkingLevelFromSnapshot(run.ConfigSnapshotJSON), MaxOutputTokens: maxTokens, EnableTools: false, Model: modelName, FallbackModel: fallbackModel})
	if err != nil {
		status := core.StatusFailed
		if result.ErrorCode == "MODEL_CREDENTIAL_MISSING" || result.ErrorCode == "PROVIDER_AUTH" {
			status = core.StatusBlocked
		}
		return core.StepOutcome{Status: status, ErrorCode: result.ErrorCode, ResultSummary: safeErrorSummary(err), Output: map[string]any{"role": input.Role, "stage": input.Stage, "retryable": result.ErrorCode == "PROVIDER_RATE_LIMIT" || result.ErrorCode == "PROVIDER_UNAVAILABLE"}, Model: &result}
	}
	return core.StepOutcome{Status: core.StatusSucceeded, ResultSummary: result.Content, Output: map[string]any{"content": result.Content, "reasoning_content": modelResultThinking(result), "role": input.Role, "stage": input.Stage, "model": result.Model, "routing_reason": result.RoutingReason, "usage": result.Usage}, Model: &result}
}
