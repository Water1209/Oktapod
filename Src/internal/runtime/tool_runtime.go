package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	mcpadapter "github.com/oktapod/oktapod/Src/internal/adapters/mcp"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	durableharness "github.com/oktapod/oktapod/Src/internal/harness"
)

// ToolExecutionLedger is the only new runtime port. Its implementation remains
// the Durable Harness store, which owns authorization and Receipt truth.
type ToolExecutionLedger interface {
	BeginToolExecution(context.Context, core.ClaimedStep, core.RunContext, core.ToolCall, string, bool, string) (string, error)
}

// FrozenToolInvocation is the exact persisted contract decoded by every tool
// family. Batch and plan metadata are accepted but never trusted for execution.
type FrozenToolInvocation struct {
	ToolCallID        string         `json:"tool_call_id"`
	Name              string         `json:"name"`
	Arguments         map[string]any `json:"arguments"`
	InputHash         string         `json:"input_hash"`
	Idempotent        bool           `json:"idempotent"`
	ModelSourceStepID string         `json:"model_source_step_id"`
	PlanIteration     int64          `json:"plan_iteration"`
	ToolBatchID       string         `json:"tool_batch_id"`
	BatchIndex        int            `json:"batch_index"`
	BatchExecution    string         `json:"batch_execution_mode"`
	PlanRevisionID    string         `json:"plan_revision_id"`
	RunPlanItemID     string         `json:"run_plan_item_id"`
	InvokedSkillIDs   []string       `json:"invoked_skill_ids"`
	SkillArgs         string         `json:"skill_args"`
}

type ToolRuntime struct {
	ledger          ToolExecutionLedger
	repository      WorkerRepository
	workspace       WorkspaceExecutor
	artifactCapture application.ArtifactCapture
	assetStager     application.AssetStager
	documentRuntime application.DocumentExecutor
	webSearch       WebSearchExecutor
	webAccess       WebAccess
	mcpSecrets      application.ProviderSecretStore
	mcpSessions     *mcpadapter.SessionBroker
	webCallTimeout  time.Duration
	mcpCallTimeout  time.Duration
	logger          *slog.Logger
}

type preparedTool struct {
	dispatch       func(context.Context) core.ToolExecution
	receiptUnknown string
	successSummary string
	failureSummary string
}

func (w *Worker) toolRuntime() ToolRuntime {
	return ToolRuntime{
		ledger: w.repository, repository: w.repository, workspace: w.workspace,
		artifactCapture: w.artifactCapture, assetStager: w.assetStager, documentRuntime: w.documentRuntime,
		webSearch: w.webSearch, webAccess: w.webAccess, mcpSecrets: w.mcpSecrets, mcpSessions: w.mcpSessions, webCallTimeout: w.webCallTimeout, mcpCallTimeout: w.mcpCallTimeout, logger: w.logger,
	}
}

func isToolRuntimeStep(stepType string) bool {
	return stepType == core.ToolSkillInvoke || stepType == core.ToolWebSearch || stepType == core.ToolWebFetch || isWorkspaceTool(stepType) || isDocumentTool(stepType) || isMCPTool(stepType)
}

