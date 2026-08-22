package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestAutoAnswerUsesOneBusinessModelCall(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"unused","instructions":"unused"}`)
	turn, err := service.SubmitProjectTurn(ctx, project.ID, core.ProjectTurnInput{
		Content: "Explain the current project in one sentence", Mode: core.ModeAuto, Visibility: core.VisibilityPrivate,
	}, "auto-agent-v2", "")
	if err != nil {
		t.Fatal(err)
	}
	if turn.Route != "task" || turn.Task == nil || turn.Task.PresentationKind != core.TaskPresentationPendingAuto {
		t.Fatalf("turn=%#v", turn)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{{
		CallID: "00000000-0000-7000-8000-000000000801", Provider: "test", Model: "test-model",
		Content: "This project is ready for governed agent work.", StopReason: core.StopReasonStop,
		FirstByteLatency: 12 * time.Millisecond, FirstSafeLatency: 18 * time.Millisecond,
		Usage: core.ModelUsage{CacheWriteTokens: 4},
	}}}
	worker := NewWorker("auto-answer-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if worked, runErr := worker.RunOnce(ctx); runErr != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, runErr)
	}
	completed, err := service.GetTask(ctx, turn.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != core.StatusSucceeded || completed.PresentationKind != core.TaskPresentationAnswer || len(model.requests) != 1 {
		t.Fatalf("task=%#v model_calls=%d", completed, len(model.requests))
	}
	messages, err := service.ListMessages(ctx, completed.ConversationID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[1].Role != string(core.MessageRoleAssistant) || messages[1].Content != "This project is ready for governed agent work." {
		t.Fatalf("auto answer conversation messages=%#v", messages)
	}
	usage, err := service.GetRunUsage(ctx, completed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCalls != 1 {
		t.Fatalf("model calls=%d, want one business call", usage.ModelCalls)
	}
	if usage.FirstByteMS != 12 || usage.FirstSafeDeltaMS != 18 || usage.CacheWriteTokens != 4 || usage.RequestBytes <= 0 || usage.ModelCallPurposes["task_execute"] != 1 {
		t.Fatalf("performance usage=%#v", usage)
	}
}

func TestTransitionPlanControlsShareBusinessResponses(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"unused","instructions":"unused"}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{
		Goal: "Collect two independent evidence points", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate,
	}, "transition-plan-v2", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{
		{
			CallID: "00000000-0000-7000-8000-000000000811", Provider: "test", Model: "test-model",
			ToolCalls: []core.ToolCall{
				{Name: core.ToolReplacePlan, Arguments: map[string]any{"items": []any{
					map[string]any{"title": "Collect the first evidence point", "description": "Obtain and preserve the first independent result."},
					map[string]any{"title": "Collect the second evidence point", "description": "Obtain and preserve the second independent result."},
				}}},
				{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "first evidence"}},
			},
		},
		{
			CallID: "00000000-0000-7000-8000-000000000812", Provider: "test", Model: "test-model",
			ToolCalls: []core.ToolCall{
				{Name: core.ToolAdvancePlan, Arguments: map[string]any{"summary": "First evidence verified."}},
				{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "second evidence"}},
			},
		},
		{CallID: "00000000-0000-7000-8000-000000000813", Provider: "test", Model: "test-model", Content: "Both evidence points were collected and verified.", StopReason: core.StopReasonStop},
	}}
	worker := NewWorker("transition-plan-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil))).WithWebSearchExecutor(successfulSkillSearch{})
	for iteration := 0; iteration < 2; iteration++ {
		if worked, runErr := worker.RunOnce(ctx); runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
	}
	planAfterTool, err := service.GetActiveRunPlan(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(planAfterTool.Items) != 2 || planAfterTool.Items[0].Status != core.RunPlanItemRunning {
		t.Fatalf("tool success advanced semantic plan without advance_plan: %#v", planAfterTool)
	}
	for iteration := 2; iteration < 5; iteration++ {
		if worked, runErr := worker.RunOnce(ctx); runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
	}
	completed, err := service.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != core.StatusSucceeded || len(model.requests) != 3 {
		t.Fatalf("task=%#v model_calls=%d", completed, len(model.requests))
	}
	for _, request := range model.requests {
		for _, definition := range request.ToolDefinitions {
			if definition.Name == core.ToolUpdatePlan {
				t.Fatal("new Run advertised legacy update_plan")
			}
		}
	}
	finalPlan, err := service.GetActiveRunPlan(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range finalPlan.Items {
		if item.Status != core.RunPlanItemCompleted {
			t.Fatalf("terminal completion did not converge plan: %#v", finalPlan)
		}
	}
}

func TestBarrierSchedulerPreservesModelOrder(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"unused","instructions":"unused"}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{
		Goal: "Collect evidence around one exclusive write", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate,
	}, "barrier-v2", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{{
		CallID: "00000000-0000-7000-8000-000000000821", Provider: "test", Model: "test-model",
		ToolCalls: []core.ToolCall{
			{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "safe one"}},
			{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "safe two"}},
			{Name: core.ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "outputs/barrier/result.txt", "content": "verified"}},
			{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "safe three"}},
		},
	}}}
	workspaceExecutor, err := workspace.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker("barrier-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithWorkspaceExecutor(workspaceExecutor).WithWebSearchExecutor(successfulSkillSearch{})
	if worked, runErr := worker.RunOnce(ctx); runErr != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, runErr)
	}
	events, err := service.ListRunEvents(ctx, task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	groups := make([]int, 0, 4)
	for _, event := range events {
		if event.EventType != "tool.batch_planned" {
			continue
		}
		items, _ := event.Payload["items"].([]any)
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			group, _ := item["barrier_group"].(float64)
			groups = append(groups, int(group))
		}
	}
	if len(groups) != 4 || groups[0] != 0 || groups[1] != 0 || groups[2] != 1 || groups[3] != 2 {
		t.Fatalf("barrier groups=%#v, want [0 0 1 2]", groups)
	}
	now := time.Now().UTC()
	first, err := store.ClaimNextStep(ctx, "barrier-claim-a", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimNextStep(ctx, "barrier-claim-b", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.StepType != core.ToolWebSearch || second.StepType != core.ToolWebSearch || first.ID == second.ID {
		t.Fatalf("first barrier group was not concurrently claimable: first=%#v second=%#v", first, second)
	}
	if _, err := store.ClaimNextStep(ctx, "barrier-claim-c", now, now.Add(time.Minute)); !errors.Is(err, core.ErrNoWork) {
		t.Fatalf("exclusive successor crossed unfinished barrier: %v", err)
	}
}

func TestLiveTurnHubDropsSlowSubscriberWithoutBlockingPublisher(t *testing.T) {
	hub := NewLiveTurnHub()
	stream, _ := hub.SubscribeLiveTurn("run-live")
	for index := 0; index < liveTurnSubscriberCapacity+1; index++ {
		hub.PublishLiveTurn(core.LiveTurnEvent{RunID: "run-live", ModelCallID: "turn-live", Kind: "assistant_delta", Delta: "x"})
	}
	count := 0
	for range stream {
		count++
	}
	if count != liveTurnSubscriberCapacity {
		t.Fatalf("buffered events=%d", count)
	}

	stream, cancel := hub.SubscribeLiveTurn("run-flush")
	defer cancel()
	emitter := newLiveDeltaEmitter(hub, "run-flush", "turn-flush")
	if err := emitter.Add(strings.Repeat("y", 2*1024)); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-stream:
		if event.Kind != "assistant_delta" || len(event.Delta) != 2*1024 {
			t.Fatalf("event=%#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("2 KiB live delta did not flush immediately")
	}
	emitter.Close()
}

func TestBarrierSchedulerTreatsUnknownAndMCPMutationAsExclusive(t *testing.T) {
	readOnly, destructive, idempotent := true, false, true
	mutation := false
	calls := []core.ToolCall{
		{Name: core.ToolWebSearch},
		{Name: "unknown_tool"},
		{Name: "mcp__safe__search"},
		{Name: "mcp__mutation__write"},
	}
	definitions := []ToolDefinition{
		{Name: core.ToolWebSearch},
		{Name: "mcp__safe__search", ReadOnlyHint: &readOnly, DestructiveHint: &destructive, IdempotentHint: &idempotent},
		{Name: "mcp__mutation__write", ReadOnlyHint: &mutation, DestructiveHint: &destructive, IdempotentHint: &idempotent},
	}
	annotateToolCallExecutionMode(calls, definitions)
	if calls[0].Sequential || !calls[1].Sequential || calls[2].Sequential || !calls[3].Sequential {
		t.Fatalf("execution modes=%#v", calls)
	}
}
