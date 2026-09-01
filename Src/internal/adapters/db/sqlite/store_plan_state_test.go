package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestSucceededTaskReconcilesBlockedPlanRevision(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000111", DisplayName: "Plan State User"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000112", Name: "Plan State Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "test-key" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Plan state", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "finish a planned task", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := formatTime(task.UpdatedAt)
	if _, _, err := createInitialRunPlan(ctx, tx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, project.ID, task.ID, task.RunID, core.CreateTaskInput{Goal: task.Goal}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'blocked' WHERE run_id = ?`, task.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'pending' WHERE run_id = ?`, task.RunID); err != nil {
		t.Fatal(err)
	}
	run, err := getRunContextTx(ctx, tx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.syncRunPlanState(ctx, tx, store.queries.WithTx(tx), run, core.StatusSucceeded, "verified result", now, "plan-state-test"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	plan, err := service.GetActiveRunPlan(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "completed" || len(plan.Items) != 1 || plan.Items[0].Status != core.RunPlanItemCompleted {
		t.Fatalf("plan was not reconciled: %#v", plan)
	}
}

func TestWaitingTaskDoesNotMoveNextSemanticPlanItem(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000121", DisplayName: "Plan Wait User"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000122", Name: "Plan Wait Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "test-key" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Plan wait", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "complete two plan items", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := formatTime(task.UpdatedAt)
	if _, _, err := createInitialRunPlan(ctx, tx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, project.ID, task.ID, task.RunID, core.CreateTaskInput{Goal: task.Goal}, now); err != nil {
		t.Fatal(err)
	}
	var revisionID, firstItemID string
	if err := tx.QueryRowContext(ctx, `SELECT p.id, i.id FROM run_plan_revisions p JOIN run_plan_items i ON i.plan_revision_id = p.id WHERE p.run_id = ?`, task.RunID).Scan(&revisionID, &firstItemID); err != nil {
		t.Fatal(err)
	}
	secondItemID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_plan_items (id, organization_id, project_id, task_id, run_id, plan_revision_id, position, title, description, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 2, 'second outcome', 'verify the second outcome', 'pending', ?, ?)`, secondItemID, task.OrganizationID, task.ProjectID, task.ID, task.RunID, revisionID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'waiting_user' WHERE id = ?`, firstItemID); err != nil {
		t.Fatal(err)
	}
	run, err := getRunContextTx(ctx, tx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.syncRunPlanState(ctx, tx, store.queries.WithTx(tx), run, core.StatusWaitingApproval, "waiting", now, "plan-wait-test"); err != nil {
		t.Fatal(err)
	}
	var firstStatus, secondStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM run_plan_items WHERE id = ?`, firstItemID).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT status FROM run_plan_items WHERE id = ?`, secondItemID).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if firstStatus != core.RunPlanItemWaitingUser || secondStatus != core.RunPlanItemPending {
		t.Fatalf("plan statuses first=%s second=%s", firstStatus, secondStatus)
	}
}