func (runtime ToolRuntime) Execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	if ctx.Err() != nil {
		return core.StepOutcome{Status: core.StatusFailed, ErrorCode: "TASK_CANCELED", ResultSummary: "任务已在工具派发前取消。", Output: map[string]any{"retryable": false}}
	}
	invocation, err := decodeFrozenToolInvocation(step.InputJSON)
	if err != nil {
		return toolInputFailure(step.StepType)
	}
	if !toolIdentityMatches(step.StepType, invocation.Name) {
		return core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "TOOL_CONTRACT_MISMATCH", ResultSummary: "Persisted tool identity does not match the claimed step.", Output: map[string]any{"retryable": false}}
	}
	call := core.ToolCall{ID: invocation.ToolCallID, Name: invocation.Name, Arguments: invocation.Arguments}
	planned := core.PlanTool(call)
	if invocation.Name == core.ToolWebFetch && runtime.webAccess != nil && planned.Decision != core.PolicyDeny {
		decision := runtime.webAccess.EvaluateFetch(run.ConfigSnapshotJSON, stringArgument(invocation.Arguments, "url"))
		planned.RiskLevel, planned.Decision, planned.Reason = decision.RiskLevel, decision.Decision, decision.Reason
	}
	if isMCPTool(invocation.Name) {
		risk, idempotent, reason := application.MCPPolicyFromSnapshot(run.ConfigSnapshotJSON, invocation.Name)
		planned.RiskLevel, planned.Idempotent = risk, idempotent
		if reason != "" {
			planned.Reason = reason
		}
	}
	if planned.Decision == core.PolicyDeny || planned.InputHash == "" || planned.InputHash != invocation.InputHash || planned.Idempotent != invocation.Idempotent {
		reason := fmt.Sprintf("%s (decision=%s, input_hash_match=%t, idempotency_match=%t)", planned.Reason, planned.Decision, planned.InputHash == invocation.InputHash, planned.Idempotent == invocation.Idempotent)
		if runtime.logger != nil {
			runtime.logger.WarnContext(ctx, "tool runtime preflight denied", "step_id", step.ID, "tool_name", invocation.Name, "reason", reason)
		}
		return toolPolicyFailure(step.StepType, reason)
	}
	prepared, failure := newToolPreparerRegistry(runtime).Prepare(ctx, step, run, invocation, planned)
	if failure != nil {
		return *failure
	}
	invocationID, beginErr := runtime.begin(ctx, step, run, call, invocation.InputHash, invocation.Idempotent)
	if beginErr != nil {
		return toolReceiptBeginFailure(beginErr, prepared.receiptUnknown)
	}
	execution := prepared.dispatch(ctx)
	execution.ToolCallID = invocation.ToolCallID
	execution.InvocationID = invocationID
	execution.InputHash = invocation.InputHash
	execution.Idempotent = invocation.Idempotent
	if execution.Output == nil {
		execution.Output = map[string]any{}
	}
	status, summary := core.StatusSucceeded, prepared.successSummary
	if execution.Status != core.StatusSucceeded {
		status, summary = core.StatusFailed, prepared.failureSummary
	}
	return core.StepOutcome{Status: status, ErrorCode: execution.ErrorCode, ResultSummary: summary, Output: execution.Output, ToolExecution: &execution}
}

func decodeFrozenToolInvocation(inputJSON string) (FrozenToolInvocation, error) {
	decoder := json.NewDecoder(bytes.NewBufferString(inputJSON))
	decoder.DisallowUnknownFields()
	var invocation FrozenToolInvocation
	if err := decoder.Decode(&invocation); err != nil {
		return FrozenToolInvocation{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return FrozenToolInvocation{}, errors.New("tool input contains trailing JSON")
	}
	if strings.TrimSpace(invocation.ToolCallID) == "" || strings.TrimSpace(invocation.Name) == "" || strings.TrimSpace(invocation.InputHash) == "" || invocation.Arguments == nil {
		return FrozenToolInvocation{}, errors.New("tool input is incomplete")
	}
	return invocation, nil
}

func toolIdentityMatches(stepType, invocationName string) bool {
	if isMCPTool(stepType) || isMCPTool(invocationName) {
		return stepType == invocationName
	}
	return core.ResolveToolName(stepType) == core.ResolveToolName(invocationName)
}

func toolInputFailure(stepType string) core.StepOutcome {
	code, summary := "TOOL_INPUT_INVALID", "Persisted tool input could not be decoded."
	if stepType == core.ToolSkillInvoke {
		code, summary = "SKILL_INVOKE_INPUT_INVALID", "Skill 调用输入无法解析。"
	} else if stepType == core.ToolWebSearch {
		code, summary = "WEB_SEARCH_INPUT_INVALID", "互联网搜索输入无法解析。"
	} else if stepType == core.ToolWebFetch {
		code, summary = "WEB_FETCH_INPUT_INVALID", "网页读取输入无法解析。"
	} else if isMCPTool(stepType) {
		code, summary = "MCP_TOOL_INPUT_INVALID", "MCP 工具输入无法解析。"
	}
	return core.StepOutcome{Status: core.StatusBlocked, ErrorCode: code, ResultSummary: summary, Output: map[string]any{"retryable": false}}
}

func toolPolicyFailure(stepType, reason string) core.StepOutcome {
	summary := "The frozen tool input failed deterministic policy validation."
	if stepType == core.ToolSkillInvoke {
		summary = "Skill 调用未通过冻结策略校验。"
	} else if stepType == core.ToolWebSearch {
		summary = "互联网搜索输入未通过冻结策略校验。"
	} else if stepType == core.ToolWebFetch {
		summary = "网页读取输入未通过冻结 Web Access 策略校验。"
	} else if isMCPTool(stepType) {
		summary = "MCP 工具输入未通过冻结策略校验。"
	}
	return core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "TOOL_POLICY_DENIED", ResultSummary: summary, Output: map[string]any{"reason": reason, "retryable": false}}
}

