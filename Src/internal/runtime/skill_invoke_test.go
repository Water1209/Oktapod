package runtime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type scriptedSkillModel struct {
	results  []core.ModelResult
	requests []ModelRequest
}

type successfulSkillSearch struct{}

func (successfulSkillSearch) Execute(_ context.Context, call core.ToolCall) core.ToolExecution {
	return core.ToolExecution{ToolCallID: call.ID, Status: core.StatusSucceeded, Output: map[string]any{"results": []any{map[string]any{"title": "Release evidence", "url": "https://example.test/release", "snippet": "verified"}}}}
}

func (m *scriptedSkillModel) Complete(_ context.Context, request ModelRequest) (core.ModelResult, error) {
	m.requests = append(m.requests, request)
	if len(m.results) == 0 {
		return core.ModelResult{}, nil
	}
	result := m.results[0]
	m.results = m.results[1:]
	return result, nil
}

func setupSkillProject(t *testing.T, manifest string) (*sqlite.Store, *application.Service, core.Project, core.Capability) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/oktapod.db", t.TempDir()+"/workspaces")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-0000-000000000401", DisplayName: "Skill User"}
	org := core.Organization{ID: "00000000-0000-7000-0000-000000000402", Name: "Skill Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Skill project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	skill, err := service.CreateCapability(ctx, project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "release-check", Version: "1.0.0", ManifestJSON: manifest}, "skill-create")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Status != core.CapabilityStatusEnabled {
		t.Fatalf("skill status=%s", skill.Status)
	}
	return store, service, project, skill
}

