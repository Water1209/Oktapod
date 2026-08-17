package runtime

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestMainAgentModelRequestContractMatchesBuiltinCatalog(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/oktapod.db", t.TempDir()+"/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	actor := core.Actor{ID: "00000000-0000-7000-0000-000000000501", DisplayName: "Runtime Contract User"}
	org := core.Organization{ID: "00000000-0000-7000-0000-000000000502", Name: "Runtime Contract Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Runtime contract", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{
		Goal:       "Inspect the project and report the result",
		Mode:       core.ModeSingleAgent,
		Visibility: core.VisibilityPrivate,
	}, "runtime-contract-task", ""); err != nil {
		t.Fatal(err)
	}

	model := &scriptedSkillModel{results: []core.ModelResult{{
		CallID: "runtime-contract-model-call", Provider: "test", Model: "test-model",
		Content: "No changes were required.", StopReason: core.StopReasonStop,
	}}}
	worker := NewWorker("runtime-contract-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if worked, runErr := worker.RunOnce(ctx); runErr != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, runErr)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model requests=%d", len(model.requests))
	}

	request := model.requests[0]
	wantNames := make([]string, 0, len(core.BuiltinToolSpecs()))
	for _, spec := range core.BuiltinToolSpecs() {
		if spec.Name == core.ToolRequestPermission {
			continue
		}
		wantNames = append(wantNames, spec.Name)
	}
	gotNames := make([]string, 0, len(request.ToolDefinitions))
	for _, definition := range request.ToolDefinitions {
		gotNames = append(gotNames, definition.Name)
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("tool definitions=%#v, want %#v", gotNames, wantNames)
	}
	if request.ToolScopeDeclared || len(request.AllowedTools) != 0 {
		t.Fatalf("default tool scope declared=%v allowed=%#v", request.ToolScopeDeclared, request.AllowedTools)
	}
	if slices.Contains(gotNames, core.ToolRequestPermission) {
		t.Fatalf("new Run advertised legacy permission tool: %#v", gotNames)
	}
	if request.ContextPlan["builtin_tool_definitions"] != len(core.BuiltinToolSpecs()) ||
		request.ContextPlan["tool_definitions_after_skill_scope"] != len(core.BuiltinToolSpecs()) {
		t.Fatalf("context plan=%#v", request.ContextPlan)
	}
	for _, name := range wantNames {
		if !strings.Contains(request.SystemPrompt, "- `"+name+"`") {
			t.Fatalf("system prompt omitted tool %q", name)
		}
	}
	if !strings.Contains(request.SystemPrompt, "# Available tools") || !strings.Contains(request.SystemPrompt, "# Stage instructions") {
		t.Fatalf("system prompt lost catalog or stage sections: %q", request.SystemPrompt)
	}
}
