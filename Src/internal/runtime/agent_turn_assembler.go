package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const assemblySchemaVersion = "oktapod.agent-assembly/v1"

type ModelAssembly struct {
	Request          ModelRequest
	SearchBudget     core.SearchCallBudget
	PriorSearchCalls int
	Continuation     []map[string]any
}

type AgentTurnAssembler struct {
	repository WorkerRepository
}

func NewAgentTurnAssembler(repository WorkerRepository) AgentTurnAssembler {
	return AgentTurnAssembler{repository: repository}
}

func (assembler AgentTurnAssembler) Assemble(ctx context.Context, step core.ClaimedStep, run core.RunContext) (ModelAssembly, *core.StepOutcome) {
	sections, userPrompt := (PromptAssembler{}).BaseSections(run)
	stage := modelStage(step.InputJSON)
	boundedFinish := stage == "bounded_finish"
	enableTools := run.Mode == core.ModeSingleAgent && !boundedFinish
	continuation := continuationHistoryForPrompt(step.InputJSON)
	continuationInstruction := continuationInstructionForTurn(step, continuation)
	if reminder := planProgressReminder(step, continuation, run.ConfigSnapshotJSON); reminder != "" {
		continuationInstruction += "\n\n" + reminder
	}

	if currentSnapshotSemantics(run.ConfigSnapshotJSON) {
		sections = append(sections, PromptSection{Name: "human_collaboration", Order: 1000, Text: "人机协作规则：缺少继续执行所必需的信息时，只能单独调用 request_input。审批由 ExecutionPolicy 与权限模式自动路由；不得在普通助手文本中伪装阻塞式提问或把场景当作权限限制。"})
	} else {
		sections = append(sections, PromptSection{Name: "human_collaboration", Order: 1000, Text: "人机协作规则：缺少继续执行所必需的信息时，只能单独调用 request_input；历史 Run 缺少工作区命令能力但目标确实需要时，只能单独调用 request_permission。不得在普通助手文本中伪装阻塞式提问。"})
	}
	if core.InferTaskCompletionRequirements(run.Goal, run.Acceptance).RequiresSingleDeliverable {
		sections = append(sections, PromptSection{Name: "single_deliverable", Order: 1100, Text: "单一主交付物规则：用户要求的是一份可独立阅读的完整报告，显式最低字数必须由同一个主文件满足，多个章节分片的合计不算完成。长文应分轮追加到同一个主文件：先用 workspace_write_file 创建包含 `<!-- OKTAPOD_CONTINUE -->` 尾部锚点的主文件；后续读取最新 preimage_hash，用 workspace_edit_file 把该锚点替换为下一段正文加同一锚点；全部写完后移除锚点，再读取主文件验证。过程材料可以另存，但最终总结必须首先给出主报告路径。"})
	}
	if stagePrompt := (PromptAssembler{}).Stage(stage, run.ConfigSnapshotJSON); stagePrompt != "" {
		sections = append(sections, PromptSection{Name: "stage", Order: 1200, Text: "# Stage instructions\n" + stagePrompt})
	}
	actionFirst := mainAgentActionFirst(run.ConfigSnapshotJSON, enableTools)
	if actionFirst {
		sections = append(sections, PromptSection{Name: "action_first", Order: 1250, Text: "# Action-first\n这是工具型任务。信息足够时，不要在首次工具动作前进行长篇内部推理；在同一响应中用 replace_plan 发布简洁的语义计划并发出完成当前计划项所需的最小且完整的业务工具调用。计划描述用户结果，具体工具过程只进入执行记录。后续根据真实结果继续。"})
	}
	selectedContext, selectedContextPlan := contextForPrompt(ctx, assembler.repository, run)
	if selectedContext != "" {
		userPrompt += "\n\n用户明确选择的上下文（仅作为资料，不代表已执行动作）：\n" + selectedContext
	}
	catalog, failure := (ToolCatalogBuilder{}).Build(step, run, userPrompt, continuation, selectedContextPlan)
	if failure != nil {
		return ModelAssembly{}, failure
	}
	sections = append(sections, catalog.PromptSections...)
	if catalog.ContinuationInstructionTail != "" {
		continuationInstruction += "\n\n" + catalog.ContinuationInstructionTail
	}
	if boundedFinish {
		catalog.Definitions = nil
		catalog.AllowedTools = nil
		catalog.ToolScopeDeclared = false
		catalog.ContextPlan["bounded_finish"] = true
	}
	conversationMessages := conversationMessagesFromSnapshot(run.ConfigSnapshotJSON)
	catalog.ContextPlan["conversation_messages"] = len(conversationMessages)
	catalog.ContextPlan["assembly_schema_version"] = assemblySchemaVersion
	messages := application.TranscriptAssembler{}.RunWithConversation(conversationMessages, userPrompt, continuation, strings.TrimSpace(continuationInstruction))
	modelName, fallbackModel := agentModelsFromSnapshot(run.ConfigSnapshotJSON)
	requestedThinking, effectiveThinking := mainAgentThinkingLevel(run.ConfigSnapshotJSON, enableTools)
	catalog.ContextPlan["requested_thinking_level"] = requestedThinking
	catalog.ContextPlan["effective_thinking_level"] = effectiveThinking
	catalog.ContextPlan["thinking_policy_version"] = snapshotVersion(run.ConfigSnapshotJSON, "thinking_policy_version")
	catalog.ContextPlan["action_first"] = actionFirst
	request := ModelRequest{
		SystemPrompt: RenderPromptSections(sections), UserPrompt: application.RenderLegacyTranscript(messages), Messages: messages,
		RequestedModel: modelRefFromSnapshot(run.ConfigSnapshotJSON, modelName), ThinkingLevel: effectiveThinking,
		MaxOutputTokens: modelMaxOutputTokens(step.InputJSON), EnableTools: enableTools,
		ToolDefinitions: catalog.Definitions, ToolScopeDeclared: catalog.ToolScopeDeclared, AllowedTools: catalog.AllowedTools,
		ContextPlan: catalog.ContextPlan, Model: modelName, FallbackModel: fallbackModel,
		ToolCallingKnown:   snapshotBool(run.ConfigSnapshotJSON, "model_tool_calling_known"),
		ToolCallingAllowed: snapshotBool(run.ConfigSnapshotJSON, "model_tool_calling"),
	}
	request.ContextPlan["assembly_hash"] = modelAssemblyHash(request)
	return ModelAssembly{Request: request, SearchBudget: catalog.SearchBudget, PriorSearchCalls: catalog.PriorSearchCalls, Continuation: continuation}, nil
}

