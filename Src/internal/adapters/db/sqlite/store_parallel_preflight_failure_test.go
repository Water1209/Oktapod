package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestParallelToolBatchClosesWhenPreflightFailureFinishesBeforeSuccess(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000321", DisplayName: "Parallel Batch User"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000322", Name: "Parallel Batch Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "test-key" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Parallel Batch", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "read two files in parallel", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	modelStep, err := store.ClaimNextStep(ctx, "planner", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	nowText := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'succeeded', output_json = '{"content":"parallel reads"}', owner_id = '', lease_until = '', updated_at = ? WHERE id = ?`, nowText, modelStep.ID); err != nil {
		t.Fatal(err)
	}
	run, err := getRunContextTx(ctx, tx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	calls := []core.ToolCall{
		{Name: core.ToolReplacePlan, Arguments: map[string]any{"items": []any{map[string]any{"title": "Read files", "description": "Read both files and preserve failures."}}}},
		{Name: core.ToolWorkspaceReadFile, Arguments: map[string]any{"path": "first.txt"}},
		{Name: core.ToolWorkspaceReadFile, Arguments: map[string]any{"path": "second.txt"}},
	}
	plannedStatus, plannedError, err := store.planToolSteps(ctx, tx, store.queries.WithTx(tx), modelStep, run, calls, nowText, "parallel-plan")
	if err != nil {
		t.Fatal(err)
	}
	if plannedStatus != core.StatusQueued {
		t.Fatalf("planned status = %s/%s, want queued", plannedStatus, plannedError)
	}
	var batchID string
	if err := tx.QueryRowContext(ctx, `SELECT tool_batch_id FROM steps WHERE run_id = ? AND tool_batch_id <> '' LIMIT 1`, task.RunID).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	first, err := store.ClaimNextStep(ctx, "worker-a", now.Add(time.Second), now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.StepType != core.ToolWorkspaceReadFile {
		t.Fatalf("first claimed step = %s, want %s", first.StepType, core.ToolWorkspaceReadFile)
	}
	if err := store.MarkStepStarted(ctx, first, run, "parallel-start-a"); err != nil {
		t.Fatal(err)
	}
	second, err := store.ClaimNextStep(ctx, "worker-b", now.Add(time.Second), now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStepStarted(ctx, second, run, "parallel-start-b"); err != nil {
		t.Fatal(err)
	}

	var invocation struct {
		ToolCallID string         `json:"tool_call_id"`
		Name       string         `json:"name"`
		Arguments  map[string]any `json:"arguments"`
		InputHash  string         `json:"input_hash"`
		Idempotent bool           `json:"idempotent"`
	}
	if err := json.Unmarshal([]byte(second.InputJSON), &invocation); err != nil {
		t.Fatal(err)
	}
	call := core.ToolCall{ID: invocation.ToolCallID, Name: invocation.Name, Arguments: invocation.Arguments}
	invocationID, err := store.BeginToolExecution(ctx, second, run, call, invocation.InputHash, invocation.Idempotent, "parallel-success-begin")
	if err != nil {
		t.Fatal(err)
	}

	blocked := core.StepOutcome{Status: core.StatusBlocked, ErrorCode: "DOCUMENT_RUNTIME_VERSION_UNAVAILABLE", ResultSummary: "document runtime unavailable", Output: map[string]any{"retryable": true}}
	if err := store.FinishStep(ctx, first, run, blocked, "parallel-preflight-blocked"); err != nil {
		t.Fatal(err)
	}
	succeeded := core.StepOutcome{
		Status: core.StatusSucceeded, ResultSummary: "file read", Output: map[string]any{"path": "second.txt", "content": "ok"},
		ToolExecution: &core.ToolExecution{ToolCallID: invocation.ToolCallID, InvocationID: invocationID, Status: core.StatusSucceeded, Output: map[string]any{"path": "second.txt", "content": "ok"}, InputHash: invocation.InputHash, Idempotent: invocation.Idempotent},
	}
	if err := store.FinishStep(ctx, second, run, succeeded, "parallel-success-finish"); err != nil {
		t.Fatal(err)
	}

	var batchStatus string
	var continuationClaimed int
	if err := store.db.QueryRowContext(ctx, `SELECT status, continuation_claimed FROM tool_batches WHERE id = ?`, batchID).Scan(&batchStatus, &continuationClaimed); err != nil {
		t.Fatal(err)
	}
	if batchStatus != core.StatusBlocked || continuationClaimed != 1 {
		t.Fatalf("batch = %s/claimed=%d, want blocked/claimed=1", batchStatus, continuationClaimed)
	}
	var taskStatus, taskError, taskSummary string
	if err := store.db.QueryRowContext(ctx, `SELECT status, error_code, result_summary FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus, &taskError, &taskSummary); err != nil {
		t.Fatal(err)
	}
	if taskStatus != core.StatusBlocked || taskError != "DOCUMENT_RUNTIME_VERSION_UNAVAILABLE" {
		t.Fatalf("task = %s/%s, want blocked/DOCUMENT_RUNTIME_VERSION_UNAVAILABLE", taskStatus, taskError)
	}
	if taskSummary == "file read" {
		t.Fatal("successful last tool overwrote the batch failure summary")
	}
	var activeCalls int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE tool_batch_id = ? AND status IN ('planned', 'queued', 'running', 'waiting_approval')`, batchID).Scan(&activeCalls); err != nil {
		t.Fatal(err)
	}
	if activeCalls != 0 {
		t.Fatalf("active tool calls = %d, want 0", activeCalls)
	}

	// A restored historical batch can contain multiple blocked members. A retry
	// must reactivate the whole unfinished batch frontier; leaving an earlier
	// blocked member behind makes the later queued member permanently unclaimable.
	if _, err := store.db.ExecContext(ctx, `UPDATE steps SET status = 'blocked', error_code = 'DOCUMENT_RUNTIME_VERSION_UNAVAILABLE' WHERE id = ?`, second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'blocked', error_code = 'DOCUMENT_RUNTIME_VERSION_UNAVAILABLE' WHERE id = ?`, invocation.ToolCallID); err != nil {
		t.Fatal(err)
	}
	updated, err := service.ControlTask(ctx, task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "parallel-batch-retry", "parallel-batch-retry")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	var queuedSteps, queuedCalls int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE tool_batch_id = ? AND status = 'queued'`, batchID).Scan(&queuedSteps); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE tool_batch_id = ? AND status = 'queued'`, batchID).Scan(&queuedCalls); err != nil {
		t.Fatal(err)
	}
	if queuedSteps != 2 || queuedCalls != 2 {
		t.Fatalf("retried batch queued steps/calls = %d/%d, want 2/2", queuedSteps, queuedCalls)
	}
	var retriedBatchStatus string
	var retriedClaimed int
	if err := store.db.QueryRowContext(ctx, `SELECT status, continuation_claimed FROM tool_batches WHERE id = ?`, batchID).Scan(&retriedBatchStatus, &retriedClaimed); err != nil {
		t.Fatal(err)
	}
	if retriedBatchStatus != core.StatusRunning || retriedClaimed != 0 {
		t.Fatalf("retried batch = %s/claimed=%d, want running/0", retriedBatchStatus, retriedClaimed)
	}

	// Simulate the partial retry persisted by an older build and confirm an idle
	// worker upgrades it without database surgery or another user command.
	if _, err := store.db.ExecContext(ctx, `UPDATE steps SET status = 'blocked', error_code = 'DOCUMENT_RUNTIME_VERSION_UNAVAILABLE' WHERE id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'blocked', error_code = 'DOCUMENT_RUNTIME_VERSION_UNAVAILABLE' WHERE step_id = ?`, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tool_batches SET status = 'blocked', continuation_claimed = 1, continuation_step_id = '' WHERE id = ?`, batchID); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimNextStep(ctx, "upgrade-worker", now.Add(2*time.Second), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != first.ID && claimed.ID != second.ID {
		t.Fatalf("claimed step %q is not a member of repaired batch %q", claimed.ID, batchID)
	}
	var blockedAfterRepair int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE tool_batch_id = ? AND status IN ('failed', 'blocked')`, batchID).Scan(&blockedAfterRepair); err != nil {
		t.Fatal(err)
	}
	if blockedAfterRepair != 0 {
		t.Fatalf("partial retry repair left %d blocked batch steps", blockedAfterRepair)
	}
}
