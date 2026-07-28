package runtime

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestMCPToolContextPlanPrunesIrrelevantSchemasAndReportsSavings(t *testing.T) {
	snapshot := `{"mcp_capabilities":[{"id":"cap-1","manifest_json":"{}","tools":[{"name":"search_docs","description":"Search project documentation","inputSchema":{"type":"object"}},{"name":"send_email","description":"Send an external email","inputSchema":{"type":"object"}}]}]}`
	definitions, plan := mcpToolDefinitionsForPrompt(snapshot, "search documentation")
	if len(definitions) != 1 || definitions[0].Name != "mcp__cap-1__search_docs" {
		t.Fatalf("definitions=%#v", definitions)
	}
	if plan["tool_definitions_before"] != 2 || plan["tool_definitions_after"] != 1 {
		t.Fatalf("plan=%#v", plan)
	}
	if saved, ok := plan["estimated_tokens_saved"].(int64); !ok || saved <= 0 {
		t.Fatalf("expected positive token savings, plan=%#v", plan)
	}
}

func TestMCPToolContextPlanRetainsBoundedFallbackWhenNoSignal(t *testing.T) {
	snapshot := `{"mcp_capabilities":[{"id":"cap-1","manifest_json":"{}","tools":[{"name":"one","description":"one","inputSchema":{"type":"object"}},{"name":"two","description":"two","inputSchema":{"type":"object"}}]}]}`
	definitions, plan := mcpToolDefinitionsForPrompt(snapshot, "")
	if len(definitions) != 2 || plan["tool_definitions_after"] != 2 {
		t.Fatalf("definitions=%#v plan=%#v", definitions, plan)
	}
}

func TestSkillContextLoadsRelevantInstructionBodyLazily(t *testing.T) {
	snapshot := `{"skill_capabilities":[{"id":"skill-1","name":"release-check","version":"1.0.0","content_hash":"sha256:x","manifest_json":"{\"description\":\"release checklist\",\"instructions\":\"Run the release checklist and report evidence.\"}"},{"id":"skill-2","name":"finance","version":"1.0.0","content_hash":"sha256:y","manifest_json":"{\"description\":\"budget planning\",\"instructions\":\"Plan a budget.\"}"}]}`
	context, plan := skillContextForPrompt(snapshot, "please run the release checklist")
	if context == "" || !strings.Contains(context, "Run the release checklist") {
		t.Fatalf("skill body was not lazily loaded: %q", context)
	}
	if plan["loaded"] != 1 || plan["available"] != 2 {
		t.Fatalf("unexpected skill plan: %#v", plan)
	}
}

func TestSkillContextMatchesChineseTriggersAndListsMetadataOnlyCatalog(t *testing.T) {
	snapshot := `{"skill_capabilities":[{"id":"skill-1","name":"release-check","version":"1.0.0","content_hash":"sha256:x","manifest_json":"{\"description\":\"发布前验证\",\"triggers\":[\"发布检查\"],\"allowed_tools\":[\"web_search\"],\"instructions\":\"运行发布检查并引用证据。\"}"},{"id":"skill-2","name":"finance","version":"1.0.0","content_hash":"sha256:y","manifest_json":"{\"description\":\"预算规划\",\"instructions\":\"制定预算。\"}"}]}`
	context, plan := skillContextForPrompt(snapshot, "请帮我做一次发布检查并确认最新版本")
	if !strings.Contains(context, "运行发布检查") || !strings.Contains(context, "web_search") || plan["loaded"] != 1 {
		t.Fatalf("context=%q plan=%#v", context, plan)
	}
	catalog, catalogPlan := skillCatalogForPrompt(snapshot)
	if !strings.Contains(catalog, "release-check@1.0.0") || !strings.Contains(catalog, "finance@1.0.0") || strings.Contains(catalog, "运行发布检查") || catalogPlan["listed"] != 2 {
		t.Fatalf("catalog=%q plan=%#v", catalog, catalogPlan)
	}
	definitions := filterToolDefinitionsBySkill([]ToolDefinition{{Name: core.ToolWebSearch}, {Name: core.ToolWorkspaceWriteFile}}, plan)
	if len(definitions) != 1 || definitions[0].Name != core.ToolWebSearch {
		t.Fatalf("skill tool scope leaked definitions=%#v plan=%#v", definitions, plan)
	}
}

func TestPlanExecuteTurnCannotRepublishPlanBeforeProducingEvidence(t *testing.T) {
	definitions := []ToolDefinition{
		{Name: core.ToolUpdatePlan},
		{Name: core.ToolWebSearch},
		{Name: core.ToolRequestInput},
	}
	filtered := filterOutToolDefinition(definitions, core.ToolUpdatePlan)
	if len(filtered) != 2 || filtered[0].Name != core.ToolWebSearch || filtered[1].Name != core.ToolRequestInput {
		t.Fatalf("plan execute definitions=%#v", filtered)
	}
}