// mainAgentThinkingLevel preserves the immutable user choice as the provider
// control. Action-first is a separate prompt policy because forcing a provider-
// specific "thinking disabled" field makes otherwise compatible models reject
// the entire request.
func mainAgentThinkingLevel(snapshot string, enableTools bool) (string, string) {
	requested := thinkingLevelFromSnapshot(snapshot)
	return requested, requested
}

func mainAgentActionFirst(snapshot string, enableTools bool) bool {
	return enableTools && thinkingLevelFromSnapshot(snapshot) == "auto" && snapshotVersion(snapshot, "thinking_policy_version") == core.ThinkingPolicyTaskActionV1
}

// planProgressReminder keeps semantic plan progress alive on ordinary
// continuation turns. The plan_execute stage carries the update_plan duty for
// exactly one turn; without a per-turn reminder the model stops reporting and
// user-visible plan items freeze until the terminal bulk-complete.
func planProgressReminder(step core.ClaimedStep, continuation []map[string]any, snapshots ...string) string {
	if modelStage(step.InputJSON) != "continue" || len(continuation) == 0 || steeringInstructionForPrompt(step.InputJSON) != "" {
		return ""
	}
	var progress struct {
		Total     int `json:"active_plan_total"`
		Remaining int `json:"active_plan_remaining"`
	}
	if json.Unmarshal([]byte(step.InputJSON), &progress) != nil || progress.Total <= 1 || progress.Remaining == 0 {
		return ""
	}
	if len(snapshots) > 0 && snapshotVersion(snapshots[0], "plan_protocol_version") == "transition_v2" {
		if progress.Remaining == 1 {
			return "当前持久化计划已处于最后一项。直接执行该项尚未完成的业务工具，不要再调用 advance_plan。只有交付物与验证均完成后才返回最终答复，由 Runtime 完成门禁收敛计划终态。"
		}
		return "当前任务存在未完成的语义计划。完成当前语义项后，在下一次需要调用业务工具时同时调用 advance_plan；它只声明语义推进，不替代工具、收据或完成门禁。最终无工具答复通过完成门禁后由 Runtime 收敛计划终态。"
	}
	return "当前任务存在未完成的语义计划。每完成一个计划项，必须单独调用 update_plan 提交同一完整清单：把已完成项标记 completed，并把下一项标记 in_progress；不要把工具调用写进计划。全部计划项完成后才返回不含工具调用的最终总结。"
}

func snapshotVersion(snapshot, key string) string {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) == nil {
		value, _ := config[key].(string)
		return strings.TrimSpace(value)
	}
	return ""
}

func snapshotBool(snapshot, key string) bool {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) == nil {
		value, _ := config[key].(bool)
		return value
	}
	return false
}