func (runtime ToolRuntime) begin(ctx context.Context, step core.ClaimedStep, run core.RunContext, call core.ToolCall, inputHash string, idempotent bool) (string, error) {
	var invocationID string
	err := durableharness.RetryBusy(ctx, func() error {
		var beginErr error
		invocationID, beginErr = runtime.ledger.BeginToolExecution(ctx, step, run, call, inputHash, idempotent, newCorrelationID())
		return beginErr
	})
	if err != nil && runtime.logger != nil {
		runtime.logger.WarnContext(ctx, "tool execution authorization failed", "step_id", step.ID, "tool_name", call.Name, "error", safeErrorSummary(err))
	}
	return invocationID, err
}

func toolReceiptBeginFailure(err error, summary string) core.StepOutcome {
	if err != nil && strings.Contains(err.Error(), "SIDE_EFFECT_STATUS_UNKNOWN") {
		return core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SIDE_EFFECT_STATUS_UNKNOWN", ResultSummary: summary, Output: map[string]any{"retryable": false}}
	}
	return core.StepOutcome{Status: core.StatusFailed, ErrorCode: "TOOL_RECEIPT_BEGIN_FAILED", ResultSummary: "工具执行收据暂时无法持久化，系统将从已提交检查点安全重试。", Output: map[string]any{"retryable": true}}
}

func (runtime ToolRuntime) preflightDocument(run core.RunContext, invocation FrozenToolInvocation) (preparedTool, *core.StepOutcome) {
	if runtime.documentRuntime == nil {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "DOCUMENT_RUNTIME_UNAVAILABLE", ResultSummary: "共享文档运行时未安装或健康检查失败。", Output: map[string]any{"retryable": false, "repair": "/resource-catalog/document-runtime:install"}}
		return preparedTool{}, &failure
	}
	var snapshot struct {
		DocumentRuntime struct {
			Protocol       string `json:"protocol"`
			RuntimeVersion string `json:"runtime_version"`
		} `json:"document_runtime"`
	}
	if json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot) != nil || snapshot.DocumentRuntime.Protocol != "oktapod.document/v1" {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "DOCUMENT_RUNTIME_NOT_SNAPSHOTTED", ResultSummary: "文档运行时没有在 Run 创建时冻结，拒绝临时扩权。", Output: map[string]any{"retryable": false}}
		return preparedTool{}, &failure
	}
	healthCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	health := runtime.documentRuntime.Health(healthCtx)
	cancel()
	if !health.Available || health.Version != snapshot.DocumentRuntime.RuntimeVersion || health.Protocol != snapshot.DocumentRuntime.Protocol {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "DOCUMENT_RUNTIME_VERSION_UNAVAILABLE", ResultSummary: "当前主机缺少该 Run 冻结的文档运行时版本。", Output: map[string]any{"retryable": true, "required_version": snapshot.DocumentRuntime.RuntimeVersion, "available_version": health.Version}}
		return preparedTool{}, &failure
	}
	call := core.ToolCall{ID: invocation.ToolCallID, Name: invocation.Name, Arguments: invocation.Arguments}
	return preparedTool{
		dispatch:       func(ctx context.Context) core.ToolExecution { return runtime.documentRuntime.Execute(ctx, run, call) },
		receiptUnknown: "文档操作收据状态未知，已停止可能重复的自动写入。",
		successSummary: "文档操作已通过修订、Hash 与运行时协议校验。",
		failureSummary: "文档操作失败，原件与已提交修订保持不变。",
	}, nil
}

