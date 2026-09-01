package runtime

import (
	"context"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type duplicateWorkspacePreparer struct{}

func (duplicateWorkspacePreparer) Owner() string { return "duplicate:test" }
func (duplicateWorkspacePreparer) Matches(name string) bool {
	return name == core.ToolWorkspaceReadFile
}
func (duplicateWorkspacePreparer) Prepare(context.Context, core.ClaimedStep, core.RunContext, FrozenToolInvocation, core.PlannedTool) (preparedTool, *core.StepOutcome) {
	return preparedTool{}, nil
}

func TestEveryModelVisibleBuiltinHasExactlyOneRuntimeOwner(t *testing.T) {
	registry := newToolPreparerRegistry(ToolRuntime{})
	if err := validateBuiltinToolOwners(registry); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{core.ToolSkillInvoke, "mcp__capability__search"} {
		if matches := registry.matching(name); len(matches) != 1 {
			t.Fatalf("tool %q owners=%d", name, len(matches))
		}
	}
	if matches := registry.matching("unknown_tool"); len(matches) != 0 {
		t.Fatalf("unknown tool owners=%d", len(matches))
	}
}

func TestDuplicateRuntimeOwnerFailsValidation(t *testing.T) {
	registry := newToolPreparerRegistry(ToolRuntime{})
	registry.preparers = append(registry.preparers, duplicateWorkspacePreparer{})
	if err := validateBuiltinToolOwners(registry); err == nil {
		t.Fatal("duplicate runtime owner passed startup validation")
	}
}

func TestControlToolsNeverEnterDurableToolRuntime(t *testing.T) {
	registry := newToolPreparerRegistry(ToolRuntime{})
	for _, name := range []string{core.ToolReplacePlan, core.ToolAdvancePlan, core.ToolUpdatePlan, core.ToolRequestInput, core.ToolRequestPermission} {
		if !isControlHandlerTool(name) || len(registry.matching(name)) != 0 || isToolRuntimeStep(name) {
			t.Fatalf("control tool %q has ambiguous ownership", name)
		}
	}
}
