package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	mcpadapter "github.com/oktapod/oktapod/Src/internal/adapters/mcp"
	"github.com/oktapod/oktapod/Src/internal/agentloop"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	durableharness "github.com/oktapod/oktapod/Src/internal/harness"
)

// WorkerRepository is the worker's explicit persistence port. Keeping this
// list local prevents the runtime loop from depending on unrelated HTTP,
// project-management, provider-configuration, or authentication operations.
type WorkerRepository interface {
	ExpireApprovals(ctx context.Context, now time.Time) (int, error)
	ClaimNextStep(ctx context.Context, workerID string, now, leaseUntil time.Time) (core.ClaimedStep, error)
	HeartbeatStep(ctx context.Context, step core.ClaimedStep, now, leaseUntil time.Time) (bool, error)
	GetRunContext(ctx context.Context, runID string) (core.RunContext, error)
	MarkStepStarted(ctx context.Context, step core.ClaimedStep, run core.RunContext, correlationID string) error
	CommitCheckpoint(ctx context.Context, step core.ClaimedStep, run core.RunContext, kind string, state map[string]any, correlationID string) error
	BeginToolExecution(ctx context.Context, step core.ClaimedStep, run core.RunContext, call core.ToolCall, inputHash string, idempotent bool, correlationID string) (string, error)
	FinishStep(ctx context.Context, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, correlationID string) error
	ApplyConversationSteering(ctx context.Context, scope core.Scope, sourceStep core.ClaimedStep, run core.RunContext, correlationID string) (core.ConversationQueueItem, string, error)
	GetTask(ctx context.Context, scope core.Scope, taskID string) (core.Task, error)
	ClaimNextFollowUp(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationQueueItem, error)
	ClaimNextIdleFollowUp(ctx context.Context, retryBefore time.Time) (core.ConversationQueueItem, error)
	MarkConversationQueueItemConsumed(ctx context.Context, scope core.Scope, itemID string) (core.ConversationQueueItem, error)
	RequeueConversationQueueItem(ctx context.Context, scope core.Scope, itemID string) error
	ListMemories(ctx context.Context, scope core.Scope, projectID, query string, limit int) ([]core.Memory, error)
	OpenAssetContent(ctx context.Context, scope core.Scope, assetID string) (core.Asset, io.ReadCloser, error)
}

// QueuedTurnStarter starts a queued conversation follow-up through the
// application service so the new task freezes the latest project configuration.
type QueuedTurnStarter func(ctx context.Context, scope core.Scope, item core.ConversationQueueItem) (core.Task, error)

type Worker struct {
	id                string
	repository        WorkerRepository
	model             ModelGateway
	agentLoop         agentloop.AgentLoop
	harness           *durableharness.Durable
	logger            *slog.Logger
	lease             time.Duration
	pollInterval      time.Duration
	workspace         WorkspaceExecutor
	artifactCapture   application.ArtifactCapture
	assetStager       application.AssetStager
	documentRuntime   application.DocumentExecutor
	webSearch         WebSearchExecutor
	webAccess         WebAccess
	mcpSecrets        application.ProviderSecretStore
	mcpSessions       *mcpadapter.SessionBroker
	mcpCallTimeout    time.Duration
	modelCallTimeout  time.Duration
	modelTurnTimeouts ModelTurnTimeouts
	queuedTurnStarter QueuedTurnStarter
	reviewerSettings  func(context.Context, string) (core.AIGovernanceSettings, error)
	liveTurns         core.LiveTurnPublisher
}

func (w *Worker) WithWorkspaceExecutor(executor WorkspaceExecutor) *Worker {
	w.workspace = executor
	// Compose the capture port from independently scoped Workspace and Blob
	// interfaces when the bootstrap repository exposes both. Production may
	// still inject an explicit capture service; this path keeps alternate
	// bootstraps from turning successful writes into unregistered outputs.
	if w.artifactCapture == nil {
		workspaceReader, workspaceOK := executor.(application.WorkspaceCaptureReader)
		blobStore, blobOK := w.repository.(application.BlobStore)
		if workspaceOK && blobOK {
			if capture, err := application.NewArtifactCaptureService(workspaceReader, blobStore); err == nil {
				w.artifactCapture = capture
			}
		}
	}
	return w
}

func (w *Worker) WithArtifactCapture(capture application.ArtifactCapture) *Worker {
	w.artifactCapture = capture
	return w
}

func (w *Worker) WithAssetStager(stager application.AssetStager) *Worker {
	w.assetStager = stager
	return w
}

func (w *Worker) WithDocumentRuntime(runtime application.DocumentExecutor) *Worker {
	w.documentRuntime = runtime
	return w
}

func (w *Worker) WithWebSearchExecutor(executor WebSearchExecutor) *Worker {
	w.webSearch = executor
	return w
}

func (w *Worker) WithWebAccess(access WebAccess) *Worker {
	w.webAccess = access
	return w
}

func (w *Worker) WithMCPSecretStore(secrets application.ProviderSecretStore) *Worker {
	w.mcpSecrets = secrets
	return w
}

// WithMCPSessionBroker enables Run-scoped protocol continuity for stateful MCP
// servers while keeping approval and receipts scoped to each individual call.
func (w *Worker) WithMCPSessionBroker(broker *mcpadapter.SessionBroker) *Worker {
	w.mcpSessions = broker
	return w
}

// WithMCPCallTimeout bounds one external MCP invocation. A remote server may
// keep its connection alive without ever producing a result, so the step lease
// heartbeat alone is not a completion boundary. Tests can shorten this value;
// production keeps enough time for research-style tools to finish.
func (w *Worker) WithMCPCallTimeout(timeout time.Duration) *Worker {
	w.mcpCallTimeout = timeout
	return w
}

// WithModelCallTimeout bounds one model turn even when the provider keeps an
// SSE connection active with reasoning or keep-alive chunks. Those bytes prove
// that the network is alive, but they do not justify making a user wait without
// a completed answer or executable tool call.
func (w *Worker) WithModelCallTimeout(timeout time.Duration) *Worker {
	w.modelCallTimeout = timeout
	w.modelTurnTimeouts = uniformModelTurnTimeouts(timeout)
	return w
}

// WithModelTurnTimeouts configures the hard completion deadline by effective
// thinking level. HTTP response and stream-idle guards remain adapter-owned.
func (w *Worker) WithModelTurnTimeouts(timeouts ModelTurnTimeouts) *Worker {
	w.modelTurnTimeouts = timeouts
	w.modelCallTimeout = timeouts.For("auto")
	return w
}

func (w *Worker) WithQueuedTurnStarter(starter QueuedTurnStarter) *Worker {
	w.queuedTurnStarter = starter
	return w
}

func (w *Worker) WithLiveTurnPublisher(publisher core.LiveTurnPublisher) *Worker {
	w.liveTurns = publisher
	return w
}

// WithLease overrides the step lease. The lease also bounds the crash-recovery
// and retry window for claimed queue items; tests shorten it to exercise the
// idle-drain backoff without waiting.
func (w *Worker) WithLease(lease time.Duration) *Worker {
	w.lease = lease
	return w
}

func NewWorker(id string, repository WorkerRepository, model ModelGateway, logger *slog.Logger) *Worker {
	if err := validateBuiltinToolOwners(newToolPreparerRegistry(ToolRuntime{})); err != nil {
		panic("invalid runtime tool ownership: " + err.Error())
	}
	worker := &Worker{
		id:                id,
		repository:        repository,
		model:             model,
		agentLoop:         agentloop.New(model),
		harness:           durableharness.New(repository),
		logger:            logger,
		lease:             90 * time.Second,
		pollInterval:      250 * time.Millisecond,
		mcpCallTimeout:    10 * time.Minute,
		modelCallTimeout:  2 * time.Minute,
		modelTurnTimeouts: DefaultModelTurnTimeouts(),
	}
	if settingsRepository, ok := repository.(interface {
		GetAIGovernanceSettings(context.Context, string) (core.AIGovernanceSettings, error)
	}); ok {
		worker.reviewerSettings = settingsRepository.GetAIGovernanceSettings
	}
	return worker
}

func (w *Worker) completeModel(ctx context.Context, request ModelRequest) (core.ModelResult, error) {
	return w.modelInvoker().Complete(ctx, request)
}

func (w *Worker) completeModelLive(ctx context.Context, request ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	return w.modelInvoker().CompleteLive(ctx, request, onDelta)
}

func (w *Worker) modelInvoker() ModelInvoker {
	return NewModelInvokerWithTimeouts(w.model, w.agentLoop, w.modelTurnTimeouts)
}

func (w *Worker) durableHarness() *durableharness.Durable {
	if w.harness == nil {
		w.harness = durableharness.New(w.repository)
	}
	return w.harness
}