func (runtime ToolRuntime) preflightSkill(run core.RunContext, invocation FrozenToolInvocation) (preparedTool, *core.StepOutcome) {
	requested, _ := invocation.Arguments["skill"].(string)
	requested = strings.TrimSpace(requested)
	args, _ := invocation.Arguments["args"].(string)
	matches := make([]skillSnapshot, 0, 1)
	for _, skill := range skillSnapshotsFromConfig(run.ConfigSnapshotJSON) {
		if skillReferenceMatches(skill, requested) {
			matches = append(matches, skill)
		}
	}
	if len(matches) == 0 {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SKILL_NOT_SNAPSHOTTED", ResultSummary: "模型请求的 Skill 不在当前 Run 的冻结快照中。", Output: map[string]any{"requested_skill": requested, "retryable": false}}
		return preparedTool{}, &failure
	}
	if len(matches) > 1 {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SKILL_NAME_AMBIGUOUS", ResultSummary: "多个 Skill 使用了相同名称，必须按 Skill id 调用。", Output: map[string]any{"requested_skill": requested, "retryable": false}}
		return preparedTool{}, &failure
	}
	selected := matches[0]
	var manifest map[string]any
	if json.Unmarshal([]byte(selected.ManifestJSON), &manifest) != nil {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SKILL_MANIFEST_INVALID", ResultSummary: "Run 快照中的 Skill manifest 无法解析。", Output: map[string]any{"skill_id": selected.ID, "retryable": false}}
		return preparedTool{}, &failure
	}
	if allowed, exists := manifest["model_invocable"].(bool); exists && !allowed {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SKILL_MODEL_INVOCATION_DENIED", ResultSummary: "该 Skill 只允许用户显式调用。", Output: map[string]any{"skill_id": selected.ID, "skill_name": selected.Name, "retryable": false}}
		return preparedTool{}, &failure
	}
	output := map[string]any{"skill_id": selected.ID, "skill_name": selected.Name, "version": selected.Version, "content_hash": selected.ContentHash, "allowed_tools": manifestStringList(manifest["allowed_tools"]), "skill_args": strings.TrimSpace(args), "has_args": strings.TrimSpace(args) != ""}
	return preparedTool{
		dispatch: func(context.Context) core.ToolExecution {
			return core.ToolExecution{Status: core.StatusSucceeded, ExitCode: 0, Output: output}
		},
		receiptUnknown: "Skill 加载收据状态未知，已停止自动重试。", successSummary: "Skill 已从 Run 快照加载，准备恢复 Agent Loop。", failureSummary: "Skill 加载失败，已记录错误收据。",
	}, nil
}

func (runtime ToolRuntime) preflightWeb(run core.RunContext, invocation FrozenToolInvocation) (preparedTool, *core.StepOutcome) {
	if runtime.webAccess == nil && (invocation.Name == core.ToolWebFetch || runtime.webSearch == nil) {
		code, summary := "WEB_ACCESS_NOT_CONFIGURED", "当前运行时未配置 Web Access 执行器。"
		if invocation.Name == core.ToolWebSearch {
			code, summary = "WEB_SEARCH_NOT_CONFIGURED", "当前运行时未配置互联网搜索执行器。"
		}
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: code, ResultSummary: summary, Output: map[string]any{"retryable": false}}
		return preparedTool{}, &failure
	}
	call := core.ToolCall{ID: invocation.ToolCallID, Name: invocation.Name, Arguments: invocation.Arguments}
	if invocation.Name == core.ToolWebFetch {
		return preparedTool{
			dispatch: func(ctx context.Context) core.ToolExecution {
				callContext, cancel := runtime.webContext(ctx)
				defer cancel()
				result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusSucceeded, ExitCode: 0, Idempotent: true, Output: map[string]any{}}
				fetched, err := runtime.webAccess.Fetch(callContext, core.WebFetchRequest{URL: stringArgument(call.Arguments, "url"), PolicySnapshot: run.ConfigSnapshotJSON})
				if err != nil {
					result.Status, result.ExitCode = core.StatusFailed, -1
					result.ErrorCode, result.Output["reason"], result.Output["redirect_target"] = webAccessError(err)
					if errors.Is(callContext.Err(), context.DeadlineExceeded) {
						result.ErrorCode = "WEB_FETCH_TIMEOUT"
						result.Output["reason"] = "web fetch exceeded the configured execution deadline"
					}
					if result.Output["redirect_target"] == "" {
						delete(result.Output, "redirect_target")
					}
					return result
				}
				body, _ := json.Marshal(fetched)
				_ = json.Unmarshal(body, &result.Output)
				result.Output["_envelope"] = map[string]any{"trust": core.TrustExternalUntrusted, "source": "builtin:web_fetch", "capability": "web.fetch"}
				return result
			},
			receiptUnknown: "网页读取收据状态未知，已停止自动重试。", successSummary: "网页读取完成，受限正文已进入来源综合。", failureSummary: "网页读取失败，已记录受控错误收据。",
		}, nil
	}
	return preparedTool{
		dispatch: func(ctx context.Context) core.ToolExecution {
			callContext, cancel := runtime.webContext(ctx)
			defer cancel()
			var execution core.ToolExecution
			if runtime.webAccess == nil {
				execution = runtime.webSearch.Execute(callContext, call)
			} else {
				execution = executeWebSearch(callContext, runtime.webAccess, call)
			}
			if errors.Is(callContext.Err(), context.DeadlineExceeded) {
				execution.Status = core.StatusFailed
				execution.ExitCode = -1
				execution.ErrorCode = "WEB_SEARCH_TIMEOUT"
				execution.Output = map[string]any{"query": stringArgument(call.Arguments, "query"), "reason": "web search exceeded the configured execution deadline"}
			}
			if execution.Output == nil {
				execution.Output = map[string]any{}
			}
			execution.Output["_envelope"] = map[string]any{"trust": core.TrustExternalUntrusted, "source": "builtin:web_search", "capability": "web.search"}
			return execution
		},
		receiptUnknown: "互联网搜索收据状态未知，已停止自动重试。", successSummary: "互联网搜索完成，结果已保存并进入来源综合。", failureSummary: "互联网搜索失败，已记录受控错误收据。",
	}, nil
}

