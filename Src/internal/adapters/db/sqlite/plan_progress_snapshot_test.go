package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestQueuedContinuationFreezesActivePlanProgress(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000721", DisplayName: "Plan Snapshot User"}
	org := core.Organization{ID: "00000000-0000-7000-8000-000000000722", Name: "Plan Snapshot Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Plan snapshot", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "complete three plan items", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "plan-snapshot", "")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	now := formatTime(task.UpdatedAt)
	if _, _, err := createInitialRunPlan(ctx, tx, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, project.ID, task.ID, task.RunID, core.CreateTaskInput{Goal: task.Goal}, now); err != nil {
		t.Fatal(err)
	}
	var revisionID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM run_plan_revisions WHERE run_id = ? AND status = 'active'`, task.RunID).Scan(&revisionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET title = 'done', status = 'completed', completed_at = updated_at WHERE plan_revision_id = ?`, revisionID); err != nil {
		t.Fatal(err)
	}
	for position, status := range []string{core.RunPlanItemRunning, core.RunPlanItemPending} {
		itemID, idErr := newID()
		if idErr != nil {
			t.Fatal(idErr)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO run_plan_items (
				id, organization_id, project_id, task_id, run_id, plan_revision_id,
				position, title, description, status, started_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?)`,
			itemID, task.OrganizationID, task.ProjectID, task.ID, task.RunID, revisionID,
			position+2, "remaining item", status, now, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	run, err := getRunContextTx(ctx, tx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, err := insertQueuedModelStep(ctx, tx, run, map[string]any{
		"history": []map[string]any{{"kind": "assistant", "content": "continue"}},
		"stage":   "continue",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'completed', completed_at = ? WHERE plan_revision_id = ?`, now, revisionID); err != nil {
		t.Fatal(err)
	}
	var inputJSON string
	if err := tx.QueryRowContext(ctx, `SELECT input_json FROM steps WHERE id = ?`, stepID).Scan(&inputJSON); err != nil {
		t.Fatal(err)
	}
	var input struct {
		Total     int `json:"active_plan_total"`
		Remaining int `json:"active_plan_remaining"`
	}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if input.Total != 3 || input.Remaining != 2 {
		t.Fatalf("frozen plan progress = %d/%d, want 3/2: %s", input.Total, input.Remaining, inputJSON)
	}
}
