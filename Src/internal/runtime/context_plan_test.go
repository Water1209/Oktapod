package runtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestGrantedWorkspaceCommandRemovesDuplicatePermissionTool(t *testing.T) {
	definitions := excludeToolDefinition([]ToolDefinition{{Name: core.ToolRequestPermission}, {Name: core.ToolWorkspaceRunCommand}}, core.ToolRequestPermission)
	if len(definitions) != 1 || definitions[0].Name != core.ToolWorkspaceRunCommand {
		t.Fatalf("definitions=%#v", definitions)
	}
	allowed := excludeToolName([]string{core.ToolRequestPermission, core.ToolWorkspaceRunCommand}, core.ToolRequestPermission)
	if len(allowed) != 1 || allowed[0] != core.ToolWorkspaceRunCommand {
		t.Fatalf("allowed=%#v", allowed)
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

func TestSkillContextDoesNotMatchGenericWordsInsideInstructionBodies(t *testing.T) {
	snapshot := `{"skill_capabilities":[{"id":"skill-frontend","name":"frontend-design","version":"1.0.0","content_hash":"sha256:x","manifest_json":"{\"description\":\"distinctive frontend design\",\"instructions\":\"Create a polished webpage and verify files.\"}"},{"id":"skill-research","name":"researchpilot","version":"1.0.0","content_hash":"sha256:y","manifest_json":"{\"description\":\"deep research workflow\",\"instructions\":\"Use this skill to create files and verify delivery.\"}"}]}`
	context, plan := skillContextForPrompt(snapshot, "use the frontend-design skill to create a webpage")
	if !strings.Contains(context, "frontend-design@1.0.0") || strings.Contains(context, "researchpilot@1.0.0") {
		t.Fatalf("context=%q plan=%#v", context, plan)
	}
	if plan["loaded"] != 1 {
		t.Fatalf("plan=%#v", plan)
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

func TestBuiltInSkillCreatorLoadsWithoutInstalledCapability(t *testing.T) {
	context, plan := skillContextForPrompt(`{}`, "请使用内置 Skill Creator 创建一个发布检查 Skill")
	if !strings.Contains(context, "exactly one `SKILL.md`") || !strings.Contains(context, "Do not install or enable it silently") {
		t.Fatalf("context=%q", context)
	}
	if plan["available"] != 1 || plan["loaded"] != 1 || plan["tool_scope_declared"] != false {
		t.Fatalf("plan=%#v", plan)
	}
}

func TestBuiltInSkillCreatorDoesNotLoadForOrdinarySkillUse(t *testing.T) {
	context, plan := skillContextForPrompt(`{}`, "使用发布检查 skill 执行验证")
	if context != "" || plan["loaded"] != 0 {
		t.Fatalf("context=%q plan=%#v", context, plan)
	}
}

func TestStrictSkillCapabilityConstraintUsesFrozenRunTools(t *testing.T) {
	snapshot := `{"skill_capabilities":[{"id":"skill-1","name":"release-check","version":"1.0.0","content_hash":"sha256:x","manifest_json":"{\"description\":\"release\",\"instructions\":\"check\",\"required-capabilities\":[\"repository.read\",\"external.ticket.write\"],\"preferred-tools\":[\"grep\"],\"fallback-policy\":\"strict\"}"}]}`
	_, plan := skillContextForIDs(snapshot, []string{"skill-1"}, "")
	missing := missingStrictSkillCapabilities(snapshot, plan)
	if len(missing) != 1 || missing[0] != "external.ticket.write" {
		t.Fatalf("missing=%v, want external.ticket.write only", missing)
	}
}

func TestExplicitSkillInvocationPreservesLongWorkflowDeliveryGates(t *testing.T) {
	t.Parallel()
	body := strings.Repeat("阶段说明。", 2500) + "最终 QA 与交付门禁"
	manifest, err := json.Marshal(map[string]any{"description": "deep research", "instructions": body})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(map[string]any{"skill_capabilities": []map[string]any{{"id": "skill-research", "name": "researchpilot", "version": "1.0.0", "content_hash": "sha256:x", "manifest_json": string(manifest)}}})
	if err != nil {
		t.Fatal(err)
	}
	context, plan := skillContextForIDs(string(snapshot), []string{"skill-research"}, "物理 AI")
	if !strings.Contains(context, "最终 QA 与交付门禁") || plan["loaded"] != 1 {
		t.Fatalf("explicit skill body was truncated before delivery gates: plan=%#v context_length=%d", plan, len(context))
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

func TestScenePolicyOnlyRestrictsHistoricalSnapshots(t *testing.T) {
	if sceneAllowsWorkspaceCommand(`{"scene":"work","scene_policy":{"workspace_command":false}}`) {
		t.Fatal("historical work snapshot must preserve its frozen command restriction")
	}
	if !sceneAllowsWorkspaceCommand(`{"schema_version":2,"scene":"work"}`) {
		t.Fatal("new work snapshot must expose governed workspace commands")
	}
	if !sceneAllowsWorkspaceCommand(`{"scene":"code","scene_policy":{"workspace_command":true}}`) {
		t.Fatal("code scene must allow workspace commands")
	}
	if !sceneAllowsWorkspaceCommand(`{"mode":"single_agent"}`) {
		t.Fatal("snapshots without scene policy must remain compatible")
	}
	if !sceneAllowsWorkspaceCommand(`{"scene":"work","permission_mode":"full_access","scene_policy":{"workspace_command":false}}`) {
		t.Fatal("full access must expose workspace commands independently of the scene hint")
	}
}

func TestScenePolicyFiltersWorkspaceCommandBeforeModelPlanning(t *testing.T) {
	snapshot := `{"scene":"work","scene_policy":{"workspace_command":false},"mcp_capabilities":[{"id":"cap-1","name":"Tavily","tools":[{"name":"tavily_search"},{"name":"tavily_extract"}]}]}`
	allowed, declared, plan := applySceneToolPolicy(snapshot, nil, false)
	if !declared || len(allowed) != 12 || allowed[0] != core.ToolUpdatePlan || allowed[1] != core.ToolRequestInput || allowed[2] != core.ToolRequestPermission || allowed[9] != core.ToolWorkspaceEditFile || allowed[10] != "mcp__cap-1__tavily_search" || allowed[11] != "mcp__cap-1__tavily_extract" {
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

	allowed, declared, plan = applySceneToolPolicy(`{"scene":"work","permission_mode":"full_access","scene_policy":{"workspace_command":false}}`, nil, false)
	if declared || allowed != nil || plan["workspace_command"] != true {
		t.Fatalf("full access unexpectedly restricted tools: declared=%v allowed=%#v plan=%#v", declared, allowed, plan)
	}

	allowed, declared, plan = applySceneToolPolicy(`{"schema_version":2,"scene":"work","permission_mode":"ask"}`, []string{core.ToolWorkspaceRunCommand}, true)
	if !declared || len(allowed) != 1 || allowed[0] != core.ToolWorkspaceRunCommand || plan["source"] != "execution_policy" {
		t.Fatalf("new work snapshot was scene-filtered: declared=%v allowed=%#v plan=%#v", declared, allowed, plan)
	}
}

func TestExplicitMCPProviderChoiceOverridesBuiltinSearch(t *testing.T) {
	snapshot := `{"mcp_capabilities":[{"id":"cap-1","name":"Tavily","tools":[{"name":"tavily_search"}]}]}`
	if got, id := explicitMCPProviderFromPrompt(snapshot, "请只使用 Tavily MCP 搜索"); got != "Tavily" || id != "cap-1" {
		t.Fatalf("preferred provider=%q id=%q", got, id)
	}
	if got, id := explicitMCPProviderFromPrompt(snapshot, "搜索最新资讯"); got != "" || id != "" {
		t.Fatalf("unexpected preferred provider=%q id=%q", got, id)
	}
	filtered := filterToolNamesForExplicitMCP([]string{core.ToolUpdatePlan, core.ToolWebSearch, core.ToolWorkspaceWriteFile, core.ToolWorkspaceReadFile, core.ToolGitDiff, "mcp__cap-1__tavily_search", "mcp__cap-2__other"}, "cap-1")
	if len(filtered) != 5 || filtered[0] != core.ToolUpdatePlan || filtered[1] != core.ToolWorkspaceWriteFile || filtered[2] != core.ToolWorkspaceReadFile || filtered[3] != core.ToolGitDiff || filtered[4] != "mcp__cap-1__tavily_search" {
		t.Fatalf("filtered=%#v", filtered)
	}
	definitions := filterToolsForExplicitMCP([]ToolDefinition{{Name: core.ToolRequestInput}, {Name: core.ToolWebSearch}, {Name: core.ToolWorkspaceWriteFile}, {Name: core.ToolWorkspaceListFiles}, {Name: "mcp__cap-1__tavily_search"}, {Name: "mcp__cap-2__other"}}, "cap-1")
	if len(definitions) != 4 || definitions[0].Name != core.ToolRequestInput || definitions[1].Name != core.ToolWorkspaceWriteFile || definitions[2].Name != core.ToolWorkspaceListFiles || definitions[3].Name != "mcp__cap-1__tavily_search" {
		t.Fatalf("definitions=%#v", definitions)
	}
}

func TestExplicitMCPProviderSuppressesHeuristicSkillScope(t *testing.T) {
	snapshot := `{"schema_version":2,"plan_protocol_version":"transition_v2","mcp_capabilities":[{"id":"playwright","name":"@playwright/mcp","tools":[{"name":"browser_navigate","description":"Navigate to a URL","inputSchema":{"type":"object"}}]}],"skill_capabilities":[{"id":"pdf","name":"pdf","version":"1.0.0","content_hash":"sha256:x","manifest_json":"{\"description\":\"读取和编辑 PDF，并完成页面渲染校验\",\"allowed_tools\":[\"document_open\"],\"instructions\":\"处理 PDF。\"}"}]}`
	catalog, failure := (ToolCatalogBuilder{}).Build(core.ClaimedStep{}, core.RunContext{Mode: core.ModeSingleAgent, ConfigSnapshotJSON: snapshot}, "必须使用 @playwright/mcp 打开页面", nil, map[string]any{})
	if failure != nil {
		t.Fatalf("unexpected failure: %#v", failure)
	}
	found := false
	for _, definition := range catalog.Definitions {
		if definition.Name == "mcp__playwright__browser_navigate" {
			found = true
			if !strings.Contains(definition.Description, "@playwright/mcp") || !strings.Contains(definition.Description, "页面快照") {
				t.Fatalf("Playwright tool description lacks provider and safe workflow guidance: %q", definition.Description)
			}
		}
	}
	if !found {
		t.Fatalf("explicit MCP tool was removed by heuristic Skill scope: %#v", catalog.Definitions)
	}
	skills, _ := catalog.ContextPlan["skills"].(map[string]any)
	if skills["strategy"] != "suppressed_by_explicit_mcp_v1" || skills["loaded"] != 0 || catalog.ToolScopeDeclared {
		t.Fatalf("skills=%#v scope_declared=%v allowed=%#v", skills, catalog.ToolScopeDeclared, catalog.AllowedTools)
	}
}

func TestFullAccessIsExplicitAndManuallyControlled(t *testing.T) {
	if !core.FullAccessActive(`{"permission_mode":"full_access"}`, time.Now().UTC()) {
		t.Fatal("explicit full access should be honored without a timer")
	}
	if core.FullAccessActive(`{"permission_mode":"ask"}`, time.Now().UTC()) {
		t.Fatal("ask mode must not enable full access")
	}
	if core.FullAccessActive(`{}`, time.Now().UTC()) {
		t.Fatal("missing permission mode must fail closed")
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
	if _, err := service.UpdateMemory(ctx, memory.ID, core.UpdateMemoryInput{Title: "Changed memory", Content: "This later edit must not enter the Run.", Visibility: core.VisibilityPrivate}, "runtime-context-memory-update"); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteMemory(ctx, memory.ID, "runtime-context-memory-delete", "runtime-context-memory-delete"); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRunContext(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker("context-worker", store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	content, plan := worker.contextForPrompt(ctx, run)
	if !strings.Contains(content, "authorized context") || !strings.Contains(content, "Use deterministic validation") || strings.Contains(content, "later edit") {
		t.Fatalf("selected context content=%q plan=%#v", content, plan)
	}
	if plan["loaded_assets"] != 1 || plan["loaded_memories"] != 1 || plan["estimated_tokens"].(int) <= 0 || plan["memory_context_source"] != "frozen_snapshot" || plan["legacy_memory_context"] != false {
		t.Fatalf("selected context plan=%#v", plan)
	}
}

func TestHistoricalSelectedMemoryUsesMarkedLiveFallback(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/oktapod.db", t.TempDir()+"/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := core.Actor{ID: "00000000-0000-7000-0000-000000000303", DisplayName: "Legacy Context User"}
	org := core.Organization{ID: "00000000-0000-7000-0000-000000000304", Name: "Legacy Context Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Legacy context project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := service.CreateMemory(ctx, project.ID, core.CreateMemoryInput{Kind: "manual", Title: "Legacy memory", Content: "Original content", Visibility: core.VisibilityPrivate}, "legacy-context-memory")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "Use legacy context", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate, ContextMemoryIDs: []string{memory.ID}}, "legacy-context-task", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRunContext(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var legacyConfig map[string]any
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &legacyConfig); err != nil {
		t.Fatal(err)
	}
	delete(legacyConfig, "context_memory_snapshots")
	legacyJSON, err := json.Marshal(legacyConfig)
	if err != nil {
		t.Fatal(err)
	}
	run.ConfigSnapshotJSON = string(legacyJSON)
	if _, err := service.UpdateMemory(ctx, memory.ID, core.UpdateMemoryInput{Title: "Legacy memory changed", Content: "Live fallback content", Visibility: core.VisibilityPrivate}, "legacy-context-memory-update"); err != nil {
		t.Fatal(err)
	}
	worker := NewWorker("legacy-context-worker", store, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	content, plan := worker.contextForPrompt(ctx, run)
	if !strings.Contains(content, "Live fallback content") || plan["memory_context_source"] != "legacy_live_ids" || plan["legacy_memory_context"] != true {
		t.Fatalf("legacy selected context content=%q plan=%#v", content, plan)
	}
}

func TestSelectedMemorySnapshotBoundsAggregateUTF8Payload(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/oktapod.db", t.TempDir()+"/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := core.Actor{ID: "00000000-0000-7000-0000-000000000305", DisplayName: "Bounded Context User"}
	org := core.Organization{ID: "00000000-0000-7000-0000-000000000306", Name: "Bounded Context Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Bounded context project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	largeContent := strings.Repeat("界", application.MaxContextMemorySnapshotBytes)
	memory, err := service.CreateMemory(ctx, project.ID, core.CreateMemoryInput{Kind: "manual", Title: "Large memory", Content: largeContent, Visibility: core.VisibilityPrivate}, "bounded-context-memory")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "Use bounded context", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate, ContextMemoryIDs: []string{memory.ID}}, "bounded-context-task", "")
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRunContext(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Memories []application.ContextMemorySnapshot `json:"context_memory_snapshots"`
	}
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Memories) != 1 || len([]byte(snapshot.Memories[0].Content)) > application.MaxContextMemorySnapshotBytes || !utf8.ValidString(snapshot.Memories[0].Content) {
		t.Fatalf("bounded memory snapshots=%#v", snapshot.Memories)
	}
}
