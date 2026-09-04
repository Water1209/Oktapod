package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestCompletedWebSearchAfterSynthesisQueuesModelContinuation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000901", DisplayName: "Web Search Owner"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000902", Name: "Web Search Org"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(store, core.ProfilePersonal, "http://127.0.0.1:1/v1", actor, organization, func() string { return "" })
	project, err := service.CreateProject(ctx, core.CreateProjectInput{Name: "Web Search Continuation", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(ctx, project.ID, core.CreateTaskInput{Goal: "search and synthesize", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "")
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	modelStep, err := store.ClaimNextStep(ctx, "web-synthesis-planner", now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	nowText := formatTime(now)
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'succeeded', output_json = '{"content":"search the web"}', owner_id = '', lease_until = '', updated_at = ? WHERE id = ?`, nowText, modelStep.ID); err != nil {
		t.Fatal(err)
	}
	run, err := getRunContextTx(ctx, tx, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	status, _, err := store.planToolSteps(ctx, tx, store.queries.WithTx(tx), modelStep, run, []core.ToolCall{
		{Name: core.ToolReplacePlan, Arguments: map[string]any{"items": []any{
			map[string]any{"title": "Search and synthesize evidence", "description": "Run the governed search and continue from its result."},
		}}},
		{Name: core.ToolWebSearch, Arguments: map[string]any{"query": "final search"}},
	}, nowText, "web-synthesis-plan")
	if err != nil {
		t.Fatal(err)
	}
	if status != core.StatusQueued {
		t.Fatalf("planned search status = %s, want queued", status)
	}
	var toolStepID, toolBatchID string
	if err := tx.QueryRowContext(ctx, `SELECT id, tool_batch_id FROM steps WHERE run_id = ? AND step_type = ?`, task.RunID, core.ToolWebSearch).Scan(&toolStepID, &toolBatchID); err != nil {
		t.Fatal(err)
	}

	// Simulate an earlier synthesis pass plus enough successful searches to
	// reach the synthesis threshold again. The durable run already contains
	// model_synthesis, so this batch must fall back to model_continue.
	synthesisStepID, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, output_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 99, 'succeeded', '{"search_results":[]}', '{"content":"first synthesis"}', ?, ?)`,
		synthesisStepID, run.OrganizationID, run.ProjectID, run.RunID, core.StepModelSynthesis, nowText, nowText,
	); err != nil {
		t.Fatal(err)
	}
	argumentsJSON, err := json.Marshal(map[string]any{"query": "earlier search"})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 11; index++ {
		toolCallID, callErr := newID()
		if callErr != nil {
			t.Fatal(callErr)
		}
		receiptID, receiptErr := newID()
		if receiptErr != nil {
			t.Fatal(receiptErr)
		}
		invocationID, invocationErr := newID()
		if invocationErr != nil {
			t.Fatal(invocationErr)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tool_calls (
				id, organization_id, project_id, task_id, run_id, step_id, position,
				tool_name, arguments_json, arguments_hash, risk_level, policy_decision,
				status, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'L1', 'allow', 'succeeded', ?, ?)`,
			toolCallID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, toolStepID, 100+index,
			core.ToolWebSearch, string(argumentsJSON), fmt.Sprintf("earlier-search-%d", index), nowText, nowText,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO side_effect_receipts (
				id, organization_id, project_id, task_id, run_id, step_id, tool_call_id,
				invocation_id, input_hash, idempotent, status, exit_code, output_json, started_at, finished_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'succeeded', 0, ?, ?, ?)`,
			receiptID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, toolStepID, toolCallID,
			invocationID, fmt.Sprintf("earlier-receipt-%d", index), `{"results":[{"title":"earlier result"}]}`, nowText, nowText,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	toolStep, err := store.ClaimNextStep(ctx, "web-synthesis-worker", now.Add(time.Second), now.Add(time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if toolStep.ID != toolStepID || toolStep.StepType != core.ToolWebSearch {
		t.Fatalf("claimed step = %s/%s, want %s", toolStep.ID, toolStep.StepType, toolStepID)
	}
	if toolStep.AttemptID == "" || toolStep.FenceToken == 0 {
		t.Fatalf("claimed step attempt=%s fence=%d, want a valid lease", toolStep.AttemptID, toolStep.FenceToken)
	}
	workerRun, err := store.GetRunContext(ctx, toolStep.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStepStarted(ctx, toolStep, workerRun, "web-synthesis-start"); err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		ToolCallID string         `json:"tool_call_id"`
		Name       string         `json:"name"`
		Arguments  map[string]any `json:"arguments"`
		InputHash  string         `json:"input_hash"`
	}
	if err := json.Unmarshal([]byte(toolStep.InputJSON), &persisted); err != nil {
		t.Fatal(err)
	}
	call := core.ToolCall{ID: persisted.ToolCallID, Name: persisted.Name, Arguments: persisted.Arguments}
	invocationID, err := store.BeginToolExecution(ctx, toolStep, workerRun, call, persisted.InputHash, true, "web-synthesis-begin")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishStep(ctx, toolStep, workerRun, core.StepOutcome{
		Status: core.StatusSucceeded,
		Output: map[string]any{"results": []any{map[string]any{"title": "final result"}}},
		ToolExecution: &core.ToolExecution{
			ToolCallID:   persisted.ToolCallID,
			InvocationID: invocationID,
			Status:       core.StatusSucceeded,
			Output:       map[string]any{"results": []any{map[string]any{"title": "final result"}}},
			InputHash:    persisted.InputHash,
			Idempotent:   true,
		},
	}, "web-synthesis-finish"); err != nil {
		t.Fatalf("FinishStep returned the old deadlock: %v", err)
	}

	var taskStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus); err != nil {
		t.Fatal(err)
	}
	if taskStatus != core.StatusRunning {
		t.Fatalf("task status = %s, want running", taskStatus)
	}
	var continuationStepID, continuationStepType, continuationStatus string
	if err := store.db.QueryRowContext(ctx, `
		SELECT steps.id, steps.step_type, steps.status
		FROM tool_batches batches
		JOIN steps ON steps.id = batches.continuation_step_id
		WHERE batches.id = ?`, toolBatchID,
	).Scan(&continuationStepID, &continuationStepType, &continuationStatus); err != nil {
		t.Fatal(err)
	}
	if continuationStepID == "" || continuationStepType != core.StepModelContinue || continuationStatus != core.StatusQueued {
		t.Fatalf("continuation = %s/%s/%s, want queued %s", continuationStepID, continuationStepType, continuationStatus, core.StepModelContinue)
	}
}