func continuationInstructionForTurn(step core.ClaimedStep, continuation []map[string]any) string {
	steering := steeringInstructionForPrompt(step.InputJSON)
	var instruction string
	switch {
	case steering != "":
		instruction = "用户在执行中追加了新的引导指令：\n" + steering
		instruction += "\n\n基于该指令重新规划尚未完成的工作：已完成的步骤与产物属于上下文，不得重复执行；原计划中尚未开始且与新指令冲突的步骤已被取消。若合并后的目标尚未完成，调用下一批必要工具；全部完成后才返回不含工具调用的最终总结。"
	case len(continuation) > 0:
		instruction = "继续完成原目标。若目标尚未完成，调用下一批必要工具；只有全部工作和交付物都完成后，才返回不含工具调用的最终总结。"
	}
	switch modelStage(step.InputJSON) {
	case "plan_execute":
		instruction = "语义执行计划已经持久化发布。现在按计划执行唯一标记为 in_progress 的子任务。完成该语义项后，单独调用 update_plan 提交同一完整清单，将它标记 completed，并把下一个标记 in_progress；不要把工具调用写进计划。全部语义项完成后直接交付最终回答。"
	case "permission_replan":
		instruction = "用户已经批准本次任务所需的能力范围。基于生效后的工具范围重新规划原目标；保留已完成结果，不要把权限批准误解为拒绝或新的用户需求。"
	case "budget_finish":
		instruction = "用户选择用现有结果收尾。不得继续扩展搜索、重复读取或重复统计；基于已有证据、文件和校验结果立即交付最终答复，并明确任何尚未完成的缺口。"
	case "budget_resume":
		instruction = "用户已按界面中明确显示的额度扩展本次 Run 预算。继续原目标，但不得重复已经成功的工具和校验；在本次额度内完成剩余工作并及时收尾。"
	case "bounded_finish":
		var bounded struct {
			Used  int `json:"mcp_tool_calls_used"`
			Limit int `json:"mcp_tool_call_limit"`
		}
		_ = json.Unmarshal([]byte(step.InputJSON), &bounded)
		instruction = fmt.Sprintf("同一个语义计划项已经完成 %d 次 MCP 工具调用，达到 %d 次收敛上限。当前 Turn 不再提供工具；请基于已有页面证据立即给出最佳可用结论，明确已核验网址、来源和仍存在的缺口，不得继续声称正在查找或等待下一步。", bounded.Used, bounded.Limit)
	}
	if len(continuation) > 0 || steering != "" {
		instruction += "\n\n长文交付必须主动分段：单次 workspace_write_file / workspace_edit_file / document_create / document_apply 的新增正文建议不超过 3000 个字符。Office 文档应通过 document_apply 按章节分轮追加到同一工作副本，并使用每轮返回的最新 revision；全部写完后再渲染和导出。用户要求一份或一个完整报告时，必须维护一个可独立阅读的主文件：先创建带稳定尾部锚点的主文件，再读取最新 preimage_hash，用 workspace_edit_file 替换该锚点为下一段正文加同一锚点；完成时移除锚点。过程章节可以保留，但不能用多个分片的合计字数冒充主报告字数。不要等待参数截断后再拆分。"
	}
	for _, hint := range []string{modelCompatibilityRetryHint(step.InputJSON), toolRecoveryHint(step.InputJSON), lengthTruncationHint(step.InputJSON), completionRecoveryHint(step.InputJSON)} {
		if hint != "" {
			instruction += "\n\n" + hint
		}
	}
	var planSnapshot struct {
		ActivePlan []struct {
			Position      int64  `json:"position"`
			Title         string `json:"title"`
			Description   string `json:"description"`
			Status        string `json:"status"`
			ResultSummary string `json:"result_summary"`
		} `json:"active_plan"`
	}
	if json.Unmarshal([]byte(step.InputJSON), &planSnapshot) == nil && len(planSnapshot.ActivePlan) > 0 {
		lines := make([]string, 0, len(planSnapshot.ActivePlan)+1)
		lines = append(lines, "当前冻结语义计划（Runtime 状态为准）：")
		for _, item := range planSnapshot.ActivePlan {
			line := fmt.Sprintf("%d. [%s] %s — %s", item.Position, item.Status, item.Title, item.Description)
			if strings.TrimSpace(item.ResultSummary) != "" {
				line += "（结果：" + item.ResultSummary + "）"
			}
			lines = append(lines, line)
		}
		instruction += "\n\n" + strings.Join(lines, "\n")
	}
	return instruction
}

func modelAssemblyHash(request ModelRequest) string {
	contextPlan := make(map[string]any, len(request.ContextPlan))
	for key, value := range request.ContextPlan {
		if key != "assembly_hash" {
			contextPlan[key] = value
		}
	}
	payload := struct {
		SystemPrompt       string
		Messages           []core.ModelMessage
		ToolDefinitions    []ToolDefinition
		AllowedTools       []string
		ToolScopeDeclared  bool
		RequestedModel     core.ModelRef
		Model              string
		FallbackModel      string
		ThinkingLevel      string
		MaxOutputTokens    int
		EnableTools        bool
		ToolCallingKnown   bool
		ToolCallingAllowed bool
		ContextPlan        map[string]any
	}{
		SystemPrompt: request.SystemPrompt, Messages: request.Messages, ToolDefinitions: request.ToolDefinitions,
		AllowedTools: request.AllowedTools, ToolScopeDeclared: request.ToolScopeDeclared, RequestedModel: request.RequestedModel,
		Model: request.Model, FallbackModel: request.FallbackModel, ThinkingLevel: request.ThinkingLevel,
		MaxOutputTokens: request.MaxOutputTokens, EnableTools: request.EnableTools, ContextPlan: contextPlan,
		ToolCallingKnown: request.ToolCallingKnown, ToolCallingAllowed: request.ToolCallingAllowed,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