func TestWebSearchDropsOnlyEmptyOptionalDomainsBeforePolicyFreeze(t *testing.T) {
	calls := []core.ToolCall{{
		Name: core.ToolWebSearch,
		Arguments: map[string]any{
			"query":           "physical ai robotics",
			"allowed_domains": []any{"", "nvidia.com", "  "},
			"blocked_domains": []any{},
		},
	}}
	if repairs := normalizeSafeToolArguments(calls); repairs != 1 {
		t.Fatalf("repairs=%d calls=%#v", repairs, calls)
	}
	allowed, ok := calls[0].Arguments["allowed_domains"].([]any)
	if !ok || len(allowed) != 1 || allowed[0] != "nvidia.com" {
		t.Fatalf("allowed_domains=%#v", calls[0].Arguments["allowed_domains"])
	}
	if _, exists := calls[0].Arguments["blocked_domains"]; !exists {
		t.Fatal("valid empty blocked_domains was unexpectedly rewritten")
	}
	invalid := []core.ToolCall{{
		Name:      core.ToolWebSearch,
		Arguments: map[string]any{"query": "physical ai", "allowed_domains": []any{"https://example.com"}},
	}}
	if repairs := normalizeSafeToolArguments(invalid); repairs != 0 {
		t.Fatalf("non-empty invalid domain was repaired: %#v", invalid)
	}
	if core.PlanTool(invalid[0]).Decision != core.PolicyDeny {
		t.Fatal("non-empty invalid domain must still fail closed")
	}
}

func TestScenePolicyDeniesWorkspaceCommandsOutsideCode(t *testing.T) {
	if sceneAllowsWorkspaceCommand(`{"scene":"work","scene_policy":{"workspace_command":false}}`) {
		t.Fatal("work scene must deny workspace commands")
	}
	if !sceneAllowsWorkspaceCommand(`{"scene":"code","scene_policy":{"workspace_command":true}}`) {
		t.Fatal("code scene must allow workspace commands")
	}
	if !sceneAllowsWorkspaceCommand(`{"mode":"single_agent"}`) {
		t.Fatal("snapshots without scene policy must remain compatible")
	}
}

func TestScenePolicyFiltersWorkspaceCommandBeforeModelPlanning(t *testing.T) {
	allowed, declared, plan := applySceneToolPolicy(`{"scene":"work","scene_policy":{"workspace_command":false}}`, nil, false)
	if !declared || len(allowed) != 10 || allowed[0] != core.ToolUpdatePlan || allowed[1] != core.ToolRequestInput || allowed[2] != core.ToolRequestPermission || allowed[9] != core.ToolWorkspaceEditFile {
		t.Fatalf("work scene scope declared=%v allowed=%#v", declared, allowed)
	}
	if plan["workspace_command"] != false {
		t.Fatalf("work scene plan=%#v", plan)
	}

	allowed, declared, _ = applySceneToolPolicy(`{"scene":"work","scene_policy":{"workspace_command":false}}`, []string{core.ToolWebSearch, core.ToolWorkspaceRunCommand}, true)
	if !declared || len(allowed) != 1 || allowed[0] != core.ToolWebSearch {
		t.Fatalf("declared skill scope leaked command: declared=%v allowed=%#v", declared, allowed)
	}

	allowed, declared, _ = applySceneToolPolicy(`{"scene":"code","scene_policy":{"workspace_command":true}}`, nil, false)
	if declared || allowed != nil {
		t.Fatalf("code scene unexpectedly restricted tools: declared=%v allowed=%#v", declared, allowed)
	}
}

func TestFullAccessIsExplicitAndTimeBound(t *testing.T) {
	active := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339Nano)
	if !core.FullAccessActive(`{"permission_mode":"full_access","full_access_expires_at":"`+active+`"}`, time.Now().UTC()) {
		t.Fatal("active full access should be honored")
	}
	expired := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if core.FullAccessActive(`{"permission_mode":"full_access","full_access_expires_at":"`+expired+`"}`, time.Now().UTC()) {
		t.Fatal("expired full access must not be honored")
	}
	if core.FullAccessActive(`{"permission_mode":"full_access"}`, time.Now().UTC()) {
		t.Fatal("missing expiry must fail closed")
	}
}

func TestSelectedContextLoadsAuthorizedTextWithBoundedPlan(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/oktapod.db", t.TempDir()+"/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := core.Actor{ID: "00000000-0000-7000-0000-000000000301", DisplayName: "Context User"}
	org := core.Organization{ID: "00000000-0000-7000-0000-000000000302", Name: "Context Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Context project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := service.CreateMemory(ctx, project.ID, core.CreateMemoryInput{Kind: "manual", Title: "Context memory", Content: "Use deterministic validation.", Visibility: core.VisibilityPrivate}, "runtime-context-memory")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := service.UploadAsset(ctx, project.ID, core.UploadAssetInput{Name: "context.txt", Visibility: core.VisibilityPrivate, MediaType: "text/plain"}, strings.NewReader("authorized context"))
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "Use selected context", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate, ContextAssetIDs: []string{asset.ID}, ContextMemoryIDs: []string{memory.ID}}, "runtime-context-task", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRunContext(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker("context-worker", store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	content, plan := worker.contextForPrompt(ctx, run)
	if !strings.Contains(content, "authorized context") || !strings.Contains(content, "Use deterministic validation") {
		t.Fatalf("selected context content=%q plan=%#v", content, plan)
	}
	if plan["loaded_assets"] != 1 || plan["loaded_memories"] != 1 || plan["estimated_tokens"].(int) <= 0 {
		t.Fatalf("selected context plan=%#v", plan)
	}
}
