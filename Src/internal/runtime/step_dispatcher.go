package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type stepExecutor interface {
	Execute(context.Context, core.ClaimedStep, core.RunContext) core.StepOutcome
}

// StepDispatcher is a fixed runtime map, not a plugin registry. Durable
// lifecycle ownership remains in Worker; this type only chooses the executor
// for an already claimed and checkpointed step.
type StepDispatcher struct {
	mainAgent stepExecutor
	reviewer  stepExecutor
	synthesis stepExecutor
	agentTeam stepExecutor
	tools     stepExecutor
}

func (dispatcher StepDispatcher) Execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	switch {
	case step.StepType == core.StepHumanActionReview:
		return dispatcher.reviewer.Execute(ctx, step, run)
	case run.Mode == core.ModeAgentTeam:
		return dispatcher.agentTeam.Execute(ctx, step, run)
	case step.StepType == core.StepModelSynthesis:
		return dispatcher.synthesis.Execute(ctx, step, run)
	case isToolRuntimeStep(step.StepType):
		return dispatcher.tools.Execute(ctx, step, run)
	default:
		return dispatcher.mainAgent.Execute(ctx, step, run)
	}
}

func (w *Worker) stepDispatcher() StepDispatcher {
	return StepDispatcher{
		mainAgent: MainAgentExecutor{assembler: NewAgentTurnAssembler(w.repository), invoker: w.modelInvoker(), liveTurns: w.liveTurns},
		reviewer:  ReviewExecutor{invoker: w.modelInvoker(), settings: w.reviewerSettings},
		synthesis: SynthesisExecutor{invoker: w.modelInvoker()},
		agentTeam: AgentTeamExecutor{invoker: w.modelInvoker()},
		tools:     ToolStepExecutor{runtime: w.toolRuntime()},
	}
}

type MainAgentExecutor struct {
	assembler AgentTurnAssembler
	invoker   ModelInvoker
	liveTurns core.LiveTurnPublisher
}

func (executor MainAgentExecutor) Execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	assemblyStarted := time.Now()
	assembly, assemblyFailure := executor.assembler.Assemble(ctx, step, run)
	if assemblyFailure != nil {
		return *assemblyFailure
	}
	assemblyLatency := time.Since(assemblyStarted)
	request := assembly.Request
	contextPlan := request.ContextPlan
	toolDefinitions := request.ToolDefinitions
	searchBudget := assembly.SearchBudget
	priorSearchCalls := assembly.PriorSearchCalls
	continuation := assembly.Continuation
	requestPayload, _ := json.Marshal(request)
	emitter := newLiveDeltaEmitter(executor.liveTurns, run.RunID, step.ID)
	if executor.liveTurns != nil {
		executor.liveTurns.PublishLiveTurn(core.LiveTurnEvent{RunID: run.RunID, ModelCallID: step.ID, Kind: "phase", Phase: "model_started"})
	}
	result, err := executor.invoker.CompleteLive(ctx, request, emitter.Add)
	emitter.Close()
	result.AssemblyLatency = assemblyLatency
	if result.RequestBytes == 0 {
		result.RequestBytes = int64(len(requestPayload))
	}
	if err != nil {
		status := core.StatusFailed
		if result.ErrorCode == "MODEL_CREDENTIAL_MISSING" || result.ErrorCode == "PROVIDER_AUTH" {
			status = core.StatusBlocked
		}
		output := map[string]any{
			"retryable":    isRetryableModelInteractionError(result.ErrorCode),
			"model":        result.Model,
			"context_plan": contextPlan,
		}
		if failure := modelFailureDetails(err); failure != nil {
			output["model_failure"] = failure
		}
		return core.StepOutcome{
			Status:        status,
			ErrorCode:     result.ErrorCode,
			ResultSummary: modelFailureSummary(result.ErrorCode, err),
			Output:        output,
			Model:         &result,
		}
	}
	annotateToolCallExecutionMode(result.ToolCalls, toolDefinitions)
	if searchBudget.Enforced && searchCallBudgetExceededWithPrior(continuation, result.ToolCalls, searchBudget.Limit, priorSearchCalls) {
		summary := fmt.Sprintf("任务明确限制最多执行 %d 次搜索；超限调用未执行，系统将使用已有证据继续。", searchBudget.Limit)
		if searchBudget.Limit == 0 {
			summary = "用户已明确禁止继续搜索；违规调用未执行，系统将使用已有证据完成任务。"
		}
		return core.StepOutcome{
			Status:        core.StatusFailed,
			ErrorCode:     "TOOL_BUDGET_EXCEEDED",
			ResultSummary: summary,
			Output: map[string]any{
				"retryable":    true,
				"search_limit": searchBudget.Limit,
				"search_used":  priorSearchCalls,
				"context_plan": contextPlan,
			},
			Model: &result,
		}
	}
	result, truncatedToolCalls := normalizeLengthTruncatedModelResult(result)
	if result.StopReason == core.StopReasonLength && len(truncatedToolCalls) == 0 && run.Mode == core.ModeSingleAgent {
		return core.StepOutcome{
			Status:        core.StatusFailed,
			ErrorCode:     "PROVIDER_TOOL_ARGUMENTS_TRUNCATED",
			ResultSummary: "模型输出在工具调用完成前达到长度上限；系统未执行不完整动作，正在缩短参数并自动续写。",
			Output: map[string]any{
				"retryable":    true,
				"model":        result.Model,
				"context_plan": contextPlan,
			},
			Model: &result,
		}
	}

	return core.StepOutcome{
		Status:        core.StatusSucceeded,
		ResultSummary: result.Content,
		Output: map[string]any{
			"content":                     result.Content,
			"reasoning_content":           modelResultThinking(result),
			"model":                       result.Model,
			"routing_reason":              result.RoutingReason,
			"usage":                       result.Usage,
			"context_plan":                contextPlan,
			"length_truncated_tool_calls": truncatedToolCalls,
		},
		Model: &result,
	}
}

type ReviewExecutor struct {
	invoker  ModelInvoker
	settings func(context.Context, string) (core.AIGovernanceSettings, error)
}

func (executor ReviewExecutor) Execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	return executeHumanActionReview(ctx, step, run, executor.invoker, executor.settings)
}

type SynthesisExecutor struct{ invoker ModelInvoker }

func (executor SynthesisExecutor) Execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	return executeModelSynthesis(ctx, step, run, executor.invoker)
}

type AgentTeamExecutor struct{ invoker ModelInvoker }

func (executor AgentTeamExecutor) Execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	return executeAgentTeam(ctx, step, run, executor.invoker)
}

type ToolStepExecutor struct{ runtime ToolRuntime }

func (executor ToolStepExecutor) Execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	return executor.runtime.Execute(ctx, step, run)
}