func (runtime ToolRuntime) webContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if runtime.webCallTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, runtime.webCallTimeout)
}

func executeWebSearch(ctx context.Context, access WebAccess, call core.ToolCall) core.ToolExecution {
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusSucceeded, ExitCode: 0, Idempotent: true, Output: map[string]any{}}
	request := core.WebSearchRequest{Query: stringArgument(call.Arguments, "query"), MaxResults: 5}
	response, err := access.Search(ctx, request)
	if err != nil {
		result.Status, result.ExitCode, result.ErrorCode = core.StatusFailed, -1, "WEB_SEARCH_UNAVAILABLE"
		if errors.Is(err, context.Canceled) {
			result.ErrorCode = "TOOL_CANCELED"
		} else if errors.Is(err, context.DeadlineExceeded) {
			result.ErrorCode = "WEB_SEARCH_TIMEOUT"
		}
		result.Output = map[string]any{"query": request.Query, "reason": safeErrorSummary(err)}
		return result
	}
	result.Output = map[string]any{"query": response.Query, "provider": response.Provider, "result_count": len(response.Results), "results": response.Results, "duration_ms": response.Duration, "truncated": response.Truncated}
	return result
}

func webAccessError(err error) (code, reason, redirectTarget string) {
	type coded interface{ ErrorCode() string }
	code = "WEB_FETCH_UNAVAILABLE"
	var value coded
	if errors.As(err, &value) && strings.TrimSpace(value.ErrorCode()) != "" {
		code = value.ErrorCode()
	}
	reason = safeErrorSummary(err)
	type redirecting interface{ RedirectURL() string }
	var redirect redirecting
	if errors.As(err, &redirect) {
		redirectTarget = redirect.RedirectURL()
	}
	return
}

func stringArgument(arguments map[string]any, name string) string {
	value, _ := arguments[name].(string)
	return strings.TrimSpace(value)
}

