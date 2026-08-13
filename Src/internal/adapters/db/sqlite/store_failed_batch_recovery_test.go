package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func setupFailedBatchRun(t *testing.T, ctx context.Context) (*Store, core.Task, string) {
	t.Helper()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000311", DisplayName: "Batch Recovery User"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000312", Name: "Batch Recovery Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "test-key" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Batch Recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "run two sequential workspace writes", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	modelStep, err := store.ClaimNextStep(ctx, "worker-planner", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	nowText := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'succeeded', output_json = '{"content":"plan two sequential writes"}', owner_id = '', lease_until = '', updated_at = ? WHERE id = ?`, nowText, modelStep.ID); err != nil {
		t.Fatal(err)
	}
	run, err := getRunContextTx(ctx, tx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	calls := []core.ToolCall{
		{Name: core.ToolWorkspaceWriteFile, Sequential: true, Arguments: map[string]any{"path": "first.txt", "content": "first"}},
		{Name: core.ToolWorkspaceWriteFile, Sequential: true, Arguments: map[string]any{"path": "second.txt", "content": "second"}},
	}
	status, _, err := store.planToolSteps(ctx, tx, store.queries.WithTx(tx), modelStep, run, calls, nowText, "plan-sequential-batch")
	if err != nil {
		t.Fatal(err)
	}
	if status != core.StatusQueued {
		t.Fatalf("planned tool status = %s, want queued", status)
	}
	var batchID string
	if err := tx.QueryRowContext(ctx, `SELECT tool_batch_id FROM steps WHERE run_id = ? AND tool_batch_id <> '' LIMIT 1`, task.RunID).Scan(&batchID); err != nil {
		t.Fatal(err)
	}
	var executionMode string
	if err := tx.QueryRowContext(ctx, `SELECT execution_mode FROM tool_batches WHERE id = ?`, batchID).Scan(&executionMode); err != nil {
		t.Fatal(err)
	}
	if executionMode != "sequential" {
		t.Fatalf("batch execution_mode = %s, want sequential", executionMode)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return store, task, batchID
}

func TestFinishStepFailedSequentialBatchCancelsRemainderAndQueuesRecovery(t *testing.T) {
	ctx := context.Background()
	store, task, batchID := setupFailedBatchRun(t, ctx)

	now := time.Now().UTC()
	toolStep, err := store.ClaimNextStep(ctx, "worker-tool", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if toolStep.StepType != core.ToolWorkspaceWriteFile {
		t.Fatalf("claimed step = %s, want %s", toolStep.StepType, core.ToolWorkspaceWriteFile)
	}
	run, err := store.GetRunContext(ctx, toolStep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStepStarted(ctx, toolStep, run, "sequential-batch-start"); err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		ToolCallID string         `json:"tool_call_id"`
		Name       string         `json:"name"`
		Arguments  map[string]any `json:"arguments"`
		InputHash  string         `json:"input_hash"`
		Idempotent bool           `json:"idempotent"`
	}
	if err := json.Unmarshal([]byte(toolStep.InputJSON), &persisted); err != nil {
		t.Fatal(err)
	}
	call := core.ToolCall{ID: persisted.ToolCallID, Name: persisted.Name, Arguments: persisted.Arguments}
	invocationID, err := store.BeginToolExecution(ctx, toolStep, run, call, persisted.InputHash, persisted.Idempotent, "sequential-batch-begin")
	if err != nil {
		t.Fatal(err)
	}
	outcome := core.StepOutcome{
		Status:        core.StatusFailed,
		ErrorCode:     "COMMAND_EXIT_NONZERO",
		ResultSummary: "command exited with code 1",
		Output:        map[string]any{"exit_code": 1},
		ToolExecution: &core.ToolExecution{
			ToolCallID:   persisted.ToolCallID,
			InvocationID: invocationID,
			Status:       core.StatusFailed,
			ErrorCode:    "COMMAND_EXIT_NONZERO",
			ExitCode:     1,
			Output:       map[string]any{"exit_code": 1},
			InputHash:    persisted.InputHash,
			Idempotent:   persisted.Idempotent,
		},
	}
	if err := store.FinishStep(ctx, toolStep, run, outcome, "sequential-batch-fail"); err != nil {
		t.Fatal(err)
	}

	var secondStepStatus, secondStepError string
	if err := store.db.QueryRowContext(ctx, `SELECT status, error_code FROM steps WHERE tool_batch_id = ? AND batch_index = 2`, batchID).Scan(&secondStepStatus, &secondStepError); err != nil {
		t.Fatal(err)
	}
	if secondStepStatus != core.StatusCanceled || secondStepError != "BATCH_CANCELED_AFTER_FAILURE" {
		t.Fatalf("second step = %s/%s, want canceled/BATCH_CANCELED_AFTER_FAILURE", secondStepStatus, secondStepError)
	}
	var secondCallStatus, secondCallError string
	if err := store.db.QueryRowContext(ctx, `SELECT status, error_code FROM tool_calls WHERE tool_batch_id = ? AND batch_index = 2`, batchID).Scan(&secondCallStatus, &secondCallError); err != nil {
		t.Fatal(err)
	}
	if secondCallStatus != "blocked" || secondCallError != "BATCH_CANCELED_AFTER_FAILURE" {
		t.Fatalf("second tool call = %s/%s, want blocked/BATCH_CANCELED_AFTER_FAILURE", secondCallStatus, secondCallError)
	}
	var batchStatus string
	var continuationClaimed int
	if err := store.db.QueryRowContext(ctx, `SELECT status, continuation_claimed FROM tool_batches WHERE id = ?`, batchID).Scan(&batchStatus, &continuationClaimed); err != nil {
		t.Fatal(err)
	}
	if batchStatus != core.StatusFailed || continuationClaimed != 1 {
		t.Fatalf("batch = %s/claimed=%d, want failed/claimed=1", batchStatus, continuationClaimed)
	}
	var recoveryEvents int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'tool.recovery_queued'`, task.RunID).Scan(&recoveryEvents); err != nil {
		t.Fatal(err)
	}
	if recoveryEvents != 1 {
		t.Fatalf("tool.recovery_queued events = %d, want 1", recoveryEvents)
	}
	var taskStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != core.StatusRunning {
		t.Fatalf("task status = %s, want running", taskStatus)
	}
	recovered, err := store.ClaimNextStep(ctx, "worker-recovery", now.Add(time.Second), now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.StepType != core.StepModelContinue {
		t.Fatalf("recovery step type = %s, want %s", recovered.StepType, core.StepModelContinue)
	}

	// Regression: after the canceled remainder, a later successful tool step
	// must not derive a blocked run from the BATCH_CANCELED_AFTER_FAILURE call.
	tx2, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	nowText2 := formatTime(time.Now().UTC())
	if _, err := tx2.ExecContext(ctx, `UPDATE steps SET status = 'succeeded', output_json = '{"content":"recovery done"}', owner_id = '', lease_until = '', updated_at = ? WHERE id = ?`, nowText2, recovered.ID); err != nil {
		t.Fatal(err)
	}
	run2, err := getRunContextTx(ctx, tx2, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	followUp := []core.ToolCall{{Name: core.ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "third.txt", "content": "third"}}}
	status2, _, err := store.planToolSteps(ctx, tx2, store.queries.WithTx(tx2), recovered, run2, followUp, nowText2, "plan-after-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if status2 != core.StatusQueued {
		t.Fatalf("follow-up tool status = %s, want queued", status2)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	nextTool, err := store.ClaimNextStep(ctx, "worker-tool-2", now.Add(2*time.Second), now.Add(time.Minute+2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if nextTool.StepType != core.ToolWorkspaceWriteFile {
		t.Fatalf("follow-up step = %s, want %s", nextTool.StepType, core.ToolWorkspaceWriteFile)
	}
	run3, err := store.GetRunContext(ctx, nextTool.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStepStarted(ctx, nextTool, run3, "follow-up-start"); err != nil {
		t.Fatal(err)
	}
	var persisted2 struct {
		ToolCallID string         `json:"tool_call_id"`
		Name       string         `json:"name"`
		Arguments  map[string]any `json:"arguments"`
		InputHash  string         `json:"input_hash"`
		Idempotent bool           `json:"idempotent"`
	}
	if err := json.Unmarshal([]byte(nextTool.InputJSON), &persisted2); err != nil {
		t.Fatal(err)
	}
	call2 := core.ToolCall{ID: persisted2.ToolCallID, Name: persisted2.Name, Arguments: persisted2.Arguments}
	invocation2, err := store.BeginToolExecution(ctx, nextTool, run3, call2, persisted2.InputHash, persisted2.Idempotent, "follow-up-begin")
	if err != nil {
		t.Fatal(err)
	}
	contentSum := sha256.Sum256([]byte("third"))
	contentHash := fmt.Sprintf("sha256:%x", contentSum[:])
	successOutcome := core.StepOutcome{
		Status:        core.StatusSucceeded,
		ResultSummary: "file written",
		Output:        map[string]any{"path": "third.txt", "content_hash": contentHash},
		ToolExecution: &core.ToolExecution{
			ToolCallID:   persisted2.ToolCallID,
			InvocationID: invocation2,
			Status:       core.StatusSucceeded,
			ExitCode:     0,
			Output:       map[string]any{"path": "third.txt", "content_hash": contentHash},
			InputHash:    persisted2.InputHash,
			Idempotent:   persisted2.Idempotent,
		},
	}
	if err := store.FinishStep(ctx, nextTool, run3, successOutcome, "follow-up-finish"); err != nil {
		t.Fatal(err)
	}
	var finalTaskStatus, finalTaskError string
	if err := store.db.QueryRowContext(ctx, `SELECT status, error_code FROM tasks WHERE id = ?`, task.ID).Scan(&finalTaskStatus, &finalTaskError); err != nil {
		t.Fatal(err)
	}
	if finalTaskStatus == core.StatusBlocked {
		t.Fatalf("task blocked (%s) after follow-up tool success; canceled batch remainder must not count as policy denial", finalTaskError)
	}
	if finalTaskStatus != core.StatusRunning {
		t.Fatalf("task status = %s, want running", finalTaskStatus)
	}
}

func TestClaimNextStepHealsWedgedFailedSequentialBatch(t *testing.T) {
	ctx := context.Background()
	store, task, batchID := setupFailedBatchRun(t, ctx)

	now := time.Now().UTC()
	nowText := formatTime(now)
	var firstStepID, firstCallID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM steps WHERE tool_batch_id = ? AND batch_index = 1`, batchID).Scan(&firstStepID); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM tool_calls WHERE tool_batch_id = ? AND batch_index = 1`, batchID).Scan(&firstCallID); err != nil {
		t.Fatal(err)
	}
	// Simulate the pre-fix wedge: the first item of a sequential batch failed
	// while the batch stayed non-terminal and the remainder stayed queued.
	if _, err := store.db.ExecContext(ctx, `UPDATE steps SET status = 'failed', error_code = 'COMMAND_EXIT_NONZERO', updated_at = ? WHERE id = ?`, nowText, firstStepID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tool_calls SET status = 'failed', error_code = 'COMMAND_EXIT_NONZERO', completed_at = ?, updated_at = ? WHERE id = ?`, nowText, nowText, firstCallID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tool_batches SET status = 'running', continuation_claimed = 0, updated_at = ? WHERE id = ?`, nowText, batchID); err != nil {
		t.Fatal(err)
	}

	recovered, err := store.ClaimNextStep(ctx, "worker-after-restart", now.Add(time.Second), now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.StepType != core.StepModelContinue {
		t.Fatalf("healed step type = %s, want %s", recovered.StepType, core.StepModelContinue)
	}
	var secondStepStatus, secondStepError string
	if err := store.db.QueryRowContext(ctx, `SELECT status, error_code FROM steps WHERE tool_batch_id = ? AND batch_index = 2`, batchID).Scan(&secondStepStatus, &secondStepError); err != nil {
		t.Fatal(err)
	}
	if secondStepStatus != core.StatusCanceled || secondStepError != "BATCH_CANCELED_AFTER_FAILURE" {
		t.Fatalf("second step = %s/%s, want canceled/BATCH_CANCELED_AFTER_FAILURE", secondStepStatus, secondStepError)
	}
	var batchStatus string
	var continuationClaimed int
	if err := store.db.QueryRowContext(ctx, `SELECT status, continuation_claimed FROM tool_batches WHERE id = ?`, batchID).Scan(&batchStatus, &continuationClaimed); err != nil {
		t.Fatal(err)
	}
	if batchStatus != core.StatusFailed || continuationClaimed != 1 {
		t.Fatalf("batch = %s/claimed=%d, want failed/claimed=1", batchStatus, continuationClaimed)
	}
	var recoveredEvents int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'tool.batch_recovered'`, task.RunID).Scan(&recoveredEvents); err != nil {
		t.Fatal(err)
	}
	if recoveredEvents != 1 {
		t.Fatalf("tool.batch_recovered events = %d, want 1", recoveredEvents)
	}
	var recoveryEvents int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'tool.recovery_queued'`, task.RunID).Scan(&recoveryEvents); err != nil {
		t.Fatal(err)
	}
	if recoveryEvents != 1 {
		t.Fatalf("tool.recovery_queued events = %d, want 1", recoveryEvents)
	}
	var taskStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != core.StatusRunning {
		t.Fatalf("task status = %s, want running", taskStatus)
	}
}
