package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestDeleteProjectSoftDeletesAndRetainsHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000811", DisplayName: "Deletion Owner"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000812", Name: "Deletion Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	scope := core.Scope{OrganizationID: organization.ID, UserID: actor.ID}
	project, err := store.CreateProject(ctx, scope, core.CreateProjectInput{Name: "Deletion", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, core.Idempotency{})
	if err != nil {
		t.Fatal(err)
	}
	task := seedProjectStrictForeignKeys(t, ctx, store, scope, project.ID)

	if err := store.DeleteProject(ctx, scope, project.ID, "delete-project"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetProject(ctx, scope, project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get soft-deleted project = %v, want sql.ErrNoRows", err)
	}
	projects, err := store.ListProjects(ctx, scope, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range projects {
		if listed.ID == project.ID {
			t.Fatal("soft-deleted project is still listed")
		}
	}
	for _, statement := range []string{
		`SELECT COUNT(*) FROM tasks WHERE project_id = ?`,
		`SELECT COUNT(*) FROM runs WHERE project_id = ?`,
		`SELECT COUNT(*) FROM model_calls WHERE project_id = ?`,
		`SELECT COUNT(*) FROM usage_ledger WHERE project_id = ?`,
		`SELECT COUNT(*) FROM team_definitions WHERE project_id = ?`,
		`SELECT COUNT(*) FROM run_approval_rules WHERE project_id = ?`,
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, statement, project.ID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("project history was removed (%s)", statement)
		}
	}
	var status, auditDetail string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM projects WHERE id = ?`, project.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" {
		t.Fatalf("project status = %q, want deleted", status)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT detail_json FROM audit_events WHERE project_id = ? AND action = 'project.deleted'`, project.ID).Scan(&auditDetail); err != nil {
		t.Fatal(err)
	}
	if auditDetail != `{"mode":"soft"}` {
		t.Fatalf("project.deleted audit detail = %q", auditDetail)
	}
	if task.ID == "" {
		t.Fatal("seed task was not retained")
	}
}

func TestDeleteProjectRejectsActiveTask(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000821", DisplayName: "Active Owner"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000822", Name: "Active Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Active", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "active task", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", ""); err != nil {
		t.Fatal(err)
	}
	scope := core.Scope{OrganizationID: organization.ID, UserID: actor.ID}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE project_id = ?`, core.StatusBlocked, project.ID); err != nil {
		t.Fatal(err)
	}
	tasks, err := service.ListTasks(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks returned = %d, want 1", len(tasks))
	}
	if tasks[0].Status != core.StatusBlocked {
		t.Fatalf("task status = %q, want blocked", tasks[0].Status)
	}
	var conflict *core.ResourceConflictError
	if err := store.DeleteProject(ctx, scope, project.ID, "delete-active"); !errors.As(err, &conflict) {
		t.Fatalf("delete with blocked task = %v, want ResourceConflictError", err)
	}
	if _, err := store.GetProject(ctx, scope, project.ID); err != nil {
		t.Fatalf("project should remain active after blocked-task rejection: %v", err)
	}
}

func TestDeleteProjectRejectsNonOwner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000831", DisplayName: "Project Owner"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000832", Name: "Non-owner Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Non-owner", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	memberID := "00000000-0000-7000-8000-000000000833"
	now := formatTime(time.Now())
	if _, err := store.db.ExecContext(ctx, `INSERT INTO users (id, display_name, created_at, updated_at) VALUES (?, 'Project Member', ?, ?)`, memberID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)`, organization.ID, memberID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'approver', 'active', ?, ?)`, project.ID, memberID, now, now); err != nil {
		t.Fatal(err)
	}
	memberScope := core.Scope{OrganizationID: organization.ID, UserID: memberID}
	var forbidden *core.ForbiddenError
	if err := store.DeleteProject(ctx, memberScope, project.ID, "delete-non-owner"); !errors.As(err, &forbidden) {
		t.Fatalf("non-owner delete = %v, want ForbiddenError", err)
	}
	if _, err := store.GetProject(ctx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, project.ID); err != nil {
		t.Fatalf("project should remain active after rejected delete: %v", err)
	}
}

func seedProjectStrictForeignKeys(t *testing.T, ctx context.Context, store *Store, scope core.Scope, projectID string) core.Task {
	t.Helper()
	task, err := store.CreateTask(ctx, scope, projectID, core.CreateTaskInput{Goal: "seed history", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, nil, "", core.Idempotency{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tasks SET status = 'succeeded' WHERE id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	now := formatTime(time.Now())
	runID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO runs (id, organization_id, project_id, task_id, status, config_snapshot_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, '{}', ?, ?)`, runID, scope.OrganizationID, projectID, task.ID, core.StatusSucceeded, now, now); err != nil {
		t.Fatal(err)
	}
	stepID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, output_json, created_at, updated_at) VALUES (?, ?, ?, ?, 'model_synthesis', 0, 'succeeded', '{}', '{}', ?, ?)`, stepID, scope.OrganizationID, projectID, runID, now, now); err != nil {
		t.Fatal(err)
	}
	toolCallID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 0, 'web_search', '{}', 'seed', 'L1', 'allow', 'succeeded', ?, ?)`, toolCallID, scope.OrganizationID, projectID, task.ID, runID, stepID, now, now); err != nil {
		t.Fatal(err)
	}
	modelCallID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO model_calls (id, organization_id, project_id, task_id, run_id, step_id, provider, endpoint, model, routing_reason, status, created_at) VALUES (?, ?, ?, ?, ?, ?, 'test', '/v1', 'test-model', 'test', 'succeeded', ?)`, modelCallID, scope.OrganizationID, projectID, task.ID, runID, stepID, now); err != nil {
		t.Fatal(err)
	}
	ledgerID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO usage_ledger (id, organization_id, project_id, task_id, run_id, step_id, call_id, input_tokens, output_tokens, cached_tokens, reasoning_tokens, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, 2, 0, 0, ?)`, ledgerID, scope.OrganizationID, projectID, task.ID, runID, stepID, modelCallID, now); err != nil {
		t.Fatal(err)
	}
	teamID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO team_definitions (id, organization_id, project_id, owner_user_id, name, version, status, roles_json, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, 'Team', '1', 'enabled', '[]', '{}', ?, ?)`, teamID, scope.OrganizationID, projectID, scope.UserID, now, now); err != nil {
		t.Fatal(err)
	}
	approvalID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO approval_requests (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, tool_name, risk_level, reason, status, frozen_params_json, input_hash, requested_by, requested_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'web_search', 'L1', 'seed', 'approved', '{}', 'seed', ?, ?, '')`, approvalID, scope.OrganizationID, projectID, task.ID, runID, stepID, toolCallID, scope.UserID, now, now); err != nil {
		t.Fatal(err)
	}
	ruleID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO run_approval_rules (id, organization_id, project_id, run_id, tool_name, grant_type, grant_key, policy_hash, canonical_origin, created_from_approval_id, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, 'web_search', 'run', 'seed', 'seed', '', ?, ?, ?, ?)`, ruleID, scope.OrganizationID, projectID, runID, approvalID, scope.UserID, now, now); err != nil {
		t.Fatal(err)
	}
	return task
}