func TestModelSkillInvokeQueuesResumeAndAppliesToolScope(t *testing.T) {
	ctx := context.Background()
	store, service, project, skill := setupSkillProject(t, `{"description":"release verification","instructions":"Run the frozen release checklist and report evidence.","allowed_tools":["web_search"]}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "verify the release", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "skill-run", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{
		{CallID: "00000000-0000-7000-0000-000000000411", Provider: "test", Model: "test-model", ToolCalls: []core.ToolCall{{Name: core.ToolSkillInvoke, Arguments: map[string]any{"skill": "release-check", "args": "production"}}}, Usage: core.ModelUsage{InputTokens: 10, OutputTokens: 3}},
		{CallID: "00000000-0000-7000-0000-000000000412", Provider: "test", Model: "test-model", ToolCalls: []core.ToolCall{{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "latest production release"}}}, Usage: core.ModelUsage{InputTokens: 12, OutputTokens: 6}},
		{CallID: "00000000-0000-7000-0000-000000000413", Provider: "test", Model: "test-model", Content: "Release check completed from the loaded Skill and verified search evidence.", Usage: core.ModelUsage{InputTokens: 15, OutputTokens: 8}},
	}}
	worker := NewWorker("skill-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil))).WithWebSearchExecutor(successfulSkillSearch{})
	for index := 0; index < 5; index++ {
		worked, runErr := worker.RunOnce(ctx)
		if runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", index, worked, runErr)
		}
	}
	completed, err := service.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != core.StatusSucceeded || !strings.Contains(completed.ResultSummary, "loaded Skill") {
		t.Fatalf("task=%#v", completed)
	}
	if len(model.requests) != 3 {
		t.Fatalf("model requests=%d", len(model.requests))
	}
	initialHasInvoke := false
	for _, definition := range model.requests[0].ToolDefinitions {
		initialHasInvoke = initialHasInvoke || definition.Name == core.ToolSkillInvoke
	}
	if !initialHasInvoke {
		t.Fatalf("initial definitions=%#v", model.requests[0].ToolDefinitions)
	}
	for _, definition := range model.requests[1].ToolDefinitions {
		if definition.Name == core.ToolSkillInvoke {
			t.Fatal("resume step exposed recursive skill_invoke")
		}
	}
	if !model.requests[1].ToolScopeDeclared || len(model.requests[1].AllowedTools) != 1 || model.requests[1].AllowedTools[0] != core.ToolWebSearch {
		t.Fatalf("resume tool scope declared=%v allowed=%#v", model.requests[1].ToolScopeDeclared, model.requests[1].AllowedTools)
	}
	if !strings.Contains(model.requests[1].SystemPrompt, "frozen release checklist") || !strings.Contains(model.requests[1].SystemPrompt, "production") {
		t.Fatalf("resume prompt=%q", model.requests[1].SystemPrompt)
	}
	if !model.requests[2].EnableTools || !strings.Contains(model.requests[2].SystemPrompt, "frozen release checklist") || !strings.Contains(model.requests[2].SystemPrompt, "production") || !strings.Contains(model.requests[2].UserPrompt, "verified") {
		t.Fatalf("continuation request=%#v", model.requests[2])
	}
	pairedToolHistory := false
	for index := 0; index+1 < len(model.requests[2].Messages); index++ {
		callBlock := model.requests[2].Messages[index].Content
		resultBlock := model.requests[2].Messages[index+1].Content
		if len(callBlock) == 1 && callBlock[0].ToolCall != nil &&
			len(resultBlock) == 1 && resultBlock[0].ToolResult != nil &&
			callBlock[0].ToolCall.ID == resultBlock[0].ToolResult.ToolCallID {
			pairedToolHistory = true
			break
		}
	}
	if !pairedToolHistory {
		t.Fatalf("typed continuation has no paired ToolCall/ToolResult: %#v", model.requests[2].Messages)
	}
	receipts, err := service.ListSideEffectReceipts(ctx, "all")
	if err != nil || len(receipts) != 2 {
		t.Fatalf("receipts=%#v err=%v", receipts, err)
	}
	seenSkillReceipt := false
	for _, receipt := range receipts {
		if receipt.ToolName == core.ToolSkillInvoke && receipt.Status == core.SideEffectReceiptSucceeded {
			seenSkillReceipt = true
		}
	}
	if !seenSkillReceipt {
		t.Fatalf("missing successful Skill receipt in %#v", receipts)
	}
	events, err := service.ListRunEvents(ctx, task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.EventType] = true
	}
	for _, eventType := range []string{"skill.invoked", "model.skill_resume_queued", "skill.loaded", "model.continuation_queued"} {
		if !seen[eventType] {
			t.Fatalf("missing event %s in %#v", eventType, seen)
		}
	}
	if skill.ID == "" {
		t.Fatal("test Skill has no id")
	}
}

func TestExplicitSkillInvocationFreezesSelectionAndRejectsDisabledSkill(t *testing.T) {
	ctx := context.Background()
	store, service, project, skill := setupSkillProject(t, `{"description":"explicit release check","instructions":"Use the explicit Skill."}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "/release-check production", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate, SkillID: skill.ID, SkillArgs: "production"}, "explicit-skill", "")
	if err != nil {
		t.Fatal(err)
	}
	if task.Mode != core.ModeSingleAgent {
		t.Fatalf("mode=%s", task.Mode)
	}
	run, err := store.GetRunContext(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot) != nil {
		t.Fatal("snapshot is invalid JSON")
	}
	invocation, ok := snapshot["explicit_skill_invocation"].(map[string]any)
	if !ok || invocation["skill_id"] != skill.ID || invocation["skill_args"] != "production" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
	if _, err := service.SetCapabilityStatus(ctx, skill.ID, core.CapabilityStatusDisabled, "disable-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "retry", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, SkillID: skill.ID}, "disabled-skill", ""); err == nil {
		t.Fatal("disabled explicit Skill was accepted")
	}
}

func TestSkillInvokeMustBeExclusive(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"release verification","instructions":"Run checks."}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "verify release", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "mixed-skill-tools", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{{CallID: "00000000-0000-7000-0000-000000000421", Provider: "test", Model: "test-model", ToolCalls: []core.ToolCall{
		{Name: core.ToolSkillInvoke, Arguments: map[string]any{"skill": "release-check"}},
		{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "latest release"}},
	}}}}
	worker := NewWorker("mixed-skill-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if worked, runErr := worker.RunOnce(ctx); runErr != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, runErr)
	}
	blocked, err := service.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != core.StatusBlocked || blocked.ErrorCode != "SKILL_INVOKE_MUST_BE_EXCLUSIVE" {
		t.Fatalf("task=%#v", blocked)
	}
}
