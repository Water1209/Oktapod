package runtime

import (
	"context"
	"fmt"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// ToolPreparer performs only the family-specific, receipt-free preparation
// needed before dispatch. ToolRuntime remains the owner of decoding, policy,
// authorization, input identity, Receipt Begin, dispatch ordering, and result
// normalization.
type ToolPreparer interface {
	Owner() string
	Matches(string) bool
	Prepare(context.Context, core.ClaimedStep, core.RunContext, FrozenToolInvocation, core.PlannedTool) (preparedTool, *core.StepOutcome)
}

// ToolPreparerRegistry is deliberately fixed at construction. It is not a
// dynamic plugin surface and cannot be mutated by project or Provider data.
type ToolPreparerRegistry struct {
	preparers []ToolPreparer
}

func newToolPreparerRegistry(runtime ToolRuntime) ToolPreparerRegistry {
	return ToolPreparerRegistry{preparers: []ToolPreparer{
		SkillToolPreparer{runtime: runtime},
		WebToolPreparer{runtime: runtime},
		WorkspaceToolPreparer{runtime: runtime},
		DocumentToolPreparer{runtime: runtime},
		MCPToolPreparer{runtime: runtime},
	}}
}

func (registry ToolPreparerRegistry) Prepare(ctx context.Context, step core.ClaimedStep, run core.RunContext, invocation FrozenToolInvocation, planned core.PlannedTool) (preparedTool, *core.StepOutcome) {
	matches := registry.matching(invocation.Name)
	if len(matches) != 1 {
		reason := fmt.Sprintf("tool runtime owner count is %d for %q", len(matches), invocation.Name)
		failure := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "TOOL_RUNTIME_OWNER_INVALID", ResultSummary: "工具没有唯一的受信运行时 owner。", Output: map[string]any{"reason": reason, "retryable": false}}
		return preparedTool{}, &failure
	}
	return matches[0].Prepare(ctx, step, run, invocation, planned)
}

func (registry ToolPreparerRegistry) matching(name string) []ToolPreparer {
	matches := make([]ToolPreparer, 0, 1)
	for _, preparer := range registry.preparers {
		if preparer != nil && preparer.Matches(name) {
			matches = append(matches, preparer)
		}
	}
	return matches
}

type SkillToolPreparer struct{ runtime ToolRuntime }

func (SkillToolPreparer) Owner() string            { return "durable:skill" }
func (SkillToolPreparer) Matches(name string) bool { return name == core.ToolSkillInvoke }
func (preparer SkillToolPreparer) Prepare(_ context.Context, _ core.ClaimedStep, run core.RunContext, invocation FrozenToolInvocation, _ core.PlannedTool) (preparedTool, *core.StepOutcome) {
	return preparer.runtime.preflightSkill(run, invocation)
}

type WebToolPreparer struct{ runtime ToolRuntime }

func (WebToolPreparer) Owner() string { return "durable:web" }
func (WebToolPreparer) Matches(name string) bool {
	return name == core.ToolWebSearch || name == core.ToolWebFetch
}
func (preparer WebToolPreparer) Prepare(_ context.Context, _ core.ClaimedStep, run core.RunContext, invocation FrozenToolInvocation, _ core.PlannedTool) (preparedTool, *core.StepOutcome) {
	return preparer.runtime.preflightWeb(run, invocation)
}

type WorkspaceToolPreparer struct{ runtime ToolRuntime }

func (WorkspaceToolPreparer) Owner() string            { return "durable:workspace" }
func (WorkspaceToolPreparer) Matches(name string) bool { return isWorkspaceTool(name) }
func (preparer WorkspaceToolPreparer) Prepare(_ context.Context, _ core.ClaimedStep, run core.RunContext, invocation FrozenToolInvocation, planned core.PlannedTool) (preparedTool, *core.StepOutcome) {
	return preparer.runtime.preflightWorkspace(run, invocation, planned)
}

type DocumentToolPreparer struct{ runtime ToolRuntime }

func (DocumentToolPreparer) Owner() string            { return "durable:document" }
func (DocumentToolPreparer) Matches(name string) bool { return isDocumentTool(name) }
func (preparer DocumentToolPreparer) Prepare(_ context.Context, _ core.ClaimedStep, run core.RunContext, invocation FrozenToolInvocation, _ core.PlannedTool) (preparedTool, *core.StepOutcome) {
	return preparer.runtime.preflightDocument(run, invocation)
}

type MCPToolPreparer struct{ runtime ToolRuntime }

func (MCPToolPreparer) Owner() string            { return "durable:mcp" }
func (MCPToolPreparer) Matches(name string) bool { return isMCPTool(name) }
func (preparer MCPToolPreparer) Prepare(ctx context.Context, _ core.ClaimedStep, run core.RunContext, invocation FrozenToolInvocation, _ core.PlannedTool) (preparedTool, *core.StepOutcome) {
	return preparer.runtime.preflightMCP(ctx, run, invocation)
}

func isControlHandlerTool(name string) bool {
	switch name {
	case core.ToolReplacePlan, core.ToolAdvancePlan, core.ToolUpdatePlan, core.ToolRequestInput, core.ToolRequestPermission:
		return true
	default:
		return false
	}
}

func validateBuiltinToolOwners(registry ToolPreparerRegistry) error {
	specs := append(core.BuiltinToolSpecs(), core.ToolSpec{Name: core.ToolSkillInvoke})
	for _, spec := range specs {
		owners := len(registry.matching(spec.Name))
		if isControlHandlerTool(spec.Name) {
			owners++
		}
		if owners != 1 {
			return fmt.Errorf("model-visible tool %q has %d runtime owners", spec.Name, owners)
		}
	}
	return nil
}