// retryOnBusy remains as a package-local compatibility seam for focused
// runtime tests; the retry policy itself is owned by Durable Harness.
func retryOnBusy(ctx context.Context, fn func() error) error {
	return durableharness.RetryBusy(ctx, fn)
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	consecutiveErrors := 0
	for {
		worked, err := w.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			w.logger.ErrorContext(ctx, "worker iteration failed", "worker_id", w.id, "error", err)
			consecutiveErrors++
			delay := time.Duration(1<<minInt(consecutiveErrors, 5)) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		} else {
			consecutiveErrors = 0
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	now := time.Now().UTC()
	if expired, err := w.repository.ExpireApprovals(ctx, now); err != nil {
		return false, fmt.Errorf("expire approvals: %w", err)
	} else if expired > 0 {
		return true, nil
	}
	execution, err := w.durableHarness().Claim(ctx, w.id, now, w.lease, newCorrelationID())
	if errors.Is(err, core.ErrNoWork) {
		return w.drainIdleFollowUp(ctx)
	}
	if err != nil {
		return false, fmt.Errorf("claim step: %w", err)
	}
	step, run, correlationID := execution.Step, execution.Run, execution.CorrelationID

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go w.heartbeat(workCtx, step, heartbeatDone, cancel)
	if err := w.durableHarness().CheckpointBeforeExecution(workCtx, execution); err != nil {
		cancel()
		<-heartbeatDone
		return true, fmt.Errorf("commit pre-execution checkpoint: %w", err)
	}
	if reserver, ok := w.repository.(ModelBudgetReserver); ok && isModelStep(step.StepType) {
		estimate := int64(modelMaxOutputTokens(step.InputJSON))
		if run.Mode == core.ModeAgentTeam {
			estimate = 1536
		}
		if err := reserver.ReserveModelBudget(workCtx, run, step, estimate, correlationID); err != nil {
			outcome := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "BUDGET_RESERVATION_EXCEEDED", ResultSummary: "Run 的并发模型预算已无可用额度；请缩小任务或批准预算扩展。", Output: map[string]any{"retryable": false, "estimated_tokens": estimate}}
			if finishErr := w.durableHarness().Finish(ctx, execution, outcome); finishErr != nil {
				cancel()
				<-heartbeatDone
				return true, fmt.Errorf("finish budget-blocked step: %w", finishErr)
			}
			// Keep renewing the lease until the terminal state is durably
			// committed. Stopping the heartbeat before FinishStep allows a
			// second worker to recover and repeat the same model step while
			// the first worker is still closing its transaction.
			cancel()
			<-heartbeatDone
			return true, nil
		}
	}

	outcome := w.execute(workCtx, step, run)
	if outcome.Status != core.StatusSucceeded {
		w.logger.WarnContext(ctx, "step execution did not succeed", "step_id", step.ID, "step_type", step.StepType, "status", outcome.Status, "error_code", outcome.ErrorCode)
	}
	if err := w.durableHarness().Finish(ctx, execution, outcome); err != nil {
		cancel()
		<-heartbeatDone
		return true, fmt.Errorf("finish step: %w", err)
	}
	// FinishStep is part of the owned attempt. Keep the lease alive through
	// its commit so another worker cannot fence and repeat the same step.
	cancel()
	<-heartbeatDone
	scope := core.Scope{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID}
	// Steering takes effect only after the atomic step and its checkpoint are
	// durably committed. The replan step, superseded-step cancellation, task
	// reactivation and queue consumption commit as one transaction.
	steeredItem, replanStepID, steerErr := w.repository.ApplyConversationSteering(ctx, scope, step, run, newCorrelationID())
	switch {
	case steerErr == nil:
		w.logger.InfoContext(ctx, "conversation steering applied", "steering_id", steeredItem.ID, "task_id", run.TaskID, "replan_step_id", replanStepID)
		return true, nil
	case !errors.Is(steerErr, core.ErrNoWork):
		w.logger.ErrorContext(ctx, "apply conversation steering failed", "task_id", run.TaskID, "error", steerErr)
	}

	// After step completion, check if the task reached a terminal state and, if
	// so, start the next queued follow-up through the application service so the
	// new task freezes the latest project configuration snapshot.
	taskAfter, taskErr := w.repository.GetTask(ctx, scope, run.TaskID)
	if taskErr == nil && (taskAfter.Status == core.StatusSucceeded || taskAfter.Status == core.StatusFailed || taskAfter.Status == core.StatusCanceled) {
		if w.assetStager != nil {
			if cleanupErr := w.assetStager.CleanupRunInputs(ctx, run.ProjectID, run.RunID); cleanupErr != nil {
				w.logger.WarnContext(ctx, "cleanup staged Run inputs failed", "run_id", run.RunID, "error", cleanupErr)
			}
		}
		followUp, followUpErr := w.repository.ClaimNextFollowUp(ctx, scope, run.ConversationID)
		switch {
		case followUpErr == nil:
			w.startQueuedTurn(ctx, scope, followUp)
		case !errors.Is(followUpErr, core.ErrNoWork):
			w.logger.ErrorContext(ctx, "claim follow-up failed", "conversation_id", run.ConversationID, "error", followUpErr)
		}
	}
	return true, nil
}

// startQueuedTurn starts one claimed queue item through the application
// service. A failed start restores the item to pending so the message is never
// lost; only a successful start consumes it.
func (w *Worker) startQueuedTurn(ctx context.Context, scope core.Scope, item core.ConversationQueueItem) bool {
	if w.queuedTurnStarter == nil {
		_ = w.repository.RequeueConversationQueueItem(ctx, scope, item.ID)
		return false
	}
	if _, startErr := w.queuedTurnStarter(ctx, scope, item); startErr != nil {
		w.logger.ErrorContext(ctx, "start queued conversation turn failed", "queue_item_id", item.ID, "error", startErr)
		_ = w.repository.RequeueConversationQueueItem(ctx, scope, item.ID)
		return false
	}
	if _, consumeErr := w.repository.MarkConversationQueueItemConsumed(ctx, scope, item.ID); consumeErr != nil {
		w.logger.ErrorContext(ctx, "consume queue item failed", "queue_item_id", item.ID, "error", consumeErr)
	}
	return true
}

// drainIdleFollowUp retries queued turns while the worker has no step to
// execute. Without it a follow-up whose start failed after the active task
// finished would stay pending until the user sent another message.
func (w *Worker) drainIdleFollowUp(ctx context.Context) (bool, error) {
	if w.queuedTurnStarter == nil {
		return false, nil
	}
	item, err := w.repository.ClaimNextIdleFollowUp(ctx, time.Now().UTC().Add(-w.lease))
	if errors.Is(err, core.ErrNoWork) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim idle follow-up: %w", err)
	}
	scope := core.Scope{OrganizationID: item.OrganizationID}
	return w.startQueuedTurn(ctx, scope, item), nil
}

func isModelStep(stepType string) bool {
	return !isWorkspaceTool(stepType) && !isDocumentTool(stepType) && stepType != core.ToolWebSearch && stepType != core.ToolSkillInvoke && !isMCPTool(stepType)
}

func isDocumentTool(stepType string) bool {
	switch stepType {
	case core.ToolDocumentOpen, core.ToolDocumentRead, core.ToolDocumentCreate, core.ToolDocumentApply, core.ToolDocumentRender, core.ToolDocumentExport:
		return true
	default:
		return false
	}
}

func isWorkspaceTool(stepType string) bool {
	switch core.ResolveToolName(stepType) {
	case core.ToolWorkspaceReadFile, core.ToolWorkspaceListFiles, core.ToolWorkspaceFindFiles,
		core.ToolWorkspaceGrep, core.ToolWorkspaceEditFile, core.ToolWorkspaceWriteFile,
		core.ToolWorkspaceRunCommand, core.ToolWorkspaceShell, core.ToolWorkspaceExtractArchive, core.ToolArtifactCapture, core.ToolAssetStage,
		core.ToolGitStatus, core.ToolGitDiff, core.ToolGitLog, core.ToolGitConflicts,
		core.ToolGitBranch, core.ToolGitCommit, core.ToolGitCommitPaths, core.ToolGitRestore, core.ToolGitRestoreHunk:
		return true
	default:
		return false
	}
}

func (w *Worker) execute(ctx context.Context, step core.ClaimedStep, run core.RunContext) core.StepOutcome {
	return w.stepDispatcher().Execute(ctx, step, run)
}

func normalizeLengthTruncatedModelResult(result core.ModelResult) (core.ModelResult, []core.ToolCall) {
	if result.StopReason != core.StopReasonLength {
		return result, nil
	}
	if len(result.ToolCalls) == 0 {
		message := "回答被截断。可继续追问，或启用显式自动续写策略。"
		if strings.TrimSpace(result.Content) == "" {
			result.Content = message
		} else {
			result.Content = strings.TrimSpace(result.Content) + "\n\n" + message
		}
		return result, nil
	}
	truncated := append([]core.ToolCall(nil), result.ToolCalls...)
	result.ToolCalls = nil
	result.ContentBlocks = removeToolCallBlocks(result.ContentBlocks)
	message := fmt.Sprintf("模型输出因长度限制被截断，%d 个工具调用均未执行；系统已记录错误结果，并要求模型重新发出完整调用。", len(truncated))
	if strings.TrimSpace(result.Content) == "" {
		result.Content = message
	} else {
		result.Content = strings.TrimSpace(result.Content) + "\n\n" + message
	}
	return result, truncated
}

