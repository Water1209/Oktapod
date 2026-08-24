package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestMCPApprovalCanBeBoundToExactToolForCurrentRun(t *testing.T) {
	ctx := context.Background()
	store, service, scope, projectID, _ := setupWebFetchApprovalStore(t, ctx)
	task := createWebFetchApprovalTask(t, ctx, service, projectID, core.PermissionModeFullAccess)

	var snapshotJSON string
	if err := store.db.QueryRow(`SELECT config_snapshot_json FROM runs WHERE id = ?`, task.RunID).Scan(&snapshotJSON); err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["mcp_capabilities"] = []any{map[string]any{
		"id": "playwright-capability", "name": "@playwright/mcp",
		"tools": []any{
			map[string]any{"name": "browser_navigate", "description": "Navigate", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{}},
			map[string]any{"name": "browser_evaluate", "description": "Evaluate", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{}},
		},
	}}
	updatedSnapshot, _ := json.Marshal(snapshot)
	if _, err := store.db.Exec(`UPDATE runs SET config_snapshot_json = ? WHERE id = ?`, string(updatedSnapshot), task.RunID); err != nil {
		t.Fatal(err)
	}

	navigate := "mcp__playwright-capability__browser_navigate"
	if status := planMCPForTest(t, ctx, store, task.RunID, navigate, map[string]any{"url": "https://example.test/first"}, "first"); status != core.StatusWaitingApproval {
		t.Fatalf("first MCP plan status=%s", status)
	}
	approvals, err := store.ListApprovals(ctx, scope, core.ApprovalPending, 10)
	if err != nil || len(approvals) != 1 || approvals[0].ApprovalScope != core.ApprovalScopeRunTool {
		t.Fatalf("approvals=%#v err=%v", approvals, err)
	}
	var title string
	if err := store.db.QueryRow(`SELECT title FROM human_action_requests WHERE id = ?`, approvals[0].ID).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(title, "playwright-capability") || !strings.Contains(title, "@playwright/mcp") {
		t.Fatalf("human action title=%q", title)
	}
	if _, err := store.DecideApproval(ctx, scope, approvals[0].ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Scope: core.ApprovalScopeRunTool}, "approve-mcp-run-tool", core.Idempotency{}); err != nil {
		t.Fatal(err)
	}

	if status := planMCPForTest(t, ctx, store, task.RunID, navigate, map[string]any{"url": "https://example.test/second"}, "same-tool"); status != core.StatusQueued {
		t.Fatalf("same MCP tool required another approval: %s", status)
	}
	if status := planMCPForTest(t, ctx, store, task.RunID, "mcp__playwright-capability__browser_evaluate", map[string]any{"function": "() => document.title"}, "different-tool"); status != core.StatusWaitingApproval {
		t.Fatalf("different MCP tool reused exact-tool grant: %s", status)
	}

	var stepID, toolCallID, inputHash string
	if err := store.db.QueryRow(`SELECT step_id, id, arguments_hash FROM tool_calls WHERE run_id = ? AND tool_name = ? AND status = 'queued' ORDER BY position DESC LIMIT 1`, task.RunID, navigate).Scan(&stepID, &toolCallID, &inputHash); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE steps SET status = 'running', owner_id = 'mcp-worker', fence_token = 1 WHERE id = ?`, stepID); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRunContext(ctx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	step := core.ClaimedStep{ID: stepID, RunID: task.RunID, StepType: navigate, OwnerID: "mcp-worker", FenceToken: 1}
	if _, err := store.BeginToolExecution(ctx, step, run, core.ToolCall{ID: toolCallID, Name: navigate, Arguments: map[string]any{"url": "https://example.test/second"}}, inputHash, false, "mcp-begin"); err != nil {
		t.Fatal(err)
	}
	var authorizationID string
	if err := store.db.QueryRow(`SELECT authorization_id FROM side_effect_receipts WHERE tool_call_id = ?`, toolCallID).Scan(&authorizationID); err != nil || authorizationID == "" {
		t.Fatalf("receipt authorization=%q err=%v", authorizationID, err)
	}
}

func planMCPForTest(t *testing.T, ctx context.Context, store *Store, runID, toolName string, arguments map[string]any, label string) string {
	t.Helper()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	run, err := getRunContextTx(ctx, tx, runID)
	if err != nil {
		t.Fatal(err)
	}
	stepID, _ := newID()
	now := formatTime(time.Now().UTC())
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, runID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at) VALUES (?, ?, ?, ?, 'model_response', ?, 'succeeded', '{}', ?, ?)`, stepID, run.OrganizationID, run.ProjectID, runID, position, now, now); err != nil {
		t.Fatal(err)
	}
	modelStep := core.ClaimedStep{ID: stepID, RunID: runID, StepType: "model_response", InputJSON: "{}", OwnerID: "mcp-planner-" + label}
	calls := []core.ToolCall{{Name: toolName, Arguments: arguments}}
	if revisionID, itemID := activeExecutionPlanBinding(ctx, tx, runID); revisionID == "" || itemID == "" {
		calls = append([]core.ToolCall{{Name: core.ToolReplacePlan, Arguments: map[string]any{"items": []any{map[string]any{"title": "Use the selected MCP tool", "description": "Execute the exact frozen MCP capability and retain receipt evidence."}}}}}, calls...)
	}
	status, _, err := store.planToolSteps(ctx, tx, store.queries.WithTx(tx), modelStep, run, calls, now, "mcp-plan-"+label)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return status
}
