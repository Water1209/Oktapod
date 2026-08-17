package runtime

import (
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestPromptAssemblerSeparatesSceneFromPermissionAndUsesFrozenInstructions(t *testing.T) {
	run := core.RunContext{
		RunID:              "run-123",
		Mode:               core.ModeSingleAgent,
		Goal:               "把附件转成 PDF",
		ConfigSnapshotJSON: `{"schema_version":2,"scene":"work","permission_mode":"ask","project_instructions":[{"path":"AGENTS.md","content_hash":"sha256:test","content":"- 交付前验证 PDF"}]}`,
	}
	system, _ := (PromptAssembler{}).Base(run)
	for _, expected := range []string{"# Work workflow profile", "Scripts, workspace commands, and Git", "# Workspace file lifecycle", ".oktapod/work/run-123/", "outputs/<task-slug>", "artifact_capture", "AGENTS.md", "交付前验证 PDF"} {
		if !strings.Contains(system, expected) {
			t.Fatalf("base prompt missing %q:\n%s", expected, system)
		}
	}
	if strings.Contains(system, "request_permission") {
		t.Fatalf("new prompt must not advertise scene permission escalation: %s", system)
	}
}

func TestPromptAssemblerAdvertisesOnlyActualToolDefinitions(t *testing.T) {
	prompt := (PromptAssembler{}).ToolCatalog([]ToolDefinition{{
		Name: core.ToolWorkspaceRunCommand, PromptHint: "Run one program.", Guidelines: []string{"Stay scoped."},
	}})
	if !strings.Contains(prompt, core.ToolWorkspaceRunCommand) || !strings.Contains(prompt, "Stay scoped.") {
		t.Fatalf("tool prompt=%s", prompt)
	}
	if strings.Contains(prompt, core.ToolRequestPermission) || strings.Contains(prompt, core.ToolWebSearch) {
		t.Fatalf("tool prompt leaked unavailable tools: %s", prompt)
	}
}