func removeToolCallBlocks(blocks []core.ContentBlock) []core.ContentBlock {
	filtered := make([]core.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != core.ContentBlockToolCall {
			filtered = append(filtered, block)
		}
	}
	return filtered
}

func lengthTruncationHint(inputJSON string) string {
	var input struct {
		LengthTruncatedToolCalls bool `json:"length_truncated_tool_calls"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil || !input.LengthTruncatedToolCalls {
		return ""
	}
	return "上一次模型响应因长度限制包含不完整工具调用，系统没有执行任何调用，并已为每个调用写入错误 ToolResult。重新评估目标，只重新发出仍然需要的完整工具调用；不得假设被截断的调用已经执行。"
}

func modelFailureSummary(code string, err error) string {
	if failure := modelFailureDetails(err); failure != nil {
		switch failure["kind"] {
		case "response_header_timeout":
			return "模型首包响应超时；系统未在 60 秒内收到响应头，将从当前检查点自动重试。"
		case "stream_idle_timeout":
			return "模型流式响应停滞；连续 60 秒没有收到新数据，将从当前检查点自动重试。"
		case "model_turn_timeout":
			return "模型本轮响应超过 120 秒；即使连接仍有数据，系统也已停止等待并从当前检查点重试。"
		case "dns_error":
			return "模型服务域名解析失败；请检查 DNS 或网络配置，系统将从当前检查点自动重试。"
		case "connect_error":
			return "无法建立到模型服务的网络连接；系统将从当前检查点自动重试。"
		case "tls_error":
			return "模型服务 TLS 握手失败；请检查证书、代理或系统时间，系统将从当前检查点自动重试。"
		case "stream_interrupted":
			return "模型流式连接在响应完成前中断；系统将从当前检查点自动重试。"
		}
	}
	switch code {
	case "PROVIDER_TOOL_ARGUMENTS_INVALID":
		return "模型返回的工具参数不是有效 JSON；系统未执行该动作，并将进行兼容性重试。"
	case "PROVIDER_TOOL_ARGUMENTS_TRUNCATED":
		return "模型工具参数输出被截断；系统未执行不完整动作，并将要求模型缩短参数后重试。"
	case "PROVIDER_UNAVAILABLE":
		if isModelRequestTimeout(err) {
			return "模型响应超时；系统将从当前检查点自动重试。"
		}
		return "模型连接暂时中断；系统将从当前检查点自动重试。"
	case "PROVIDER_RATE_LIMIT":
		return "模型服务暂时限流；系统将保留当前进度并自动重试。"
	case "PROVIDER_PROTOCOL_ERROR", "PROVIDER_EMPTY_RESPONSE", "PROVIDER_REASONING_TRUNCATED":
		return "模型响应不完整；系统将保留当前进度并重新请求。"
	default:
		return safeErrorSummary(err)
	}
}

type modelFailureDiagnostic interface {
	ModelFailureKind() string
	ModelFailurePhase() string
	ModelFailureTimeout() bool
}

func modelFailureDetails(err error) map[string]any {
	if err == nil {
		return nil
	}
	var diagnostic modelFailureDiagnostic
	if !errors.As(err, &diagnostic) {
		return nil
	}
	return map[string]any{
		"kind":      diagnostic.ModelFailureKind(),
		"phase":     diagnostic.ModelFailurePhase(),
		"timed_out": diagnostic.ModelFailureTimeout(),
	}
}

func isModelRequestTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var diagnostic modelFailureDiagnostic
	if errors.As(err, &diagnostic) {
		return diagnostic.ModelFailureTimeout()
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "client.timeout exceeded") ||
		strings.Contains(message, "awaiting headers") ||
		strings.Contains(message, "i/o timeout")
}

func isRetryableModelInteractionError(code string) bool {
	switch code {
	case "PROVIDER_UNAVAILABLE", "PROVIDER_RATE_LIMIT", "PROVIDER_PROTOCOL_ERROR", "PROVIDER_EMPTY_RESPONSE", "PROVIDER_REASONING_TRUNCATED", "PROVIDER_TOOL_ARGUMENTS_INVALID", "PROVIDER_TOOL_ARGUMENTS_TRUNCATED", "TOOL_BUDGET_EXCEEDED":
		return true
	default:
		return false
	}
}

func modelCompatibilityRetryHint(inputJSON string) string {
	var input struct {
		RetryCount       int    `json:"model_retry_count"`
		LegacyRetryCount int    `json:"model_compat_retry_count"`
		RetryReason      string `json:"model_retry_reason"`
		LegacyReason     string `json:"model_compat_retry_reason"`
		PolicyDenials    []struct {
			ToolName string `json:"tool_name"`
			Reason   string `json:"reason"`
		} `json:"model_policy_denials"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		return ""
	}
	if input.RetryCount < 1 {
		input.RetryCount = input.LegacyRetryCount
	}
	if input.RetryReason == "" {
		input.RetryReason = input.LegacyReason
	}
	if input.RetryCount < 1 {
		return ""
	}
	hint := fmt.Sprintf("系统模型交互重试 %d/%d：上一次请求%s。请从当前检查点继续，不要重复已经成功的步骤。", input.RetryCount, core.ModelRetryLimit(input.RetryReason), modelRetryReasonLabel(input.RetryReason))
	if input.RetryReason == "PROVIDER_TOOL_ARGUMENTS_INVALID" || input.RetryReason == "PROVIDER_TOOL_ARGUMENTS_TRUNCATED" {
		hint += " 每个 function.arguments 必须是完整 JSON 对象，使用双引号并正确转义换行和反斜杠，不要使用 Markdown 代码块；参数过长时拆成更小的工具调用。"
	}
	if input.RetryReason == "PROVIDER_REASONING_TRUNCATED" {
		hint += " 上一次只产生了内部推理并耗尽输出预算，没有形成任何动作。不要重新做全局分析；若执行轨迹已经包含用户要求的交付物与验证，禁止为了总结重复读写或创建额外文件，直接返回不超过 800 字的最终交付摘要。否则立即发出一个最小且完整的工具调用；长文任务一次只写一个不超过 3000 字符的章节，后续轮次再继续。"
	}
	if input.RetryReason == "TOOL_BUDGET_EXCEEDED" {
		hint += " 用户声明的联网调用上限已经用完，刚才的超限调用没有执行。禁止继续调用 web_search、web_fetch 或任何 search、research、crawl、extract、map、browse、discover 类 MCP 工具；只使用已有工具结果完成剩余写作与工作区核验。"
	}
	if input.RetryReason == "RUN_PLAN_STATUS_REGRESSION" {
		hint += " 上一次 update_plan 试图把已完成项改回未完成，系统已拒绝并保留原计划。先读取当前持久化计划，再提交同一完整清单；所有 completed 项必须继续保持 completed，只推进当前项和后续项。"
	}
	if isRunPlanRepairReason(input.RetryReason) {
		if input.RetryReason == "TRANSITION_RUN_PLAN_REPLACEMENT_NOT_ALLOWED" || input.RetryReason == "TRANSITION_RUN_PLAN_NO_UNFINISHED_ITEMS" {
			hint += " 当前已有稳定的语义计划，系统已保留完成项并拒绝重建。禁止再次调用 replace_plan；继续当前未完成项，必要时用 advance_plan 推进。若交付物与验收已经完成，直接返回最终答复，Runtime 会通过完成门禁收敛计划终态。"
		} else if input.RetryReason == "TRANSITION_RUN_PLAN_DUPLICATE_ITEM" {
			hint += " replace_plan 的 items 只能包含互不重复的未完成目标，不得重新提交 Runtime 已保留的完成项。"
		} else if isTransitionPlanRepairReason(input.RetryReason) {
			hint += " 请在同一响应中修正计划控制并调用下一批必要业务工具：初次或重规划使用 replace_plan，推进当前语义项使用 advance_plan；每个响应最多一个计划控制，不得单独发送计划控制。最后一项完成后直接返回最终答复，不要调用 advance_plan。"
		} else {
			hint += " 请重新单独调用一次 update_plan：提交 1 到 12 个目标特定的完整语义步骤，每项包含具体 title、description 和合法 status；不要使用“制定执行计划”等占位文案，不要同时调用其他工具，也不要在尚无执行证据时把所有步骤标为 completed。"
		}
	}
	if input.RetryReason == "TOOL_POLICY_DENIED" || strings.HasPrefix(input.RetryReason, "SKILL_") || input.RetryReason == "UPDATE_PLAN_MUST_BE_EXCLUSIVE" {
		hint += " 这是不可审批的确定性策略限制，不代表用户拒绝。请换用允许的工具或参数继续执行；不要再次调用相同的被拒动作，也不要把任务判失败。"
		if len(input.PolicyDenials) > 0 {
			details := make([]string, 0, len(input.PolicyDenials))
			for _, denial := range input.PolicyDenials {
				details = append(details, fmt.Sprintf("%s（%s）", strings.TrimSpace(denial.ToolName), strings.TrimSpace(denial.Reason)))
			}
			hint += " 被拒动作：" + strings.Join(details, "；") + "。"
		}
	}
	if input.RetryCount >= 2 && input.RetryReason == "PROVIDER_TOOL_ARGUMENTS_TRUNCATED" {
		hint += " 本次禁止在单个工具参数中生成整篇长文；先写入较短的结构或分段文件，再用后续工具调用逐段补全。"
	}
	return hint
}

