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