func (runtime ToolRuntime) preflightWorkspace(run core.RunContext, invocation FrozenToolInvocation, planned core.PlannedTool) (preparedTool, *core.StepOutcome) {
	if (invocation.Name == core.ToolWorkspaceRunCommand || invocation.Name == core.ToolWorkspaceShell) && !sceneAllowsWorkspaceCommand(run.ConfigSnapshotJSON) {
		failure := core.StepOutcome{Status: core.StatusFailed, ErrorCode: "SCENE_POLICY_DENIED", ResultSummary: "当前 Work 场景不允许执行工作区命令，系统将保留已完成步骤并改用允许的工具重新规划。", Output: map[string]any{"retryable": true, "scene": "work", "recovery": "replan"}}
		return preparedTool{}, &failure
	}
	if invocation.Name == core.ToolArtifactCapture && runtime.artifactCapture == nil {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "ARTIFACT_CAPTURE_UNAVAILABLE", ResultSummary: "Artifact Capture is not configured.", Output: map[string]any{"retryable": false}}
		return preparedTool{}, &failure
	}
	if invocation.Name == core.ToolAssetStage && runtime.assetStager == nil {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "ASSET_STAGER_UNAVAILABLE", ResultSummary: "Asset staging is not configured.", Output: map[string]any{"retryable": false}}
		return preparedTool{}, &failure
	}
	if invocation.Name != core.ToolArtifactCapture && invocation.Name != core.ToolAssetStage && runtime.workspace == nil {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "WORKSPACE_EXECUTOR_UNAVAILABLE", ResultSummary: "The workspace executor is not configured.", Output: map[string]any{"retryable": false}}
		return preparedTool{}, &failure
	}
	call := core.ToolCall{ID: invocation.ToolCallID, Name: invocation.Name, Arguments: invocation.Arguments}
	return preparedTool{
		dispatch: func(ctx context.Context) core.ToolExecution {
			return runtime.dispatchWorkspace(ctx, run, call, planned)
		},
		receiptUnknown: "A previous non-idempotent execution has no final receipt; automatic retry was stopped.", successSummary: "工具执行完成，并通过确定性校验。", failureSummary: "工具执行失败，确定性校验未通过。",
	}, nil
}

func (runtime ToolRuntime) dispatchWorkspace(ctx context.Context, run core.RunContext, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	var execution core.ToolExecution
	if call.Name == core.ToolArtifactCapture {
		execution = runtime.captureArtifact(ctx, run.ProjectID, call, planned)
	} else if call.Name == core.ToolAssetStage {
		execution = runtime.stageAsset(ctx, run, call, planned)
	} else {
		execution = runtime.workspace.Execute(ctx, run.ProjectID, call)
		if execution.Status == core.StatusSucceeded && (call.Name == core.ToolWorkspaceWriteFile || call.Name == core.ToolWorkspaceEditFile) {
			path, _ := execution.Output["path"].(string)
			if path == "" {
				path, _ = call.Arguments["path"].(string)
			}
			kind, shouldCapture := workspaceAutoCaptureKind(path)
			if !shouldCapture {
				execution.Output["artifact_capture_skipped"] = "internal_workspace_file"
			} else {
				captureCall := core.ToolCall{ID: call.ID, Name: core.ToolArtifactCapture, Arguments: map[string]any{"path": path, "kind": kind}}
				captured := runtime.captureArtifact(ctx, run.ProjectID, captureCall, core.PlanTool(captureCall))
				if captured.Status != core.StatusSucceeded {
					execution.Status, execution.ErrorCode = core.StatusFailed, captured.ErrorCode
					execution.Output["artifact_capture_error"] = captured.Output["reason"]
				} else {
					for key, value := range captured.Output {
						execution.Output[key] = value
					}
				}
			}
		}
	}
	return execution
}

func workspaceAutoCaptureKind(path string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	clean = strings.TrimPrefix(clean, "./")
	if clean == ".oktapod" || strings.HasPrefix(clean, ".oktapod/") {
		return "", false
	}
	if strings.HasPrefix(clean, ".research/") {
		return "evidence", true
	}
	return "deliverable", true
}

func (runtime ToolRuntime) stageAsset(ctx context.Context, run core.RunContext, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ErrorCode: "ASSET_STAGE_FAILED", ExitCode: -1, InputHash: planned.InputHash, Idempotent: true}
	assetID, _ := call.Arguments["asset_id"].(string)
	asset, ok := frozenAssetFromSnapshot(run.ConfigSnapshotJSON, assetID)
	if !ok {
		result.ErrorCode, result.Output = "ASSET_NOT_FROZEN", map[string]any{"asset_id": assetID, "reason": "asset is not part of the immutable Run context"}
		return result
	}
	scope := core.Scope{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID}
	live, reader, err := runtime.repository.OpenAssetContent(ctx, scope, assetID)
	if err != nil {
		result.Output = map[string]any{"asset_id": assetID, "reason": safeErrorSummary(err)}
		return result
	}
	defer reader.Close()
	if live.ProjectID != run.ProjectID || live.ContentHash != asset.ContentHash || live.SizeBytes != asset.SizeBytes || live.Name != asset.Name || live.MediaType != asset.MediaType {
		result.ErrorCode, result.Output = "ASSET_SNAPSHOT_MISMATCH", map[string]any{"asset_id": assetID, "reason": "asset metadata no longer matches the frozen Run snapshot"}
		return result
	}
	staged, err := runtime.assetStager.StageAsset(ctx, run.ProjectID, run.RunID, asset.ID, asset.Name, reader, asset.SizeBytes, asset.ContentHash)
	if err != nil {
		result.Output = map[string]any{"asset_id": assetID, "reason": safeErrorSummary(err)}
		return result
	}
	result.Status, result.ErrorCode, result.ExitCode = core.StatusSucceeded, "", 0
	result.Output = map[string]any{"asset_id": asset.ID, "name": asset.Name, "path": staged.Path, "media_type": asset.MediaType, "content_hash": staged.ContentHash, "size_bytes": staged.SizeBytes, "read_only": true}
	return result
}