func modelMaxOutputTokens(inputJSON string) int {
	var input struct {
		RetryCount       int `json:"model_retry_count"`
		LegacyRetryCount int `json:"model_compat_retry_count"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		return core.DefaultModelOutputTokens
	}
	if input.RetryCount < 1 {
		input.RetryCount = input.LegacyRetryCount
	}
	if input.RetryCount < 1 {
		return core.DefaultModelOutputTokens
	}
	if input.RetryCount == 1 {
		return core.DefaultModelOutputTokens * 2
	}
	return core.DefaultModelOutputTokens * 4
}

func modelRetryReasonLabel(reason string) string {
	switch reason {
	case "PROVIDER_TOOL_ARGUMENTS_TRUNCATED":
		return "的工具参数被截断"
	case "PROVIDER_TOOL_ARGUMENTS_INVALID":
		return "的工具参数不是有效 JSON"
	case "PROVIDER_RATE_LIMIT":
		return "被模型服务限流"
	case "PROVIDER_UNAVAILABLE":
		return "未能连接模型服务"
	case "PROVIDER_EMPTY_RESPONSE":
		return "没有返回可用内容"
	case "PROVIDER_REASONING_TRUNCATED":
		return "只产生内部推理并在形成动作前达到输出上限"
	case "TOOL_BUDGET_EXCEEDED":
		return "规划了超过用户明确上限的搜索调用"
	case "RUN_PLAN_STATUS_REGRESSION":
		return "提交了会回退已完成进度的执行计划"
	case "RUN_PLAN_INVALID":
		return "提交了结构不完整的执行计划"
	case "TRANSITION_RUN_PLAN_INVALID":
		return "提交了结构不完整的新协议执行计划"
	case "RUN_PLAN_PLACEHOLDER_DENIED":
		return "提交了占位执行计划"
	case "TRANSITION_RUN_PLAN_PLACEHOLDER_DENIED":
		return "提交了占位的新协议执行计划"
	case "RUN_PLAN_PREMATURE_COMPLETION":
		return "在没有执行证据时提前完成了计划"
	case "RUN_PLAN_REQUIRED":
		return "在语义执行计划建立前尝试执行工具"
	case "TRANSITION_RUN_PLAN_REQUIRED":
		return "在语义执行计划建立前尝试推进"
	case "TRANSITION_RUN_PLAN_DUPLICATE_ITEM":
		return "提交了重复的语义计划项"
	case "TRANSITION_RUN_PLAN_NO_UNFINISHED_ITEMS":
		return "在原计划已无待替换目标时尝试重建计划"
	case "TRANSITION_RUN_PLAN_REPLACEMENT_NOT_ALLOWED":
		return "在普通执行轮次中尝试改写稳定计划"
	case "UPDATE_PLAN_MUST_BE_EXCLUSIVE":
		return "把计划更新与其他工具混在同一响应中"
	case "PLAN_CONTROL_MULTIPLE":
		return "在一个响应中提交了多个计划控制"
	case "PLAN_CONTROL_REQUIRES_BUSINESS_TOOL":
		return "单独提交了计划控制而没有业务工具"
	case "LEGACY_PLAN_CONTROL_DENIED":
		return "在新协议 Run 中使用了旧计划控制"
	case "RUN_PLAN_ITEM_HAS_UNFINISHED_WORK":
		return "在当前计划项仍有未完成动作时尝试推进"
	case "RUN_PLAN_ALREADY_AT_FINAL_ITEM":
		return "在最后计划项应直接交付时尝试继续推进"
	case "TOOL_POLICY_DENIED":
		return "规划了不符合确定性策略的工具或参数"
	case "SKILL_INVOKE_MUST_BE_EXCLUSIVE", "SKILL_RECURSIVE_INVOCATION_DENIED":
		return "规划了不符合 Skill 调用约束的动作"
	default:
		return "返回了无法解析的响应"
	}
}

func searchCallBudgetExceeded(history []map[string]any, proposed []core.ToolCall, limit int) bool {
	return searchCallBudgetExceededWithPrior(history, proposed, limit, 0)
}

func searchCallBudgetExceededWithPrior(history []map[string]any, proposed []core.ToolCall, limit, prior int) bool {
	used := prior
	for _, item := range history {
		kind, _ := item["kind"].(string)
		status, _ := item["status"].(string)
		name, _ := item["tool_name"].(string)
		if kind == "tool_result" && status == core.StatusSucceeded && isSearchEvidenceTool(name) {
			used++
		}
	}
	for _, call := range proposed {
		if isSearchEvidenceTool(call.Name) {
			used++
		}
	}
	return used > limit
}

func isSearchEvidenceTool(name string) bool {
	return core.IsSearchEvidenceTool(name)
}

func succeededSearchCallsInHistory(history []map[string]any) int {
	used := 0
	for _, item := range history {
		kind, _ := item["kind"].(string)
		status, _ := item["status"].(string)
		name, _ := item["tool_name"].(string)
		if kind == "tool_result" && status == core.StatusSucceeded && isSearchEvidenceTool(name) {
			used++
		}
	}
	return used
}

func excludeSearchToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	filtered := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if !isSearchEvidenceTool(definition.Name) {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func excludeSearchToolNames(names []string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if !isSearchEvidenceTool(name) {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func searchCallsUsedFromSnapshot(snapshot string) int {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return 0
	}
	value, ok := config["continuation_search_calls_used"].(float64)
	if !ok || value < 0 || value > 100_000 {
		return 0
	}
	return int(value)
}

func isRunPlanRepairReason(reason string) bool {
	switch reason {
	case "RUN_PLAN_INVALID", "RUN_PLAN_PLACEHOLDER_DENIED", "RUN_PLAN_PREMATURE_COMPLETION", "RUN_PLAN_REQUIRED", "UPDATE_PLAN_MUST_BE_EXCLUSIVE",
		"PLAN_CONTROL_MULTIPLE", "PLAN_CONTROL_REQUIRES_BUSINESS_TOOL", "LEGACY_PLAN_CONTROL_DENIED", "RUN_PLAN_ITEM_HAS_UNFINISHED_WORK", "RUN_PLAN_ALREADY_AT_FINAL_ITEM",
		"TRANSITION_RUN_PLAN_INVALID", "TRANSITION_RUN_PLAN_PLACEHOLDER_DENIED", "TRANSITION_RUN_PLAN_REQUIRED", "TRANSITION_RUN_PLAN_DUPLICATE_ITEM", "TRANSITION_RUN_PLAN_NO_UNFINISHED_ITEMS", "TRANSITION_RUN_PLAN_REPLACEMENT_NOT_ALLOWED":
		return true
	default:
		return false
	}
}

func isTransitionPlanRepairReason(reason string) bool {
	switch reason {
	case "PLAN_CONTROL_MULTIPLE", "PLAN_CONTROL_REQUIRES_BUSINESS_TOOL", "LEGACY_PLAN_CONTROL_DENIED", "RUN_PLAN_ITEM_HAS_UNFINISHED_WORK", "RUN_PLAN_ALREADY_AT_FINAL_ITEM",
		"TRANSITION_RUN_PLAN_INVALID", "TRANSITION_RUN_PLAN_PLACEHOLDER_DENIED", "TRANSITION_RUN_PLAN_REQUIRED", "TRANSITION_RUN_PLAN_DUPLICATE_ITEM", "TRANSITION_RUN_PLAN_NO_UNFINISHED_ITEMS", "TRANSITION_RUN_PLAN_REPLACEMENT_NOT_ALLOWED":
		return true
	default:
		return false
	}
}

func toolRecoveryHint(inputJSON string) string {
	var input struct {
		RecoveryCount int    `json:"tool_recovery_count"`
		Reason        string `json:"tool_recovery_reason"`
		ToolName      string `json:"tool_recovery_name"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil || input.RecoveryCount < 1 {
		return ""
	}
	return fmt.Sprintf("工具失败恢复 %d/5：%s 执行失败（%s）。失败收据已经包含在执行轨迹中。你可以：1) 用 workspace_read_file 读取相关脚本或配置文件，定位错误根因；2) 用 workspace_edit_file 修复脚本中的语法错误、路径错误或依赖缺失；3) 用 workspace_write_file 创建缺失的依赖文件或配置；4) 用 workspace_run_command 安装缺失依赖或运行验证；5) 换用不同参数或替代工具重新尝试。不要原样重试同一个失败动作；先诊断根因再修复。不要重复已经成功的步骤。", input.RecoveryCount, strings.TrimSpace(input.ToolName), strings.TrimSpace(input.Reason))
}

func modelStage(inputJSON string) string {
	var input struct {
		Stage string `json:"stage"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		return ""
	}
	return strings.TrimSpace(input.Stage)
}

func completionRecoveryHint(inputJSON string) string {
	var input struct {
		Reason             string `json:"completion_recovery_reason"`
		DeliverableFormat  string `json:"completion_deliverable_format"`
		RequiredCharacters int64  `json:"completion_required_characters"`
		ActualCharacters   int64  `json:"completion_actual_characters"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil || strings.TrimSpace(input.Reason) == "" {
		return ""
	}
	switch input.Reason {
	case "ATTACHMENT_NOT_READ":
		return "确定性完成检查未通过：本次任务包含尚未读取的二进制附件。请根据冻结的附件元数据调用 asset_stage(asset_id)，再使用返回的只读路径通过合适的脚本或命令解析真实内容；不得改查空的项目 Workspace，也不得在未读取附件时直接总结。"
	case "WEB_EVIDENCE_MISSING":
		return "确定性完成检查未通过：任务要求联网检索，但当前 Run 没有任何成功的 web_search 收据。不得用“无法联网”说明冒充交付；请继续调用 web_search 获取真实证据，或在受控恢复耗尽后请求用户决定。"
	case "DELIVERABLE_MISSING":
		if input.DeliverableFormat == "pdf" {
			return "确定性完成检查未通过：任务要求 PDF，但当前 Run 没有新的、校验通过的 application/pdf Artifact。若脚本已经生成 PDF，不要重复转换；请先确认项目相对路径，再调用 artifact_capture(path, kind=deliverable) 登记现有文件。只有 %PDF- 签名、MIME、Hash 和大小校验全部通过后才能总结。"
		}
		return "确定性完成检查未通过：用户要求 Markdown/文档交付物，但当前 Run 与可追溯的续跑来源链中都没有最新交付 Artifact。请用 workspace_write_file 创建或 workspace_edit_file 更新真实内容，并验证文件存在，再给最终总结。"
	case "DELIVERABLE_TOO_SHORT":
		return fmt.Sprintf("确定性完成检查未通过：当前符合要求的单一主交付物为 %d 字，用户要求一份不少于 %d 字的完整报告。继续分段更新同一个主文件：保留稳定尾部锚点，读取最新 preimage_hash 后用 workspace_edit_file 将下一段正文插入锚点之前；过程分片的合计字数不计入主报告。达到要求、移除锚点并读取验证后才能总结。", input.ActualCharacters, input.RequiredCharacters)
	case "DELIVERABLE_CITATIONS_MISSING":
		return "确定性完成检查未通过：用户要求来源、引用或可点击链接，但单一主交付物中没有 http/https 来源链接。请把已验证证据的来源链接写入主报告并读取核对后再总结。"
	case "DELIVERABLE_INCOMPLETE":
		return "确定性完成检查未通过：用户要求完整交付物，但单一主文件仍包含续写锚点或明确的 TODO/TBD/待补充占位。请继续写完并移除占位后读取核对，不得把过程稿宣布为完成。"
	case "WORKSPACE_MUTATION_MISSING":
		return "确定性完成检查未通过：用户要求创建、修改、修复或提交工作区内容，但当前 Run 没有成功的工作区写入/编辑或受控 Git 变更收据。请使用与目标匹配的 workspace_write_file、workspace_edit_file 或 Git 变更工具执行真实动作并检查结果，不得只描述准备怎么做。"
	case "VERIFICATION_MISSING":
		return "确定性完成检查未通过：用户明确要求测试或验证，但当前 Run 没有变更后的验证收据。请运行适用的测试、命令、文件检查或浏览器工具，读取真实结果后再总结。"
	default:
		return "确定性完成检查未通过。请补齐缺失的执行证据后再交付，不得再次直接声明完成。"
	}
}

func isMCPTool(name string) bool {
	return strings.HasPrefix(name, "mcp__") && strings.Count(name, "__") >= 2
}

func parseMCPToolName(name string) (string, string, bool) {
	if !isMCPTool(name) {
		return "", "", false
	}
	parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

type mcpSnapshot struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	ManifestJSON  string        `json:"manifest_json"`
	CredentialRef string        `json:"credential_ref"`
	Tools         []mcpToolInfo `json:"tools"`
}

type mcpToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Sequential  bool           `json:"sequential"`
	Annotations struct {
		ReadOnlyHint    *bool `json:"readOnlyHint"`
		DestructiveHint *bool `json:"destructiveHint"`
		IdempotentHint  *bool `json:"idempotentHint"`
		OpenWorldHint   *bool `json:"openWorldHint"`
	} `json:"annotations"`
}

func mcpSnapshotsFromConfig(snapshot string) []mcpSnapshot {
	var config struct {
		Capabilities []mcpSnapshot `json:"mcp_capabilities"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return nil
	}
	return config.Capabilities
}

func mcpToolDefinitionsFromSnapshot(snapshot string) []ToolDefinition {
	definitions, _ := mcpToolDefinitionsForPrompt(snapshot, "")
	return definitions
}

// mcpToolDefinitionsForPrompt applies a deterministic, bounded capability
// context plan. Tool schemas are the most expensive part of a request, so a
// relevant subset is sent first; when no signal is available we retain the
// complete (bounded) snapshot rather than risking a false negative.
func mcpToolDefinitionsForPrompt(snapshot, prompt string) ([]ToolDefinition, map[string]any) {
	definitions := make([]ToolDefinition, 0)
	allCandidates := make([]ToolDefinition, 0)
	for _, capability := range mcpSnapshotsFromConfig(snapshot) {
		for _, tool := range capability.Tools {
			if strings.TrimSpace(tool.Name) == "" {
				continue
			}
			parameters := tool.InputSchema
			if len(parameters) == 0 {
				parameters = map[string]any{"type": "object", "additionalProperties": true}
			}
			allCandidates = append(allCandidates, ToolDefinition{
				Name:        "mcp__" + capability.ID + "__" + tool.Name,
				Description: mcpToolPromptDescription(capability, tool),
				Parameters:  parameters, Sequential: tool.Sequential,
				ReadOnlyHint:    tool.Annotations.ReadOnlyHint,
				DestructiveHint: tool.Annotations.DestructiveHint,
				IdempotentHint:  tool.Annotations.IdempotentHint,
				OpenWorldHint:   tool.Annotations.OpenWorldHint,
			})
		}
	}
	const maxDefinitions = 32
	tokens := contextTokens(prompt)
	for _, definition := range allCandidates {
		if len(definitions) >= maxDefinitions {
			break
		}
		if len(tokens) == 0 || definitionRelevant(definition, tokens) {
			definitions = append(definitions, definition)
		}
	}
	if len(definitions) == 0 && len(allCandidates) > 0 {
		limit := len(allCandidates)
		if limit > maxDefinitions {
			limit = maxDefinitions
		}
		definitions = append(definitions, allCandidates[:limit]...)
	}
	beforeTokens := estimateDefinitionTokens(allCandidates)
	afterTokens := estimateDefinitionTokens(definitions)
	return definitions, map[string]any{
		"strategy":                "deterministic_relevance_v1",
		"tool_definitions_before": len(allCandidates),
		"tool_definitions_after":  len(definitions),
		"estimated_tokens_before": beforeTokens,
		"estimated_tokens_after":  afterTokens,
		"estimated_tokens_saved":  maxInt64(0, beforeTokens-afterTokens),
		"max_tool_definitions":    maxDefinitions,
	}
}

func mcpToolPromptDescription(capability mcpSnapshot, tool mcpToolInfo) string {
	provider := strings.TrimSpace(capability.Name)
	if provider == "" {
		provider = "MCP Server"
	}
	description := strings.TrimSpace(tool.Description)
	if description == "" {
		description = "由已安装 MCP Server 提供的工具。"
	}
	identity := strings.ToLower(provider + " " + capability.ManifestJSON)
	if !strings.Contains(identity, "playwright") {
		return provider + " 的 " + tool.Name + "：" + description
	}
	hint := ""
	switch tool.Name {
	case "browser_snapshot", "browser_find":
		hint = "优先用它读取和定位页面内容。"
	case "browser_navigate":
		hint = "只负责打开明确网址；导航后优先用页面快照或查找读取内容。"
	case "browser_evaluate":
		hint = "仅在页面快照和查找无法获得所需结构时使用，并保持脚本只读且最小。"
	case "browser_run_code_unsafe":
		hint = "高风险兜底能力；普通导航、搜索和读取任务不得使用。"
	}
	return provider + " 浏览器自动化工具 " + tool.Name + "：" + description + " " + hint
}

func contextTokens(prompt string) map[string]struct{} {
	tokens := make(map[string]struct{})
	normalized := strings.ToLower(prompt)
	for _, raw := range strings.Fields(normalized) {
		clean := strings.Trim(raw, " .,;:!?()[]{}<>\"'`/\\")
		if len([]rune(clean)) >= 3 {
			tokens[clean] = struct{}{}
		}
	}
	// Space-delimited tokenization misses CJK prompts. Generate bounded 2-4
	// rune windows for contiguous Han text so a trigger such as “发布检查” can
	// match a natural sentence without loading every Skill body.
	sequences := strings.FieldsFunc(normalized, func(char rune) bool {
		return !unicode.IsLetter(char) && !unicode.IsDigit(char)
	})
	for _, sequence := range sequences {
		runes := []rune(sequence)
		hasHan := false
		for _, char := range runes {
			if unicode.Is(unicode.Han, char) {
				hasHan = true
				break
			}
		}
		if !hasHan {
			continue
		}
		for width := 2; width <= 4; width++ {
			for start := 0; start+width <= len(runes) && len(tokens) < 256; start++ {
				tokens[string(runes[start:start+width])] = struct{}{}
			}
		}
	}
	return tokens
}

func definitionRelevant(definition ToolDefinition, tokens map[string]struct{}) bool {
	text := strings.ToLower(definition.Name + " " + definition.Description)
	for token := range tokens {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func estimateDefinitionTokens(definitions []ToolDefinition) int64 {
	var total int64
	for _, definition := range definitions {
		body, _ := json.Marshal(definition)
		total += int64((len(body) + 3) / 4)
	}
	return total
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

type skillSnapshot struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	ContentHash  string `json:"content_hash"`
	ManifestJSON string `json:"manifest_json"`
}

func skillSnapshotsFromConfig(snapshot string) []skillSnapshot {
	var config struct {
		Skills []skillSnapshot `json:"skill_capabilities"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return nil
	}
	return config.Skills
}

func skillInvocationFromStep(inputJSON string) ([]string, string) {
	var input struct {
		InvokedSkillIDs []string `json:"invoked_skill_ids"`
		SkillArgs       string   `json:"skill_args"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		return nil, ""
	}
	seen := make(map[string]struct{}, len(input.InvokedSkillIDs))
	result := make([]string, 0, len(input.InvokedSkillIDs))
	for _, id := range input.InvokedSkillIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, strings.TrimSpace(input.SkillArgs)
}

func skillContextForIDs(snapshot string, ids []string, args string) (string, map[string]any) {
	skills := skillSnapshotsFromConfig(snapshot)
	requested := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		requested[id] = struct{}{}
	}
	loadedIDs := make([]string, 0, len(ids))
	loadedNames := make([]string, 0, len(ids))
	parts := make([]string, 0, len(ids))
	allowedToolSet := make(map[string]struct{})
	requiredCapabilitySet := make(map[string]struct{})
	preferredToolSet := make(map[string]struct{})
	toolScopeDeclared := false
	strictFallback := false
	// Explicit invocation is an auditable user choice. Preserve enough of a
	// substantial workflow Skill to include its QA and delivery gates; the
	// smaller automatic-match limit below remains the token-saving fallback.
	const maxSkillBody = 24000
	for _, skill := range skills {
		if _, ok := requested[skill.ID]; !ok {
			continue
		}
		var manifest map[string]any
		if json.Unmarshal([]byte(skill.ManifestJSON), &manifest) != nil {
			continue
		}
		body := ""
		for _, key := range []string{"instructions", "content", "body", "prompt"} {
			if value, ok := manifest[key].(string); ok && strings.TrimSpace(value) != "" {
				body = strings.TrimSpace(value)
				break
			}
		}
		if body == "" {
			continue
		}
		if len([]rune(body)) > maxSkillBody {
			body = string([]rune(body)[:maxSkillBody]) + "…"
		}
		tools := manifestStringList(manifest["allowed_tools"])
		if len(tools) > 0 {
			toolScopeDeclared = true
			for _, tool := range tools {
				allowedToolSet[tool] = struct{}{}
			}
		}
		for _, capability := range manifestStringList(firstManifestValue(manifest, "required_capabilities", "required-capabilities")) {
			requiredCapabilitySet[capability] = struct{}{}
		}
		for _, tool := range manifestStringList(firstManifestValue(manifest, "preferred_tools", "preferred-tools")) {
			preferredToolSet[tool] = struct{}{}
		}
		if fallbackPolicy, _ := firstManifestValue(manifest, "fallback_policy", "fallback-policy").(string); fallbackPolicy == "strict" {
			strictFallback = true
		}
		header := "# " + skill.Name + "@" + skill.Version
		if len(tools) > 0 {
			header += "\n声明可用工具（不扩大运行时权限）：" + strings.Join(tools, ", ")
		}
		parts = append(parts, header+"\n"+body)
		loadedIDs = append(loadedIDs, skill.ID)
		loadedNames = append(loadedNames, skill.Name)
	}
	if strings.TrimSpace(args) != "" {
		parts = append(parts, "# 本次 Skill 参数\n"+strings.TrimSpace(args))
	}
	allowedTools := make([]string, 0, len(allowedToolSet))
	for tool := range allowedToolSet {
		allowedTools = append(allowedTools, tool)
	}
	sort.Strings(allowedTools)
	requiredCapabilities := sortedStringSet(requiredCapabilitySet)
	preferredTools := sortedStringSet(preferredToolSet)
	fallbackPolicy := "same-capability"
	if strictFallback {
		fallbackPolicy = "strict"
	}
	return strings.Join(parts, "\n\n"), map[string]any{
		"strategy":              "explicit_skill_invocation_v1",
		"available":             len(skills),
		"requested":             len(ids),
		"loaded":                len(loadedIDs),
		"loaded_ids":            loadedIDs,
		"loaded_names":          loadedNames,
		"args_provided":         strings.TrimSpace(args) != "",
		"tool_scope_declared":   toolScopeDeclared,
		"allowed_tools":         allowedTools,
		"required_capabilities": requiredCapabilities,
		"preferred_tools":       preferredTools,
		"fallback_policy":       fallbackPolicy,
	}
}

func firstManifestValue(manifest map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := manifest[key]; exists {
			return value
		}
	}
	return nil
}

func sortedStringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func missingStrictSkillCapabilities(snapshot string, skillPlan map[string]any) []string {
	if skillPlan == nil || skillPlan["fallback_policy"] != "strict" {
		return nil
	}
	required, _ := skillPlan["required_capabilities"].([]string)
	if len(required) == 0 {
		return nil
	}
	available := make(map[string]struct{})
	documentAvailable := documentRuntimeSnapshotted(snapshot)
	for _, spec := range core.BuiltinToolSpecs() {
		if isDocumentTool(spec.Name) && !documentAvailable {
			continue
		}
		available[spec.Name] = struct{}{}
		for _, alias := range spec.Aliases {
			available[alias] = struct{}{}
		}
		for _, capability := range spec.Capabilities {
			available[capability] = struct{}{}
		}
	}
	for _, capability := range mcpSnapshotsFromConfig(snapshot) {
		available[capability.ID] = struct{}{}
		for _, tool := range capability.Tools {
			available[tool.Name] = struct{}{}
			available["mcp__"+capability.ID+"__"+tool.Name] = struct{}{}
		}
	}
	missing := make([]string, 0)
	for _, capability := range required {
		if _, exists := available[capability]; !exists {
			missing = append(missing, capability)
		}
	}
	sort.Strings(missing)
	return missing
}

func documentRuntimeSnapshotted(snapshotJSON string) bool {
	var snapshot struct {
		DocumentRuntime struct {
			Protocol       string `json:"protocol"`
			RuntimeVersion string `json:"runtime_version"`
		} `json:"document_runtime"`
	}
	return json.Unmarshal([]byte(snapshotJSON), &snapshot) == nil && snapshot.DocumentRuntime.Protocol == "oktapod.document/v1" && strings.TrimSpace(snapshot.DocumentRuntime.RuntimeVersion) != ""
}

func filterOutDocumentToolDefinitions(definitions []ToolDefinition) []ToolDefinition {
	result := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if !isDocumentTool(definition.Name) {
			result = append(result, definition)
		}
	}
	return result
}

func skillInvokeToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        core.ToolSkillInvoke,
		Description: "Load one enabled project Skill from the immutable Run snapshot, then resume this task with that Skill's instructions and tool scope. Call this tool alone before any other tool.",
		Sequential:  true,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"skill"},
			"properties": map[string]any{
				"skill": map[string]any{"type": "string", "minLength": 1, "maxLength": 200, "description": "Exact Skill name or id from the catalog"},
				"args":  map[string]any{"type": "string", "maxLength": 10000, "description": "Optional user arguments for the Skill"},
			},
		},
	}
}

func annotateToolCallExecutionMode(calls []core.ToolCall, definitions []ToolDefinition) {
	sequential := map[string]bool{
		core.ToolSkillInvoke:         true,
		core.ToolWorkspaceWriteFile:  true,
		core.ToolWorkspaceRunCommand: true,
	}
	definitionByName := make(map[string]ToolDefinition, len(definitions))
	for _, definition := range definitions {
		definitionByName[definition.Name] = definition
		if definition.Sequential {
			sequential[definition.Name] = true
		}
	}
	for index := range calls {
		definition, known := definitionByName[calls[index].Name]
		exclusive := !known || sequential[calls[index].Name]
		if isMCPTool(calls[index].Name) {
			// An MCP call participates in a safe parallel segment only when the
			// frozen server annotations prove it is read-only and idempotent.
			// Mutation and incomplete/unknown metadata form an exclusive barrier.
			exclusive = exclusive || definition.ReadOnlyHint == nil || !*definition.ReadOnlyHint ||
				definition.DestructiveHint == nil || *definition.DestructiveHint ||
				definition.IdempotentHint == nil || !*definition.IdempotentHint
		}
		calls[index].Sequential = calls[index].Sequential || exclusive
	}
}

