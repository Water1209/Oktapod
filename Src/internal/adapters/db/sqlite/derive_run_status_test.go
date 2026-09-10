package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// TestDeriveRunStatusIgnoresSettledPolicyRepairs guards the run-status
// derivation against stale POLICY_REPAIR_QUEUED rejections. A denied tool that
// already drove its bounded model-repair loop must not re-block the Run after
// every later successful tool step, abandoning still-queued allowed steps.
func TestDeriveRunStatusIgnoresSettledPolicyRepairs(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "oktapod.db"), filepath.Join(t.TempDir(), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	actor := core.Actor{ID: "00000000-0000-7000-0000-000000000701", DisplayName: "Derive User"}
	org := core.Organization{ID: "00000000-0000-7000-0000-000000000702", Name: "Derive Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Derive project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "produce the report", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "derive-run", "")
	if err != nil {
		t.Fatal(err)
	}
	var stepID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM steps WHERE run_id = ? ORDER BY position LIMIT 1`, task.RunID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	now := "2026-08-18T00:00:00Z"
	seedCall := func(id, status, errorCode string) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, error_code, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 1, 'workspace_run_command', '{}', 'sha256:x', 'L2', 'deny', ?, ?, ?, ?)`,
			id, task.OrganizationID, task.ProjectID, task.ID, task.RunID, stepID, status, errorCode, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}
	seedCall("00000000-0000-7000-0000-000000000711", "rejected", "POLICY_REPAIR_QUEUED")
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, error_code, created_at, updated_at)
		VALUES ('00000000-0000-7000-0000-000000000712', ?, ?, ?, ?, ?, 2, 'workspace_run_command', '{}', 'sha256:y', 'L2', 'allow', 'queued', '', ?, ?)`,
		task.OrganizationID, task.ProjectID, task.ID, task.RunID, stepID, now, now,
	); err != nil {
		t.Fatal(err)
	}

	derive := func() (string, string) {
		t.Helper()
		tx, txErr := store.db.BeginTx(ctx, nil)
		if txErr != nil {
			t.Fatal(txErr)
		}
		defer func() { _ = tx.Rollback() }()
		status, code, deriveErr := deriveRunStatus(ctx, tx, task.RunID)
		if deriveErr != nil {
			t.Fatal(deriveErr)
		}
		return status, code
	}

	if status, code := derive(); status != core.StatusRunning || code != "" {
		t.Fatalf("settled policy repair must not block a run with queued work: status=%s code=%s", status, code)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE tool_calls SET error_code = 'APPROVAL_REJECTED' WHERE id = '00000000-0000-7000-0000-000000000711'`); err != nil {
		t.Fatal(err)
	}
	if status, code := derive(); status != core.StatusBlocked || code != "TOOL_POLICY_DENIED" {
		t.Fatalf("a genuine unresolved rejection must still block the run: status=%s code=%s", status, code)
	}

	// Exhausted automatic repairs must not consume a later explicit retry.
	if _, err := store.db.ExecContext(ctx, `UPDATE steps SET input_json = '{"model_retry_count":3,"history":[{"content":"retained evidence"}]}' WHERE id = ?`, stepID); err != nil {
		t.Fatal(err)
	}
	frontierID, _ := newID()
	frontierInput, _ := json.Marshal(map[string]any{"model_source_step_id": stepID})
	if _, err := store.db.ExecContext(ctx, `INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, tool_batch_id, created_at, updated_at) VALUES (?, ?, ?, ?, 'workspace_run_command', 2, 'blocked', ?, 'denied-batch', ?, ?)`, frontierID, task.OrganizationID, task.ProjectID, task.RunID, string(frontierInput), now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE tool_calls SET step_id = ?, status = 'blocked', tool_batch_id = 'denied-batch', arguments_json = '{"program":"powershell.exe","args":["-Command","dir"]}', error_code = '' WHERE position = 1 AND run_id = ?`, frontierID, task.RunID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	result, repaired, err := store.retryDeniedToolThroughModel(ctx, tx, task.RunID, taskControlStep{ID: frontierID, InputJSON: string(frontierInput)}, now, "manual-repair")
	if err != nil || !repaired {
		t.Fatalf("manual retry failed after exhausted automatic retries: %v", err)
	}
	var encoded string
	if err := tx.QueryRowContext(ctx, `SELECT input_json FROM steps WHERE id = ?`, result["step_id"]).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var restored struct {
		Count   int   `json:"model_retry_count"`
		Manual  bool  `json:"manual_retry"`
		History []any `json:"history"`
		Denials []struct {
			Reason string `json:"reason"`
		} `json:"model_policy_denials"`
	}
	if err := json.Unmarshal([]byte(encoded), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Count != 1 || !restored.Manual || len(restored.History) != 1 || len(restored.Denials) != 1 || restored.Denials[0].Reason == "" {
		t.Fatalf("bad manual recovery context: %s", encoded)
	}
	var deniedStatus, originalCount string
	_ = tx.QueryRowContext(ctx, `SELECT status FROM steps WHERE id = ?`, frontierID).Scan(&deniedStatus)
	_ = tx.QueryRowContext(ctx, `SELECT json_extract(input_json, '$.model_retry_count') FROM steps WHERE id = ?`, stepID).Scan(&originalCount)
	if deniedStatus != "canceled" || originalCount != "3" {
		t.Fatalf("denied invocation replayed or historical budget mutated: %s %s", deniedStatus, originalCount)
	}
}