func (runtime ToolRuntime) captureArtifact(ctx context.Context, projectID string, call core.ToolCall, planned core.PlannedTool) core.ToolExecution {
	result := core.ToolExecution{ToolCallID: call.ID, Status: core.StatusFailed, ErrorCode: "ARTIFACT_CAPTURE_FAILED", ExitCode: -1, InputHash: planned.InputHash, Idempotent: true}
	if runtime.artifactCapture == nil {
		result.ErrorCode, result.Output = "ARTIFACT_CAPTURE_UNAVAILABLE", map[string]any{"reason": "artifact capture is not configured"}
		return result
	}
	path, _ := call.Arguments["path"].(string)
	kind, _ := call.Arguments["kind"].(string)
	captured, err := runtime.artifactCapture.Capture(ctx, projectID, path, kind)
	if err != nil {
		result.Output = map[string]any{"path": path, "kind": kind, "reason": safeErrorSummary(err)}
		return result
	}
	result.Status, result.ErrorCode, result.ExitCode = core.StatusSucceeded, "", 0
	result.Output = map[string]any{"path": captured.Path, "kind": captured.Kind, "media_type": captured.MediaType, "content_hash": captured.ContentHash, "storage_key": captured.StorageKey, "size_bytes": captured.SizeBytes}
	return result
}

func (runtime ToolRuntime) preflightMCP(ctx context.Context, run core.RunContext, invocation FrozenToolInvocation) (preparedTool, *core.StepOutcome) {
	capabilityID, toolName, ok := parseMCPToolName(invocation.Name)
	if !ok {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "MCP_TOOL_NAME_INVALID", ResultSummary: "MCP 工具名称不符合固定 capability/tool 绑定格式。", Output: map[string]any{"retryable": false}}
		return preparedTool{}, &failure
	}
	var manifestJSON, credentialRef string
	allowed, foundCapability := false, false
	for _, capability := range mcpSnapshotsFromConfig(run.ConfigSnapshotJSON) {
		if capability.ID != capabilityID {
			continue
		}
		foundCapability, manifestJSON, credentialRef = true, capability.ManifestJSON, capability.CredentialRef
		for _, tool := range capability.Tools {
			if tool.Name == toolName {
				allowed = true
				break
			}
		}
		break
	}
	if !foundCapability || strings.TrimSpace(manifestJSON) == "" {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "MCP_CAPABILITY_NOT_SNAPSHOTTED", ResultSummary: "MCP capability 未在 Run 快照中获准，拒绝执行。", Output: map[string]any{"retryable": false, "capability_id": capabilityID, "tool_name": toolName}}
		return preparedTool{}, &failure
	}
	if !allowed {
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "MCP_TOOL_NOT_ALLOWED", ResultSummary: "MCP 工具不在创建 Run 时的冻结工具清单中，拒绝执行。", Output: map[string]any{"retryable": false, "capability_id": capabilityID, "tool_name": toolName}}
		return preparedTool{}, &failure
	}
	credential := ""
	if credentialRef != "" {
		if runtime.mcpSecrets == nil {
			failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "MCP_CREDENTIAL_MISSING", ResultSummary: "MCP 凭据存储不可用。", Output: map[string]any{"retryable": true}}
			return preparedTool{}, &failure
		}
		resolved, err := runtime.mcpSecrets.Get(ctx, credentialRef)
		if err != nil {
			failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "MCP_CREDENTIAL_MISSING", ResultSummary: "MCP 凭据不可用，请重新配置后重试。", Output: map[string]any{"retryable": true}}
			return preparedTool{}, &failure
		}
		credential = resolved
	}
	if err := mcpadapter.PreflightCall(manifestJSON, credential, toolName, invocation.Arguments); err != nil {
		if errors.Is(err, mcpadapter.ErrNeedsAuth) {
			failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "MCP_CREDENTIAL_MISSING", ResultSummary: "MCP 凭据不可用，请重新配置后重试。", Output: map[string]any{"retryable": true}}
			return preparedTool{}, &failure
		}
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "MCP_PREFLIGHT_FAILED", ResultSummary: "MCP 调用未通过本地预检，未开始执行。", Output: map[string]any{"retryable": false, "reason": err.Error()}}
		return preparedTool{}, &failure
	}
	var updater mcpadapter.CredentialUpdater
	if credentialRef != "" && runtime.mcpSecrets != nil {
		updater = func(saveContext context.Context, payload string) error {
			return runtime.mcpSecrets.Set(saveContext, credentialRef, payload)
		}
	}
	return preparedTool{
		dispatch: func(ctx context.Context) core.ToolExecution {
			return runtime.dispatchMCP(ctx, run, invocation, manifestJSON, credential, capabilityID, toolName, updater)
		},
		receiptUnknown: "MCP 副作用状态未知，已停止自动重试。", successSummary: "MCP 工具执行完成，等待确定性收据验收。", failureSummary: "MCP 工具执行失败，已记录错误收据。",
	}, nil
}