// skillContextForPrompt implements metadata-first, lazy Skill loading. The
// full manifest is retained in the immutable Run snapshot, but only selected
// instruction text enters the model request and it is bounded per Skill.
func skillContextForPrompt(snapshot, prompt string) (string, map[string]any) {
	skills := skillSnapshotsFromConfig(snapshot)
	builtInContext, builtInName := builtInSkillContext(prompt)
	if len(skills) == 0 && builtInContext == "" {
		return "", map[string]any{"strategy": "metadata_only", "available": 0, "loaded": 0, "tool_scope_declared": false, "allowed_tools": []string{}, "estimated_tokens_saved": 0}
	}
	tokens := contextTokens(prompt)
	loaded := make([]string, 0, len(skills))
	loadedIDs := make([]string, 0, len(skills))
	parts := make([]string, 0, len(skills))
	if builtInContext != "" {
		parts = append(parts, builtInContext)
		loaded = append(loaded, builtInName)
		loadedIDs = append(loadedIDs, "builtin-skill-creator")
	}
	allowedToolSet := make(map[string]struct{})
	toolScopeDeclared := false
	const maxSkillBody = 6000
	for _, skill := range skills {
		if strings.TrimSpace(skill.Name) == "" || (len(tokens) > 0 && !skillRelevant(skill, tokens, prompt)) {
			continue
		}
		var manifest map[string]any
		if json.Unmarshal([]byte(skill.ManifestJSON), &manifest) != nil {
			continue
		}
		body := ""
		for _, key := range []string{"instructions", "content", "body", "prompt"} {
			if value, ok := manifest[key].(string); ok && strings.TrimSpace(value) != "" {
				body = strings.TrimSpace(value)
				break
			}
		}
		if body == "" {
			continue
		}
		if len([]rune(body)) > maxSkillBody {
			body = string([]rune(body)[:maxSkillBody]) + "…"
		}
		if tools := manifestStringList(manifest["allowed_tools"]); len(tools) > 0 {
			toolScopeDeclared = true
			for _, tool := range tools {
				allowedToolSet[tool] = struct{}{}
			}
		}
		loaded = append(loaded, skill.Name)
		loadedIDs = append(loadedIDs, skill.ID)
		header := "# " + skill.Name + "@" + skill.Version
		if tools := manifestStringList(manifest["allowed_tools"]); len(tools) > 0 {
			header += "\n声明可用工具（不扩大运行时权限）：" + strings.Join(tools, ", ")
		}
		parts = append(parts, header+"\n"+body)
	}
	before := int64(len([]rune(builtInContext)) / 4)
	for _, skill := range skills {
		before += int64(len([]rune(skill.ManifestJSON)) / 4)
	}
	after := int64(len([]rune(strings.Join(parts, "\n\n"))) / 4)
	allowedTools := make([]string, 0, len(allowedToolSet))
	for tool := range allowedToolSet {
		allowedTools = append(allowedTools, tool)
	}
	sort.Strings(allowedTools)
	return strings.Join(parts, "\n\n"), map[string]any{
		"strategy":                "metadata_first_lazy_v1",
		"available":               len(skills) + boolInt(builtInContext != ""),
		"loaded":                  len(loaded),
		"loaded_ids":              loadedIDs,
		"loaded_names":            loaded,
		"estimated_tokens_before": before,
		"estimated_tokens_after":  after,
		"estimated_tokens_saved":  maxInt64(0, before-after),
		"max_skill_body_chars":    maxSkillBody,
		"tool_scope_declared":     toolScopeDeclared,
		"allowed_tools":           allowedTools,
	}
}

