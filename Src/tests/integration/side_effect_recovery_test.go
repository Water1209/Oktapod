package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestUnknownSideEffectRequiresHumanResolutionAndNeverRetriesAutomatically(t *testing.T) {
	provider := newToolMockProvider(t)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Unknown side effect", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{
		Goal: "run a non-idempotent command", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate,
		PermissionMode: core.PermissionModeFullAccess, MaxModelCalls: 4, MaxTokens: 10000,
	}, "unknown-side-effect-task", "unknown-side-effect-idempotency")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("unknown-side-effect-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("model planning worked=%v err=%v", worked, runErr)
	}
	// Execute only the idempotent write step. The command step is then claimed
	// manually so the test can reproduce the retry window before execution.
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("write step worked=%v err=%v", worked, runErr)
	}
	claimed, err := repository.ClaimNextStep(context.Background(), "unknown-side-effect-manual", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.StepType != core.ToolWorkspaceRunCommand {
		t.Fatalf("claimed step=%s, want command", claimed.StepType)
	}
	run, err := repository.GetRunContext(context.Background(), claimed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkStepStarted(context.Background(), claimed, run, "unknown-side-effect-start"); err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		ToolCallID string         `json:"tool_call_id"`
		Name       string         `json:"name"`
		Arguments  map[string]any `json:"arguments"`
		InputHash  string         `json:"input_hash"`
		Idempotent bool           `json:"idempotent"`
	}
	if err := json.Unmarshal([]byte(claimed.InputJSON), &persisted); err != nil {
		t.Fatal(err)
	}
	call := core.ToolCall{ID: persisted.ToolCallID, Name: persisted.Name, Arguments: persisted.Arguments}
	_, err = repository.BeginToolExecution(context.Background(), claimed, run, call, persisted.InputHash, persisted.Idempotent, "unknown-side-effect-first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.BeginToolExecution(context.Background(), claimed, run, call, persisted.InputHash, persisted.Idempotent, "unknown-side-effect-retry"); err == nil || !strings.Contains(err.Error(), "SIDE_EFFECT_STATUS_UNKNOWN") {
		t.Fatalf("automatic retry error=%v, want SIDE_EFFECT_STATUS_UNKNOWN", err)
	}
	if err := repository.FinishStep(context.Background(), claimed, run, core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "SIDE_EFFECT_STATUS_UNKNOWN", ResultSummary: "external command result is unknown", Output: map[string]any{"retryable": false}}, "unknown-side-effect-block"); err != nil {
		t.Fatal(err)
	}
	receipts, err := service.ListSideEffectReceipts(context.Background(), core.SideEffectReceiptUnknown)
	if err != nil || len(receipts) != 1 || receipts[0].ID == "" {
		t.Fatalf("unknown receipts=%#v err=%v", receipts, err)
	}
	next, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: task.ConversationID, Content: "wait for side effect reconciliation", Mode: core.ModeSingleAgent}, "", "after-unknown-turn")
	if err != nil || !next.Queued || next.BlockingTaskID != task.ID {
		t.Fatalf("unknown receipt did not queue successor: %#v %v", next, err)
	}
	if _, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, next.QueueItem.ID); !errors.Is(err, core.ErrNoWork) {
		t.Fatalf("successor bypassed unknown receipt: %v", err)
	}
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	listed := getJSON[struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/side-effect-receipts?status=unknown")
	if len(listed.Items) != 1 || listed.Items[0].ID != receipts[0].ID || listed.Items[0].Status != string(core.SideEffectReceiptUnknown) {
		t.Fatalf("HTTP unknown receipts=%#v", listed)
	}
	resolvedHTTP := postJSONWithKey[struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}](t, server.URL+"/api/v1/side-effect-receipts/"+receipts[0].ID+"/resolution", map[string]any{"decision": core.SideEffectDecisionConfirmApplied}, "unknown-side-effect-resolution")
	if resolvedHTTP.Status != string(core.SideEffectReceiptSucceeded) || resolvedHTTP.ErrorCode != "MANUALLY_CONFIRMED" {
		t.Fatalf("HTTP resolved receipt=%#v", resolvedHTTP)
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != core.StatusSucceeded {
		t.Fatalf("task after human confirmation=%#v", completed)
	}
	nextClaim, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, next.QueueItem.ID)
	if err != nil {
		t.Fatalf("reconciled receipt did not release project: %v", err)
	}
	if _, err := service.StartQueuedConversationTurn(t.Context(), nextClaim); err != nil {
		t.Fatal(err)
	}
	var conflict *core.StateConflictError
	if _, err := service.ResolveSideEffect(context.Background(), receipts[0].ID, core.ResolveSideEffectInput{Decision: core.SideEffectDecisionConfirmApplied}, "unknown-side-effect-replay", "unknown-side-effect-replay"); !errors.As(err, &conflict) {
		t.Fatalf("second resolution error=%v, want state conflict", err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenUnknown, seenResolved := false, false
	for _, event := range events {
		seenUnknown = seenUnknown || event.EventType == "tool.side_effect_unknown"
		seenResolved = seenResolved || event.EventType == "tool.side_effect_resolved"
	}
	if !seenUnknown || !seenResolved {
		t.Fatalf("missing unknown/resolution events: %#v", events)
	}
}