func (runtime ToolRuntime) dispatchMCP(ctx context.Context, run core.RunContext, invocation FrozenToolInvocation, manifestJSON, credential, capabilityID, toolName string, updater mcpadapter.CredentialUpdater) core.ToolExecution {
	callContext, cancel := ctx, func() {}
	if runtime.mcpCallTimeout > 0 {
		callContext, cancel = context.WithTimeout(ctx, runtime.mcpCallTimeout)
	}
	defer cancel()
	var result mcpadapter.CallResult
	var callErr error
	if runtime.mcpSessions != nil {
		sessionKey := strings.Join([]string{run.OrganizationID, run.ProjectID, run.RunID, capabilityID}, "\x00")
		result, callErr = runtime.mcpSessions.Call(callContext, sessionKey, manifestJSON, credential, toolName, invocation.Arguments, updater)
	} else {
		result, callErr = mcpadapter.CallWithCredentialUpdater(callContext, manifestJSON, credential, toolName, invocation.Arguments, updater)
	}
	output := map[string]any{"capability_id": capabilityID, "tool_name": toolName, "is_error": result.IsError, "_envelope": map[string]any{"trust": core.TrustExternalUntrusted, "source": "mcp:" + capabilityID, "capability": toolName}}
	content := make([]any, 0, len(result.Content))
	for _, raw := range result.Content {
		var item any
		if json.Unmarshal(raw, &item) == nil {
			content = append(content, item)
		}
	}
	output["content"] = content
	if len(result.StructuredResult) > 0 {
		var structured any
		if json.Unmarshal(result.StructuredResult, &structured) == nil {
			output["structured_content"] = structured
		}
	}
	execution := core.ToolExecution{Status: core.StatusSucceeded, ExitCode: 0, Output: output}
	if callErr != nil {
		execution.Status = core.StatusFailed
		execution.ErrorCode, output["reason"], output["retryable"] = classifyMCPCallError(callErr, callContext.Err())
	} else if result.IsError {
		execution.Status, execution.ErrorCode = core.StatusFailed, "MCP_TOOL_ERROR"
	}
	return execution
}

func classifyMCPCallError(callErr, contextErr error) (string, string, bool) {
	if errors.Is(callErr, context.DeadlineExceeded) || errors.Is(contextErr, context.DeadlineExceeded) {
		return "MCP_CALL_TIMEOUT", "MCP tool exceeded the configured execution deadline", true
	}
	if errors.Is(callErr, context.Canceled) || errors.Is(contextErr, context.Canceled) {
		return "TOOL_CANCELED", "MCP tool was canceled after execution began", false
	}
	return "MCP_CALL_FAILED", callErr.Error(), false
}

func (invocation FrozenToolInvocation) String() string {
	return fmt.Sprintf("%s/%s", invocation.Name, invocation.ToolCallID)
}
