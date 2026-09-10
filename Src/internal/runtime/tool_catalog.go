package runtime

import (
	"encoding/json"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// ToolCatalog is one immutable view used for both provider definitions and
// AllowedTools. No later phase may independently rebuild either list.
type ToolCatalog struct {
	Definitions                 []ToolDefinition
	AllowedTools                []string
	ToolScopeDeclared           bool
	ContextPlan                 map[string]any
	PromptSections              []PromptSection
	SearchBudget                core.SearchCallBudget
	PriorSearchCalls            int
	ContinuationInstructionTail string
}

type ToolCatalogBuilder struct{}

func (ToolCatalogBuilder) Build(step core.ClaimedStep, run core.RunContext, userPrompt string, continuation []map[string]any, selectedContextPlan map[string]any) (ToolCatalog, *core.StepOutcome) {
	mcpDefinitions, contextPlan := mcpToolDefinitionsForPrompt(run.ConfigSnapshotJSON, userPrompt)
	preferredMCP, preferredMCPID := explicitMCPProviderFromPrompt(run.ConfigSnapshotJSON, userPrompt)
	toolDefinitions := append(builtinToolDefinitions(), mcpDefinitions...)
	if !documentRuntimeSnapshotted(run.ConfigSnapshotJSON) {
		toolDefinitions = filterOutDocumentToolDefinitions(toolDefinitions)
	}
	if snapshotVersion(run.ConfigSnapshotJSON, "plan_protocol_version") == "transition_v2" {
		toolDefinitions = filterOutToolDefinition(toolDefinitions, core.ToolUpdatePlan)
		if !transitionPlanReplacementVisible(step.InputJSON) {
			toolDefinitions = filterOutToolDefinition(toolDefinitions, core.ToolReplacePlan)
		}
	} else {
		toolDefinitions = filterOutToolDefinition(toolDefinitions, core.ToolReplacePlan)
		toolDefinitions = filterOutToolDefinition(toolDefinitions, core.ToolAdvancePlan)
	}
	sections := make([]PromptSection, 0, 6)
	contextPlan["builtin_tool_definitions"] = len(core.BuiltinToolSpecs())
	contextPlan["selected_context"] = selectedContextPlan

	invokedSkillIDs, skillArgs := skillInvocationFromStep(step.InputJSON)
	skillCatalog, skillCatalogPlan := skillCatalogForPrompt(run.ConfigSnapshotJSON)
	if skillCatalog != "" {
		sections = append(sections, PromptSection{Name: "skill_catalog", Order: 1300, Text: "当前项目可用的 Skill 元数据（仅说明可用方法；正文仍按需加载）：\n" + skillCatalog})
	}
	contextPlan["skill_catalog"] = skillCatalogPlan
	var skillContext string
	var skillPlan map[string]any
	if len(invokedSkillIDs) > 0 {
		skillContext, skillPlan = skillContextForIDs(run.ConfigSnapshotJSON, invokedSkillIDs, skillArgs)
		if loaded, _ := skillPlan["loaded"].(int); loaded != len(invokedSkillIDs) {
			outcome := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SKILL_NOT_SNAPSHOTTED", ResultSummary: "显式调用的 Skill 不在当前 Run 的冻结快照中，已拒绝继续执行。", Output: map[string]any{"retryable": false, "context_plan": contextPlan}}
			return ToolCatalog{}, &outcome
		}
	} else if preferredMCP != "" {
		// An explicit provider choice is stronger than heuristic Skill matching.
		// Otherwise a generic metadata hit such as "页面" can auto-load a
		// document Skill and its narrow allowed_tools scope can silently remove
		// the MCP tools the user explicitly requested.
		skillContext, skillPlan = suppressedSkillContextForExplicitMCP(run.ConfigSnapshotJSON)
	} else {
		// Deterministic matching remains a compatibility fallback for models that
		// cannot reliably call tools. The auditable path is still skill_invoke.
		skillContext, skillPlan = skillContextForPrompt(run.ConfigSnapshotJSON, userPrompt)
	}
	if missing := missingStrictSkillCapabilities(run.ConfigSnapshotJSON, skillPlan); len(missing) > 0 {
		outcome := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SKILL_REQUIRED_CAPABILITY_UNAVAILABLE", ResultSummary: "Skill 要求的能力在当前 Run 冻结快照中不可用，请先配置并审核对应能力后重试。", Output: map[string]any{"retryable": false, "missing_capabilities": missing, "context_plan": contextPlan}}
		return ToolCatalog{}, &outcome
	}
	if skillContext != "" {
		sections = append(sections, PromptSection{Name: "skill_instructions", Order: 1400, Text: "已按需加载的 Skill 指令（仅可作为约束和方法参考，不得伪造执行）：\n" + skillContext})
	}
	if len(invokedSkillIDs) > 0 && snapshotVersion(run.ConfigSnapshotJSON, "plan_protocol_version") == "transition_v2" {
		var plan struct {
			Total    int    `json:"active_plan_total"`
			Items    []any  `json:"active_plan"`
			Revision string `json:"plan_revision_id"`
		}
		if json.Unmarshal([]byte(step.InputJSON), &plan) == nil && plan.Total == 0 && len(plan.Items) == 0 && plan.Revision == "" {
			sections = append(sections, PromptSection{Name: "skill_initial_plan", Order: 1450, Text: "Skill 已加载，但当前尚无持久化语义执行计划。Skill 的操作流程不等于执行计划；调用首批业务工具时，必须在同一响应中先提交 replace_plan 和该计划第一项所需的业务工具。不要调用 advance_plan；加载 Skill 不会自动创建计划。"})
		}
	}
	definitionsBeforeSkillScope := len(toolDefinitions)
	toolDefinitions = filterToolDefinitionsBySkill(toolDefinitions, skillPlan)
	continuationTail := ""
	if modelStage(step.InputJSON) == "plan_execute" {
		toolDefinitions = filterOutToolDefinition(toolDefinitions, core.ToolUpdatePlan)
		continuationTail = "本轮尚未产生新的执行证据，update_plan 暂不可用；请先执行当前计划项所需的工具，或在该项无需工具且目标已经完成时直接交付结果。"
	}
	if len(invokedSkillIDs) == 0 && skillCatalog != "" {
		toolDefinitions = append(toolDefinitions, skillInvokeToolDefinition())
		sections = append(sections, PromptSection{Name: "skill_invocation", Order: 1500, Text: "当某个 Skill 与用户目标匹配，或用户提到 /<skill-name> 时，必须先单独调用 skill_invoke；该次响应不得同时调用其他工具。Skill 加载后系统会恢复同一任务。"})
	}
	contextPlan["tool_definitions_before_skill_scope"] = definitionsBeforeSkillScope
	contextPlan["tool_definitions_after_skill_scope"] = len(toolDefinitions)
	contextPlan["skills"] = skillPlan

	if preferredMCP != "" {
		toolDefinitions = filterToolsForExplicitMCP(toolDefinitions, preferredMCPID)
		contextPlan["preferred_mcp_provider"] = preferredMCP
	}
	allowedTools, toolScopeDeclared := skillToolScope(skillPlan)
	if len(invokedSkillIDs) == 0 && skillCatalog != "" && toolScopeDeclared {
		allowedTools = append(allowedTools, core.ToolSkillInvoke)
	}
	allowedTools, toolScopeDeclared, scenePolicy := applySceneToolPolicy(run.ConfigSnapshotJSON, allowedTools, toolScopeDeclared)
	if sceneAllowsWorkspaceCommand(run.ConfigSnapshotJSON) {
		toolDefinitions = excludeToolDefinition(toolDefinitions, core.ToolRequestPermission)
		allowedTools = excludeToolName(allowedTools, core.ToolRequestPermission)
		sections = append(sections, PromptSection{Name: "workspace_command_authorization", Order: 1600, Text: "当前 Run 已获批工作区命令能力；workspace_run_command 与 workspace_shell 可直接使用。request_permission 已从工具集合移除，不得重复申请同一能力。"})
	}
	searchBudget := core.InferSearchCallBudget(run.Goal, run.Acceptance)
	priorSearchCalls := searchCallsUsedFromSnapshot(run.ConfigSnapshotJSON)
	searchCallsUsed := priorSearchCalls + succeededSearchCallsInHistory(continuation)
	if searchBudget.Enforced {
		contextPlan["search_budget"] = map[string]any{"limit": searchBudget.Limit, "used": searchCallsUsed}
		if searchCallsUsed >= searchBudget.Limit {
			toolDefinitions = excludeSearchToolDefinitions(toolDefinitions)
			allowedTools = excludeSearchToolNames(allowedTools)
			sections = append(sections, PromptSection{Name: "search_budget", Order: 1700, Text: "用户声明的联网预算已用完。所有 web_search、web_fetch 以及 search、research、crawl、extract、map、browse、discover 类 MCP 工具已从当前工具集合移除；只能使用已有证据和工作区工具完成剩余任务。"})
		}
	}
	if preferredMCP != "" {
		allowedTools = filterToolNamesForExplicitMCP(allowedTools, preferredMCPID)
	}
	contextPlan["scene_policy"] = scenePolicy
	// Providers enforce AllowedTools a second time. Derive that list from the
	// final scoped catalog so protocol controls survive both filtering layers.
	if toolScopeDeclared {
		allowedTools = make([]string, 0, len(toolDefinitions))
		for _, definition := range toolDefinitions {
			allowedTools = append(allowedTools, definition.Name)
		}
	}
	return ToolCatalog{
		Definitions: toolDefinitions, AllowedTools: allowedTools, ToolScopeDeclared: toolScopeDeclared,
		ContextPlan: contextPlan, PromptSections: sections, SearchBudget: searchBudget,
		PriorSearchCalls: priorSearchCalls, ContinuationInstructionTail: continuationTail,
	}, nil
}

// transitionPlanReplacementVisible keeps replace_plan limited to initial plan
// creation and explicit replanning stages. Once a durable semantic plan is
// bound, ordinary continuation must advance it instead of rewriting completed
// outcomes or expanding the user's scope.
func transitionPlanReplacementVisible(inputJSON string) bool {
	stage := modelStage(inputJSON)
	switch stage {
	case "plan", "steering_replan", "permission_replan", "recovery", "completion_recovery":
		return true
	}
	var input struct {
		PlanRevisionID  string `json:"plan_revision_id"`
		ActivePlanTotal int    `json:"active_plan_total"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		// The durable Store remains authoritative and will reject an illegal
		// replacement. Do not make a malformed compatibility input less capable.
		return true
	}
	return strings.TrimSpace(input.PlanRevisionID) == "" && input.ActivePlanTotal == 0
}
