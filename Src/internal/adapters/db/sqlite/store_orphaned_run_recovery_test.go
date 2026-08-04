package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestClaimNextStepRecoversCompletedBatchWithoutRunnableStep(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000211", DisplayName: "Recovery User"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000212", Name: "Recovery Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "test-key" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "write and verify an HTML file", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	modelStep, err := store.ClaimNextStep(ctx, "worker-before-crash", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	nowText := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'succeeded', output_json = '{"content":"write the verified file"}', owner_id = '', lease_until = '', updated_at = ? WHERE id = ?`, nowText, modelStep.ID); err != nil {
		t.Fatal(err)
	}
	run, err := getRunContextTx(ctx, tx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	status, _, err := store.planToolSteps(ctx, tx, store.queries.WithTx(tx), modelStep, run, []core.ToolCall{{Name: core.ToolWorkspaceWriteFile, Arguments: map[string]any{"path": "done.html", "content": "<h1>done</h1>"}}}, nowText, "plan-before-crash")
	if err != nil {
		t.Fatal(err)
	}
	if status != core.StatusQueued {
		t.Fatalf("planned tool status = %s, want queued", status)
	}
	var batchID, toolStepID string
	if err := tx.QueryRowContext(ctx, `SELECT tool_batch_id, id FROM steps WHERE run_id = ? AND step_type = ?`, task.RunID, core.ToolWorkspaceWriteFile).Scan(&batchID, &toolStepID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'succeeded', output_json = '{"path":"done.html"}', updated_at = ? WHERE id = ?`, nowText, toolStepID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'succeeded', completed_at = ?, updated_at = ? WHERE step_id = ?`, nowText, nowText, toolStepID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_batches SET status = 'running', continuation_claimed = 0, continuation_step_id = '', updated_at = ? WHERE id = ?`, nowText, batchID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	recovered, err := store.ClaimNextStep(ctx, "worker-after-crash", now.Add(time.Second), now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.StepType != core.StepModelContinue {
		t.Fatalf("recovered step type = %s, want %s", recovered.StepType, core.StepModelContinue)
	}
	var batchStatus, continuationStepID string
	var continuationClaimed int
	if err := store.db.QueryRowContext(ctx, `SELECT status, continuation_claimed, continuation_step_id FROM tool_batches WHERE id = ?`, batchID).Scan(&batchStatus, &continuationClaimed, &continuationStepID); err != nil {
		t.Fatal(err)
	}
	if batchStatus != core.StatusSucceeded || continuationClaimed != 1 || continuationStepID != recovered.ID {
		t.Fatalf("batch recovery = %s/%d/%s, recovered step = %s", batchStatus, continuationClaimed, continuationStepID, recovered.ID)
	}
	var recoveryEvents int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'tool.batch_recovered'`, task.RunID).Scan(&recoveryEvents); err != nil {
		t.Fatal(err)
	}
	if recoveryEvents != 1 {
		t.Fatalf("tool.batch_recovered events = %d, want 1", recoveryEvents)
	}
}
