package runtime

import (
	"context"
	"io"
	"log/slog"
	"reflect"
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
		if spec.Name == core.ToolRequestPermission || spec.Name == core.ToolUpdatePlan || isDocumentTool(spec.Name) {
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
		request.ContextPlan["tool_definitions_after_skill_scope"] != len(wantNames)+1 {
		t.Fatalf("context plan=%#v", request.ContextPlan)
	}
	if strings.Contains(request.SystemPrompt, "# Available tools") || !strings.Contains(request.SystemPrompt, "# Stage instructions") {
		t.Fatalf("system prompt must rely on provider schemas without duplicating the tool catalog: %q", request.SystemPrompt)
	}
	if request.ContextPlan["assembly_schema_version"] != assemblySchemaVersion || request.ContextPlan["assembly_hash"] != modelAssemblyHash(request) {
		t.Fatalf("assembly identity=%#v", request.ContextPlan)
	}
}

func TestAgentTurnAssemblyIsDeterministicAndHashesFinalRequest(t *testing.T) {
	assembler := NewAgentTurnAssembler(nil)
	step := core.ClaimedStep{ID: "assembly-step", InputJSON: `{"stage":"plan"}`}
	run := core.RunContext{
		RunID: "assembly-run", Goal: "Inspect the workspace", Acceptance: "Report verified evidence",
		Mode: core.ModeSingleAgent, ConfigSnapshotJSON: `{"schema_version":2,"scene":"code","permission_mode":"ask"}`,
	}
	first, failure := assembler.Assemble(context.Background(), step, run)
	if failure != nil {
		t.Fatalf("first assembly failed: %#v", failure)
	}
	second, failure := assembler.Assemble(context.Background(), step, run)
	if failure != nil {
		t.Fatalf("second assembly failed: %#v", failure)
	}
	if !reflect.DeepEqual(first.Request, second.Request) {
		t.Fatalf("same Run and Step assembled differently\nfirst=%#v\nsecond=%#v", first.Request, second.Request)
	}
	baseHash, _ := first.Request.ContextPlan["assembly_hash"].(string)
	if baseHash == "" || baseHash != modelAssemblyHash(first.Request) {
		t.Fatalf("invalid assembly hash %q", baseHash)
	}

	promptChanged := first.Request
	promptChanged.SystemPrompt += "\nchanged"
	if modelAssemblyHash(promptChanged) == baseHash {
		t.Fatal("system prompt change did not change assembly hash")
	}
	schemaChanged := first.Request
	schemaChanged.ToolDefinitions = append([]ToolDefinition(nil), first.Request.ToolDefinitions...)
	schemaChanged.ToolDefinitions[0].Parameters = map[string]any{"type": "object", "properties": map[string]any{"changed": map[string]any{"type": "boolean"}}}
	if modelAssemblyHash(schemaChanged) == baseHash {
		t.Fatal("tool schema change did not change assembly hash")
	}
	allowedChanged := first.Request
	allowedChanged.AllowedTools = append(append([]string(nil), first.Request.AllowedTools...), core.ToolWorkspaceReadFile)
	if modelAssemblyHash(allowedChanged) == baseHash {
		t.Fatal("allowed tools change did not change assembly hash")
	}
}