func suppressedSkillContextForExplicitMCP(snapshot string) (string, map[string]any) {
	skills := skillSnapshotsFromConfig(snapshot)
	var estimatedBefore int64
	for _, skill := range skills {
		estimatedBefore += int64(len([]rune(skill.ManifestJSON)) / 4)
	}
	return "", map[string]any{
		"strategy":                "suppressed_by_explicit_mcp_v1",
		"available":               len(skills),
		"loaded":                  0,
		"loaded_ids":              []string{},
		"loaded_names":            []string{},
		"estimated_tokens_before": estimatedBefore,
		"estimated_tokens_after":  int64(0),
		"estimated_tokens_saved":  estimatedBefore,
		"tool_scope_declared":     false,
		"allowed_tools":           []string{},
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func skillToolScope(plan map[string]any) ([]string, bool) {
	declared, _ := plan["tool_scope_declared"].(bool)
	allowed, _ := plan["allowed_tools"].([]string)
	return allowed, declared
}

// applySceneToolPolicy turns the frozen Run scene policy into the model's
// advertised builtin-tool scope. This prevents the model from planning an
// action that would inevitably be rejected only after an approval prompt.
func applySceneToolPolicy(snapshot string, allowed []string, declared bool) ([]string, bool, map[string]any) {
	if currentSnapshotSemantics(snapshot) {
		return allowed, declared, map[string]any{"workspace_command": true, "filtered_tools": []string{}, "source": "execution_policy"}
	}
	if sceneAllowsWorkspaceCommand(snapshot) {
		return allowed, declared, map[string]any{"workspace_command": true, "filtered_tools": []string{}}
	}
	if !declared {
		allowed = []string{
			core.ToolUpdatePlan, core.ToolRequestInput, core.ToolRequestPermission,
			core.ToolWebSearch, core.ToolWorkspaceReadFile, core.ToolWorkspaceListFiles,
			core.ToolWorkspaceFindFiles, core.ToolWorkspaceGrep,
			core.ToolWorkspaceWriteFile, core.ToolWorkspaceEditFile,
		}
		for _, capability := range mcpSnapshotsFromConfig(snapshot) {
			for _, tool := range capability.Tools {
				if strings.TrimSpace(capability.ID) == "" || strings.TrimSpace(tool.Name) == "" {
					continue
				}
				allowed = append(allowed, "mcp__"+capability.ID+"__"+tool.Name)
			}
		}
	} else {
		filtered := make([]string, 0, len(allowed))
		for _, name := range allowed {
			if name != core.ToolWorkspaceRunCommand && name != core.ToolWorkspaceShell {
				filtered = append(filtered, name)
			}
		}
		allowed = filtered
	}
	return allowed, true, map[string]any{"workspace_command": false, "filtered_tools": []string{core.ToolWorkspaceRunCommand, core.ToolWorkspaceShell}}
}

func explicitMCPProviderFromPrompt(snapshot, prompt string) (string, string) {
	normalizedPrompt := strings.ToLower(prompt)
	for _, capability := range mcpSnapshotsFromConfig(snapshot) {
		name := strings.TrimSpace(capability.Name)
		if len([]rune(name)) < 3 || !strings.Contains(normalizedPrompt, strings.ToLower(name)) {
			continue
		}
		return name, capability.ID
	}
	return "", ""
}

func filterToolNamesForExplicitMCP(names []string, capabilityID string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if explicitMCPToolAllowed(name, capabilityID) {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func filterToolsForExplicitMCP(definitions []ToolDefinition, capabilityID string) []ToolDefinition {
	filtered := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if explicitMCPToolAllowed(definition.Name, capabilityID) {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func excludeToolDefinition(definitions []ToolDefinition, excluded string) []ToolDefinition {
	filtered := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name != excluded {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func excludeToolName(names []string, excluded string) []string {
	filtered := make([]string, 0, len(names))
	for _, name := range names {
		if name != excluded {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func explicitMCPToolAllowed(name, capabilityID string) bool {
	if strings.HasPrefix(name, "mcp__") {
		return strings.HasPrefix(name, "mcp__"+capabilityID+"__")
	}
	// An explicit MCP is a source/provider choice, not an instruction to remove
	// local delivery capabilities. Exclude the interchangeable builtin search,
	// while preserving workspace, Git, planning, input and approval tools so a
	// task can turn MCP evidence into verified artifacts.
	return name != core.ToolWebSearch && name != core.ToolWebFetch
}

func filterToolDefinitionsBySkill(definitions []ToolDefinition, plan map[string]any) []ToolDefinition {
	allowed, declared := skillToolScope(plan)
	if !declared {
		return definitions
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	filtered := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name == core.ToolUpdatePlan || definition.Name == core.ToolRequestInput || definition.Name == core.ToolRequestPermission {
			filtered = append(filtered, definition)
			continue
		}
		if _, ok := allowedSet[definition.Name]; ok {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func filterOutToolDefinition(definitions []ToolDefinition, toolName string) []ToolDefinition {
	filtered := make([]ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name != toolName {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func skillRelevant(skill skillSnapshot, tokens map[string]struct{}, prompt string) bool {
	text := strings.ToLower(skill.Name + " " + skill.Version)
	var manifest map[string]any
	if json.Unmarshal([]byte(skill.ManifestJSON), &manifest) == nil {
		for _, trigger := range append(manifestStringList(manifest["triggers"]), manifestStringList(manifest["keywords"])...) {
			if strings.Contains(strings.ToLower(prompt), strings.ToLower(trigger)) {
				return true
			}
		}
		// Match only metadata. Searching full instructions turns generic workflow
		// words such as "skill", "file", or "verify" into false positives and
		// defeats metadata-first lazy loading.
		for _, key := range []string{"description", "keywords", "triggers"} {
			if value, ok := manifest[key]; ok {
				encoded, _ := json.Marshal(value)
				text += " " + strings.ToLower(string(encoded))
			}
		}
	}
	for token := range tokens {
		if strings.Contains(text, token) {
			return true
		}
	}
	return false
}

func skillCatalogForPrompt(snapshot string) (string, map[string]any) {
	var config struct {
		Skills []skillSnapshot `json:"skill_capabilities"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil || len(config.Skills) == 0 {
		return "", map[string]any{"available": 0, "listed": 0, "strategy": "metadata_only_v1"}
	}
	const maxSkills = 32
	const maxDescription = 300
	lines := make([]string, 0, minInt(len(config.Skills), maxSkills))
	for _, skill := range config.Skills {
		if len(lines) >= maxSkills {
			break
		}
		var manifest map[string]any
		if json.Unmarshal([]byte(skill.ManifestJSON), &manifest) != nil {
			continue
		}
		description, _ := manifest["description"].(string)
		description = strings.TrimSpace(description)
		if len([]rune(description)) > maxDescription {
			description = string([]rune(description)[:maxDescription]) + "…"
		}
		line := "- " + skill.Name + "@" + skill.Version
		if description != "" {
			line += "：" + description
		}
		if triggers := manifestStringList(manifest["triggers"]); len(triggers) > 0 {
			if len(triggers) > 6 {
				triggers = triggers[:6]
			}
			line += "；触发词=" + strings.Join(triggers, "、")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), map[string]any{"available": len(config.Skills), "listed": len(lines), "strategy": "metadata_only_v1", "max_listed": maxSkills}
}

func manifestStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func thinkingLevelFromSnapshot(snapshot string) string {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return "auto"
	}
	level, _ := config["thinking_level"].(string)
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "off", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(level))
	default:
		return "auto"
	}
}

func modelResultThinking(result core.ModelResult) string {
	var value strings.Builder
	for _, block := range result.ContentBlocks {
		if block.Type != core.ContentBlockThinking || block.Thinking == nil || strings.TrimSpace(block.Thinking.Text) == "" {
			continue
		}
		if value.Len() > 0 {
			value.WriteByte('\n')
		}
		value.WriteString(block.Thinking.Text)
	}
	return value.String()
}

func agentModelsFromSnapshot(snapshot string) (string, string) {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return "", ""
	}
	model, _ := config["model_override"].(string)
	fallback, _ := config["fallback_model_override"].(string)
	if strings.TrimSpace(model) == "" {
		model, _ = config["project_default_model"].(string)
	}
	if strings.TrimSpace(fallback) == "" {
		fallback, _ = config["project_fallback_model"].(string)
	}
	agent, ok := config["agent"].(map[string]any)
	if ok {
		if strings.TrimSpace(model) == "" {
			model, _ = agent["model"].(string)
		}
		if strings.TrimSpace(fallback) == "" {
			fallback, _ = agent["fallback_model"].(string)
		}
	}
	return strings.TrimSpace(model), strings.TrimSpace(fallback)
}

func modelRefFromSnapshot(snapshot, fallbackModelID string) core.ModelRef {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return core.ModelRef{ModelID: strings.TrimSpace(fallbackModelID)}
	}
	providerID, _ := config["effective_provider_id"].(string)
	modelID, _ := config["effective_model_id"].(string)
	if strings.TrimSpace(modelID) == "" {
		modelID = fallbackModelID
	}
	return core.ModelRef{ProviderID: strings.TrimSpace(providerID), ModelID: strings.TrimSpace(modelID)}
}

type frozenAssetSnapshot struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MediaType   string `json:"media_type"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentHash string `json:"content_hash"`
}

func frozenAssetFromSnapshot(snapshot, assetID string) (frozenAssetSnapshot, bool) {
	var config struct {
		Assets []frozenAssetSnapshot `json:"context_asset_snapshots"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return frozenAssetSnapshot{}, false
	}
	for _, asset := range config.Assets {
		if asset.ID == assetID {
			return asset, true
		}
	}
	return frozenAssetSnapshot{}, false
}

func sceneAllowsWorkspaceCommand(snapshot string) bool {
	if currentSnapshotSemantics(snapshot) {
		return true
	}
	if core.FullAccessActive(snapshot, time.Now().UTC()) {
		return true
	}
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return true
	}
	policy, ok := config["scene_policy"].(map[string]any)
	if !ok {
		return true
	}
	allowed, ok := policy["workspace_command"].(bool)
	return !ok || allowed
}

func currentSnapshotSemantics(snapshot string) bool {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return false
	}
	version, ok := config["schema_version"].(float64)
	return ok && version >= 2
}

func skillReferenceMatches(skill skillSnapshot, requested string) bool {
	requested = strings.TrimSpace(requested)
	return skill.ID == requested || strings.EqualFold(skill.Name, requested) ||
		strings.EqualFold(skill.Name+"@"+skill.Version, requested)
}

func (w *Worker) heartbeat(ctx context.Context, step core.ClaimedStep, done chan<- struct{}, cancel context.CancelFunc) {
	defer close(done)
	interval := w.lease / 3
	if interval > 3*time.Second {
		interval = 3 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			owned, err := w.repository.HeartbeatStep(ctx, step, now.UTC(), now.UTC().Add(w.lease))
			if err != nil {
				w.logger.WarnContext(ctx, "step heartbeat failed", "step_id", step.ID, "fence_token", step.FenceToken, "error", err)
				continue
			}
			if !owned {
				w.logger.WarnContext(ctx, "step lease lost", "step_id", step.ID, "fence_token", step.FenceToken)
				cancel()
				return
			}
		}
	}
}

func prompts(run core.RunContext) (string, string) {
	return (PromptAssembler{}).Base(run)
}

func continuationHistoryForPrompt(inputJSON string) []map[string]any {
	var input struct {
		History []map[string]any `json:"history"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil || len(input.History) == 0 {
		return nil
	}
	return input.History
}

func conversationMessagesFromSnapshot(configJSON string) []core.ModelMessage {
	var snapshot struct {
		Messages []core.ModelMessage `json:"conversation_messages"`
	}
	if json.Unmarshal([]byte(configJSON), &snapshot) != nil {
		return nil
	}
	return snapshot.Messages
}

// steeringInstructionForPrompt returns the user steering instruction carried by
// a steering_replan step, or an empty string for ordinary continuation steps.
func steeringInstructionForPrompt(inputJSON string) string {
	var input struct {
		Stage    string `json:"stage"`
		Steering string `json:"steering"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil || input.Stage != "steering_replan" {
		return ""
	}
	return strings.TrimSpace(input.Steering)
}

// contextForPrompt loads only explicitly selected, already authorized context.
// It is metadata-first and bounded so adding an asset or memory cannot turn a
// normal task into an unbounded prompt or silently broaden project scope.
func newCorrelationID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Sprintf("corr-%d", time.Now().UnixNano())
	}
	return id.String()
}

func safeErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len([]rune(message)) > 500 {
		message = string([]rune(message)[:500])
	}
	encoded, _ := json.Marshal(message)
	var normalized string
	_ = json.Unmarshal(encoded, &normalized)
	return normalized
}
