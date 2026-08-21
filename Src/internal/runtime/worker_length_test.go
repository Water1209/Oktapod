package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type countingSearch struct {
	calls   int
	queries []string
	payload string
}

type blockingStreamingModel struct{}

func (blockingStreamingModel) Complete(context.Context, ModelRequest) (core.ModelResult, error) {
	panic("streaming path was not used")
}

func (blockingStreamingModel) Stream(ctx context.Context, _ ModelRequest, _ func(string) error) (core.ModelResult, error) {
	<-ctx.Done()
	return core.ModelResult{Provider: "test", Model: "slow-model", ErrorCode: "MODEL_ABORTED"}, ctx.Err()
}

func TestMCPCallsHaveABoundedExecutionDeadline(t *testing.T) {
	worker := NewWorker("mcp-timeout-worker", nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if worker.mcpCallTimeout != 10*time.Minute {
		t.Fatalf("default MCP timeout = %s, want 10m", worker.mcpCallTimeout)
	}
	worker.WithMCPCallTimeout(25 * time.Millisecond)
	if worker.mcpCallTimeout != 25*time.Millisecond {
		t.Fatalf("configured MCP timeout = %s, want 25ms", worker.mcpCallTimeout)
	}
}

func TestModelTurnsHaveABoundedCompletionDeadline(t *testing.T) {
	worker := NewWorker("model-timeout-worker", nil, blockingStreamingModel{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if worker.modelCallTimeout != 2*time.Minute {
		t.Fatalf("default model timeout = %s, want 2m", worker.modelCallTimeout)
	}
	worker.WithModelCallTimeout(20 * time.Millisecond)
	started := time.Now()
	result, err := worker.completeModel(t.Context(), ModelRequest{})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("model timeout returned after %s", elapsed)
	}
	if result.ErrorCode != "PROVIDER_UNAVAILABLE" || !isModelRequestTimeout(err) {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	failure := modelFailureDetails(err)
	if failure["kind"] != "model_turn_timeout" || failure["phase"] != "model_turn" || failure["timed_out"] != true {
		t.Fatalf("failure=%#v", failure)
	}
	if got := modelFailureSummary(result.ErrorCode, err); got != "模型本轮响应超过 120 秒；即使连接仍有数据，系统也已停止等待并从当前检查点重试。" {
		t.Fatalf("summary=%q", got)
	}
}

func TestModelTimeoutSummaryDistinguishesSlowProviderResponse(t *testing.T) {
	err := errors.New("PROVIDER_UNAVAILABLE: context deadline exceeded (Client.Timeout exceeded while awaiting headers)")
	if !isModelRequestTimeout(err) {
		t.Fatal("expected provider timeout to be recognized")
	}
	if got := modelFailureSummary("PROVIDER_UNAVAILABLE", err); got != "模型响应超时；系统将从当前检查点自动重试。" {
		t.Fatalf("summary=%q", got)
	}
}

func (s *countingSearch) Execute(_ context.Context, call core.ToolCall) core.ToolExecution {
	s.calls++
	query, _ := call.Arguments["query"].(string)
	s.queries = append(s.queries, query)
	return core.ToolExecution{ToolCallID: call.ID, Status: core.StatusSucceeded, Output: map[string]any{"query": query, "payload": s.payload}}
}

func TestLengthTruncatedToolCallsAreSkippedAndRecovered(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"unused","instructions":"unused","allowed_tools":["web_search"]}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{
		Goal:       "read evidence",
		Mode:       core.ModeSingleAgent,
		Visibility: core.VisibilityPrivate,
	}, "length-truncated-run", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{
		{
			CallID:     "length-call-1",
			Provider:   "test",
			Model:      "test-model",
			Content:    "I will search",
			StopReason: core.StopReasonLength,
			ToolCalls: []core.ToolCall{{
				ID:        "provider-tool-call-1",
				Name:      core.ToolWebSearch,
				Arguments: map[string]any{"query": "incomplete"},
			}},
		},
		{
			CallID:     "length-call-2",
			Provider:   "test",
			Model:      "test-model",
			Content:    "Recovered without assuming the skipped call ran.",
			StopReason: core.StopReasonStop,
		},
	}}
	search := &countingSearch{}
	worker := NewWorker("length-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil))).WithWebSearchExecutor(search)
	for iteration := 0; iteration < 2; iteration++ {
		worked, runErr := worker.RunOnce(ctx)
		if runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
	}
	if search.calls != 0 {
		t.Fatalf("truncated tool was executed %d times", search.calls)
	}
	completed, err := service.GetTask(ctx, task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		t.Fatalf("task=%#v err=%v", completed, err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model requests=%d", len(model.requests))
	}
	recovery := model.requests[1]
	if !strings.Contains(recovery.UserPrompt, "不得假设被截断的调用已经执行") {
		t.Fatalf("recovery instruction=%q", recovery.UserPrompt)
	}
	foundErrorResult := false
	for _, message := range recovery.Messages {
		for _, block := range message.Content {
			if block.ToolResult != nil && block.ToolResult.IsError && strings.Contains(block.ToolResult.Content, "MODEL_OUTPUT_TRUNCATED") {
				foundErrorResult = true
			}
		}
	}
	if !foundErrorResult {
		t.Fatalf("recovery messages lack paired error ToolResult: %#v", recovery.Messages)
	}
	receipts, err := service.ListSideEffectReceipts(ctx, "all")
	if err != nil || len(receipts) != 0 {
		t.Fatalf("truncated tool created receipts=%#v err=%v", receipts, err)
	}
	events, err := service.ListRunEvents(ctx, task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	skipped := false
	for _, event := range events {
		if event.EventType == "tool.skipped" && event.Payload["error_code"] == "MODEL_OUTPUT_TRUNCATED" && event.Payload["executed"] == false {
			skipped = true
		}
	}
	if !skipped {
		t.Fatalf("missing deterministic tool.skipped event: %#v", events)
	}
}

func TestLengthTruncatedResponseWithoutParsedToolCallRetriesAndRequiresArtifact(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"unused","instructions":"unused","allowed_tools":["workspace_write_file"]}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{
		Goal:       "write report.md",
		Mode:       core.ModeSingleAgent,
		Visibility: core.VisibilityPrivate,
	}, "length-truncated-unparsed-tool", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{
		{
			CallID:     "length-unparsed-1",
			Provider:   "test",
			Model:      "test-model",
			Content:    "I will write the report now",
			StopReason: core.StopReasonLength,
		},
		{
			CallID:     "length-unparsed-2",
			Provider:   "test",
			Model:      "test-model",
			Content:    "Recovered after shortening the tool arguments.",
			StopReason: core.StopReasonStop,
		},
	}}
	worker := NewWorker("length-unparsed-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for iteration := 0; iteration < 2; iteration++ {
		worked, runErr := worker.RunOnce(ctx)
		if runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
	}
	current, err := service.GetTask(ctx, task.ID)
	if err != nil || current.Status != core.StatusRunning {
		t.Fatalf("artifact task falsely completed after model retry: task=%#v err=%v", current, err)
	}
	if len(model.requests) != 2 {
		t.Fatalf("model requests=%d", len(model.requests))
	}
	if model.requests[1].MaxOutputTokens <= model.requests[0].MaxOutputTokens {
		t.Fatalf("retry output budget did not increase: first=%d retry=%d", model.requests[0].MaxOutputTokens, model.requests[1].MaxOutputTokens)
	}
	if !strings.Contains(model.requests[1].UserPrompt, "参数过长时拆成更小的工具调用") {
		t.Fatalf("retry instruction=%q", model.requests[1].UserPrompt)
	}
	events, err := service.ListRunEvents(ctx, task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(event core.RunEvent) bool {
		return event.EventType == "completion.validation_failed" && event.Payload["reason"] == "DELIVERABLE_MISSING"
	}) {
		t.Fatalf("artifact completion validation was not queued: %#v", events)
	}
}

func TestSequentialDeclarationForcesWholeToolBatchToSourceOrder(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"unused","instructions":"unused","allowed_tools":["web_search"]}`)
	_, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{
		Goal:       "run sequential evidence checks",
		Mode:       core.ModeSingleAgent,
		Visibility: core.VisibilityPrivate,
	}, "sequential-batch", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{
		semanticPlanResult("sequential-semantic-plan"),
		{
			CallID: "sequential-plan",
			ToolCalls: []core.ToolCall{
				{ID: "source-1", Name: core.ToolWebSearch, Arguments: map[string]any{"query": "first"}, Sequential: true},
				{ID: "source-2", Name: core.ToolWebSearch, Arguments: map[string]any{"query": "second"}},
			},
		},
	}}
	worker := NewWorker("sequential-model", store, model, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for iteration := 0; iteration < 2; iteration++ {
		if worked, runErr := worker.RunOnce(ctx); runErr != nil || !worked {
			t.Fatalf("planning iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
	}
	first, err := store.ClaimNextStep(ctx, "sequential-tool-1", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil || first.StepType != core.ToolWebSearch {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	if _, err := store.ClaimNextStep(ctx, "sequential-tool-2", time.Now().UTC(), time.Now().UTC().Add(time.Minute)); !errors.Is(err, core.ErrNoWork) {
		t.Fatalf("sequential batch exposed its second call early: %v", err)
	}
}

func TestDurableParallelToolBatchQueuesOneOrderedContinuation(t *testing.T) {
	ctx := context.Background()
	store, service, project, _ := setupSkillProject(t, `{"description":"unused","instructions":"unused","allowed_tools":["web_search"]}`)
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{
		Goal:       "collect two independent sources",
		Mode:       core.ModeSingleAgent,
		Visibility: core.VisibilityPrivate,
	}, "parallel-batch", "")
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSkillModel{results: []core.ModelResult{
		semanticPlanResult("parallel-semantic-plan"),
		{
			CallID: "parallel-plan",
			ToolCalls: []core.ToolCall{
				{ID: "source-1", Name: core.ToolWebSearch, Arguments: map[string]any{"query": "first"}},
				{ID: "source-2", Name: core.ToolWebSearch, Arguments: map[string]any{"query": "second"}},
			},
		},
		{CallID: "parallel-final", Content: "Both ordered results were synthesized.", StopReason: core.StopReasonStop},
	}}
	search := &countingSearch{payload: strings.Repeat("x", 40_000)}
	worker := NewWorker("parallel-worker", store, model, slog.New(slog.NewTextHandler(io.Discard, nil))).WithWebSearchExecutor(search)
	for iteration := 0; iteration < 5; iteration++ {
		worked, runErr := worker.RunOnce(ctx)
		if runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
	}
	if len(search.queries) != 2 || search.queries[0] != "first" || search.queries[1] != "second" {
		t.Fatalf("executed queries=%#v", search.queries)
	}
	if len(model.requests) != 3 {
		t.Fatalf("model requests=%d", len(model.requests))
	}
	orderedResults := make([]string, 0, 2)
	for _, message := range model.requests[2].Messages {
		for _, block := range message.Content {
			if block.ToolCall != nil {
				query, _ := block.ToolCall.Arguments["query"].(string)
				orderedResults = append(orderedResults, query)
			}
		}
	}
	if len(orderedResults) != 2 || orderedResults[0] != "first" || orderedResults[1] != "second" {
		t.Fatalf("persisted source order=%#v messages=%#v", orderedResults, model.requests[2].Messages)
	}
	artifactReferences := 0
	for _, message := range model.requests[2].Messages {
		for _, block := range message.Content {
			if block.ToolResult != nil && strings.Contains(block.ToolResult.Content, `"artifact_id"`) && strings.Contains(block.ToolResult.Content, `"truncated":true`) {
				artifactReferences++
			}
		}
	}
	if artifactReferences != 2 {
		t.Fatalf("large tool results were not replaced by artifact references: %#v", model.requests[2].Messages)
	}
	artifacts, err := service.ListRunArtifacts(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 2 || artifacts[0].Kind != "tool_result" || artifacts[1].Kind != "tool_result" {
		t.Fatalf("tool result artifacts=%#v", artifacts)
	}
	events, err := service.ListRunEvents(ctx, task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.EventType]++
		if event.EventType == "tool.batch_created" && event.Payload["execution_mode"] != "parallel" {
			t.Fatalf("batch event=%#v", event)
		}
	}
	if counts["tool.batch_created"] != 1 || counts["tool.batch_completed"] != 1 || counts["model.continuation_queued"] != 1 {
		t.Fatalf("durable batch event counts=%#v", counts)
	}
}

func TestLengthTruncatedTextIsVisibleAndDoesNotAutoRepeat(t *testing.T) {
	result, calls := normalizeLengthTruncatedModelResult(core.ModelResult{
		Content:    "partial answer",
		StopReason: core.StopReasonLength,
	})
	if len(calls) != 0 || !strings.Contains(result.Content, "回答被截断") {
		t.Fatalf("result=%#v calls=%#v", result, calls)
	}
}
