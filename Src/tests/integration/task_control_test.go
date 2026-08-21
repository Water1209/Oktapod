package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTaskRetryResumesFailedCheckpoint(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Retry User"}, core.Organization{ID: testOrgID, Name: "Retry Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "retry from checkpoint", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "retry-task", "")
	if err != nil {
		t.Fatal(err)
	}
	markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusFailed, core.StatusFailed)
	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "retry-control", "retry-control-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "run.retried" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("run.retried missing from events=%#v", events)
	}
}

func TestTaskRetryResetsFrozenToolCallWithFailedFrontier(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Retry Tool User"}, core.Organization{ID: testOrgID, Name: "Retry Tool Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry tool project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "retry failed search", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "retry-tool-task", "")
	if err != nil {
		t.Fatal(err)
	}
	stepID := markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusFailed, core.StatusFailed)
	db := openTaskControlDatabase(t, databasePath)
	toolCallID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, error_code, created_at, updated_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, 99, 'web_search', '{}', 'hash:search', 'L0', 'allow', 'failed', 'WEB_SEARCH_UNAVAILABLE', ?, ?, ?)`, toolCallID, testOrgID, project.ID, task.ID, task.RunID, stepID, now, now, now); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "retry-tool-control", "retry-tool-control-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	db = openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var stepStatus, toolStatus, toolError, completedAt string
	if err := db.QueryRow(`SELECT status, error_code, completed_at FROM tool_calls WHERE id = ?`, toolCallID).Scan(&toolStatus, &toolError, &completedAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if stepStatus != core.StatusQueued || toolStatus != core.StatusQueued || toolError != "" || completedAt != "" {
		t.Fatalf("retry checkpoint step=%q tool=%q error=%q completed_at=%q", stepStatus, toolStatus, toolError, completedAt)
	}
}

func TestTaskRetryRecreatesPlanRecoveryCheckpoint(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Plan Recovery User"}, core.Organization{ID: testOrgID, Name: "Plan Recovery Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Plan recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "resume a rejected plan update", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "plan-recovery-task", "")
	if err != nil {
		t.Fatal(err)
	}
	db := openTaskControlDatabase(t, databasePath)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE steps SET status = 'succeeded', error_code = '', updated_at = ? WHERE run_id = ?`, now, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status = 'blocked', error_code = 'RUN_PLAN_STATUS_REGRESSION', updated_at = ? WHERE id = ?`, now, task.ID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET status = 'blocked', updated_at = ? WHERE id = ?`, now, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE run_plan_revisions SET status = 'blocked' WHERE run_id = ?`, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE run_plan_items SET status = 'blocked', updated_at = ? WHERE plan_revision_id = (SELECT id FROM run_plan_revisions WHERE run_id = ? ORDER BY revision DESC LIMIT 1)`, now, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "plan-recovery", "plan-recovery-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	db = openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var status, inputJSON, planRevisionID, planItemID string
	if err := db.QueryRow(`
		SELECT status, input_json, plan_revision_id, run_plan_item_id
		FROM steps WHERE run_id = ? ORDER BY position DESC LIMIT 1`, task.RunID).Scan(&status, &inputJSON, &planRevisionID, &planItemID); err != nil {
		t.Fatal(err)
	}
	if status != core.StatusQueued || planRevisionID == "" || planItemID == "" {
		t.Fatalf("recovery checkpoint status=%q plan=%q item=%q", status, planRevisionID, planItemID)
	}
	if !strings.Contains(inputJSON, `"model_retry_reason":"RUN_PLAN_STATUS_REGRESSION"`) || !strings.Contains(inputJSON, `"manual_retry":true`) {
		t.Fatalf("unexpected recovery input=%s", inputJSON)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "run.retried" && event.Payload["retry_scope"] == "plan_recovery" {
			found = true
		}
	}
	if !found {
		t.Fatalf("plan recovery run.retried missing from events=%#v", events)
	}
}

func TestTaskRetryAppendsCompletionValidationRecoveryCheckpoint(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Completion Recovery User"}, core.Organization{ID: testOrgID, Name: "Completion Recovery Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Completion recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "不要修改任何文件；只需回复 OK。", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "completion-recovery-task", "")
	if err != nil {
		t.Fatal(err)
	}
	db := openTaskControlDatabase(t, databasePath)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var sourceStepID string
	if err := db.QueryRow(`SELECT id FROM steps WHERE run_id = ? ORDER BY position LIMIT 1`, task.RunID).Scan(&sourceStepID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE steps SET status = 'succeeded', input_json = '{"stage":"completion_recovery","completion_recovery_reason":"WORKSPACE_MUTATION_MISSING","history":[]}', output_json = '{"content":"OK"}', updated_at = ? WHERE id = ?`, now, sourceStepID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status = 'blocked', error_code = 'WORKSPACE_MUTATION_MISSING', result_summary = 'missing mutation', updated_at = ? WHERE id = ?`, now, task.ID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET status = 'blocked', updated_at = ? WHERE id = ?`, now, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "completion-recovery", "completion-recovery-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	db = openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var sourceStatus, recoveryStepID, recoveryStatus, recoveryInput string
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, sourceStepID).Scan(&sourceStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT id, status, input_json FROM steps WHERE run_id = ? ORDER BY position DESC LIMIT 1`, task.RunID).Scan(&recoveryStepID, &recoveryStatus, &recoveryInput); err != nil {
		t.Fatal(err)
	}
	if sourceStatus != core.StatusSucceeded || recoveryStepID == sourceStepID || recoveryStatus != core.StatusQueued {
		t.Fatalf("source=%q recovery=%q status=%q", sourceStatus, recoveryStepID, recoveryStatus)
	}
	if !strings.Contains(recoveryInput, `"stage":"completion_recovery"`) ||
		!strings.Contains(recoveryInput, `"model_retry_reason":"WORKSPACE_MUTATION_MISSING"`) ||
		!strings.Contains(recoveryInput, `"manual_retry":true`) {
		t.Fatalf("unexpected recovery input=%s", recoveryInput)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "run.retried" && event.Payload["retry_scope"] == "completion_validation_recovery" && event.Payload["source_step_id"] == sourceStepID {
			found = true
		}
	}
	if !found {
		t.Fatalf("completion recovery run.retried missing from events=%#v", events)
	}
}

func TestTaskRetryRejectsUnknownSideEffect(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Retry Safety User"}, core.Organization{ID: testOrgID, Name: "Retry Safety Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry safety", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "do not repeat an unknown side effect", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "retry-safety-task", "")
	if err != nil {
		t.Fatal(err)
	}
	stepID := markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusBlocked, core.StatusFailed)
	insertUnknownReceipt(t, databasePath, project.ID, task.ID, task.RunID, stepID)
	_, err = service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "retry-safety", "retry-safety-key")
	var conflict *core.ResourceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("retry with unknown side effect err=%T %v", err, err)
	}
}

func TestTaskRetryOnlyQueuesLatestRecoveryFrontier(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Retry Frontier User"}, core.Organization{ID: testOrgID, Name: "Retry Frontier Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry frontier", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "resume only the latest recovery checkpoint", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "retry-frontier-task", "")
	if err != nil {
		t.Fatal(err)
	}
	originalStepID := markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusFailed, core.StatusFailed)
	retryOneID := insertRetryStep(t, databasePath, project.ID, task.RunID, originalStepID, 1, core.StatusFailed)
	retryTwoID := insertRetryStep(t, databasePath, project.ID, task.RunID, retryOneID, 2, core.StatusQueued)

	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "retry-frontier", "retry-frontier-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	statuses := map[string]string{}
	rows, err := db.Query(`SELECT id, status FROM steps WHERE run_id = ? ORDER BY position`, task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatal(err)
		}
		statuses[id] = status
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if statuses[originalStepID] != core.StatusFailed || statuses[retryOneID] != core.StatusFailed || statuses[retryTwoID] != core.StatusQueued {
		t.Fatalf("retry reactivated historical steps: %#v", statuses)
	}
	var queued int
	if err := db.QueryRow(`SELECT COUNT(*) FROM steps WHERE run_id = ? AND status = 'queued'`, task.RunID).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 1 {
		t.Fatalf("queued steps=%d, want exactly latest frontier", queued)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "run.retried" && event.Payload["step_id"] == retryTwoID && event.Payload["retry_scope"] == "frontier_only" {
			found = true
		}
	}
	if !found {
		t.Fatalf("checkpoint-scoped run.retried missing from events=%#v", events)
	}
}

func TestTaskRetryPrefersNewerQueuedContinuationOverHistoricalFailure(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Retry Continuation User"}, core.Organization{ID: testOrgID, Name: "Retry Continuation Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry continuation", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "resume queued recovery work", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "retry-continuation-task", "")
	if err != nil {
		t.Fatal(err)
	}
	failedStepID := markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusFailed, core.StatusFailed)
	queuedStepID := insertPlainStep(t, databasePath, project.ID, task.RunID, core.StatusQueued)

	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "retry-continuation", "retry-continuation-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var failedStatus, queuedStatus string
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, failedStepID).Scan(&failedStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, queuedStepID).Scan(&queuedStatus); err != nil {
		t.Fatal(err)
	}
	if failedStatus != core.StatusFailed || queuedStatus != core.StatusQueued {
		t.Fatalf("retry replayed historical failure: failed=%q queued=%q", failedStatus, queuedStatus)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "run.retried" && event.Payload["step_id"] == queuedStepID {
			found = true
		}
	}
	if !found {
		t.Fatalf("queued continuation was not selected: events=%#v", events)
	}
}

func TestTaskRetryQueuesMissingContinuationForCompletedToolBatch(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Retry Batch User"}, core.Organization{ID: testOrgID, Name: "Retry Batch Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry completed batch", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "continue after completed tools", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "retry-completed-batch-task", "")
	if err != nil {
		t.Fatal(err)
	}
	db := openTaskControlDatabase(t, databasePath)
	var sourceStepID string
	if err := db.QueryRow(`SELECT id FROM steps WHERE run_id = ? ORDER BY position LIMIT 1`, task.RunID).Scan(&sourceStepID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	batchID, toolStepID, toolCallID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE steps SET status = 'succeeded', output_json = '{"content":"planned tools"}', updated_at = ? WHERE id = ?`, now, sourceStepID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tool_batches (id, organization_id, project_id, task_id, run_id, source_step_id, plan_iteration, execution_mode, status, tool_count, continuation_claimed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 1, 'parallel', 'running', 1, 0, ?, ?)`, batchID, testOrgID, project.ID, task.ID, task.RunID, sourceStepID, now, now); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, output_json, tool_batch_id, batch_index, created_at, updated_at) VALUES (?, ?, ?, ?, 'web_search', 2, 'succeeded', '{"name":"web_search","arguments":{"query":"evidence"}}', '{"result":"ok"}', ?, 1, ?, ?)`, toolStepID, testOrgID, project.ID, task.RunID, batchID, now, now); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, tool_batch_id, batch_index, completed_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 2, 'web_search', '{"query":"evidence"}', 'hash:evidence', 'L0', 'allow', 'succeeded', ?, 1, ?, ?, ?)`, toolCallID, testOrgID, project.ID, task.ID, task.RunID, toolStepID, batchID, now, now, now); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status = 'failed', error_code = 'TOOL_EXECUTION_FAILED', updated_at = ? WHERE id = ?`, now, task.ID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET status = 'failed', updated_at = ? WHERE id = ?`, now, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "retry-completed-batch", "retry-completed-batch-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	db = openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var batchStatus string
	var claimed int
	var continuationStepID, continuationStatus, continuationInput string
	if err := db.QueryRow(`SELECT status, continuation_claimed, continuation_step_id FROM tool_batches WHERE id = ?`, batchID).Scan(&batchStatus, &claimed, &continuationStepID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status, input_json FROM steps WHERE id = ?`, continuationStepID).Scan(&continuationStatus, &continuationInput); err != nil {
		t.Fatal(err)
	}
	if batchStatus != core.StatusSucceeded || claimed != 1 || continuationStatus != core.StatusQueued || !strings.Contains(continuationInput, `"stage":"continue"`) {
		t.Fatalf("batch=%q claimed=%d continuation=%q input=%s", batchStatus, claimed, continuationStatus, continuationInput)
	}
}

func TestTaskRetryPrefersNewerBlockedStepOverOlderRetryAttempt(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Retry Ordering User"}, core.Organization{ID: testOrgID, Name: "Retry Ordering Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Retry ordering", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "retry the current blocked frontier", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "retry-ordering-task", "")
	if err != nil {
		t.Fatal(err)
	}
	originalStepID := markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusBlocked, core.StatusFailed)
	olderRetryID := insertRetryStep(t, databasePath, project.ID, task.RunID, originalStepID, 1, core.StatusFailed)
	newerBlockedID := insertPlainStep(t, databasePath, project.ID, task.RunID, core.StatusBlocked)
	setTaskAndRunStatus(t, databasePath, task.ID, task.RunID, core.StatusBlocked)

	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "retry-ordering", "retry-ordering-key")
	if err != nil || updated.Status != core.StatusQueued {
		t.Fatalf("retry task=%#v err=%v", updated, err)
	}
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var olderStatus, newerStatus string
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, olderRetryID).Scan(&olderStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, newerBlockedID).Scan(&newerStatus); err != nil {
		t.Fatal(err)
	}
	if olderStatus != core.StatusFailed || newerStatus != core.StatusQueued {
		t.Fatalf("retry selected stale recovery: older=%s newer=%s", olderStatus, newerStatus)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.EventType == "run.retried" && event.Payload["step_id"] == newerBlockedID {
			found = true
		}
	}
	if !found {
		t.Fatalf("newest blocked frontier was not recorded: %#v", events)
	}
}

func TestTaskResumeCannotBypassBlockedRetryChecks(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Blocked Resume User"}, core.Organization{ID: testOrgID, Name: "Blocked Resume Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Blocked resume", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "do not bypass retry safety", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "blocked-resume-task", "")
	if err != nil {
		t.Fatal(err)
	}
	markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusBlocked, core.StatusBlocked)
	_, err = service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandResume}, "blocked-resume", "blocked-resume-key")
	var conflict *core.StateConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("resume blocked task err=%T %v", err, err)
	}
}

func TestTaskRetryRequiresBudgetIncreaseWhenRunIsExhausted(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Budget Retry User"}, core.Organization{ID: testOrgID, Name: "Budget Retry Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Budget retry", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "increase budget before retry", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "budget-retry-task", "")
	if err != nil {
		t.Fatal(err)
	}
	stepID := markTaskForRetry(t, databasePath, task.ID, task.RunID, core.StatusBlocked, core.StatusBlocked)
	db := openTaskControlDatabase(t, databasePath)
	if _, err := db.Exec(`UPDATE tasks SET error_code = 'BUDGET_RESERVATION_EXCEEDED' WHERE id = ?`, task.ID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE run_budgets SET status = 'exhausted' WHERE run_id = ?`, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	_, err = service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "budget-retry", "budget-retry-key")
	var conflict *core.ResourceConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("retry exhausted budget err=%T %v", err, err)
	}
	db = openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var status string
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, stepID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != core.StatusBlocked {
		t.Fatalf("budget retry changed checkpoint to %q", status)
	}
}

func TestTaskPauseOnlyPausesRunnableSteps(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Pause User"}, core.Organization{ID: testOrgID, Name: "Pause Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Pause scope", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "pause only runnable work", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "pause-task", "")
	if err != nil {
		t.Fatal(err)
	}
	blockedStepID := insertPlainStep(t, databasePath, project.ID, task.RunID, core.StatusBlocked)
	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandPause}, "pause-control", "pause-control-key")
	if err != nil || updated.Status != core.StatusPaused {
		t.Fatalf("pause task=%#v err=%v", updated, err)
	}
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var blockedStatus string
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, blockedStepID).Scan(&blockedStatus); err != nil {
		t.Fatal(err)
	}
	if blockedStatus != core.StatusBlocked {
		t.Fatalf("pause changed blocked step to %q", blockedStatus)
	}
}

func TestTaskCancelClosesPendingApproval(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Cancel User"}, core.Organization{ID: testOrgID, Name: "Cancel Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Cancel approval", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "cancel pending approval", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "cancel-task", "")
	if err != nil {
		t.Fatal(err)
	}
	stepID, toolCallID, approvalID := insertPendingApproval(t, databasePath, project.ID, task.ID, task.RunID)
	receiptID := insertStartedReceipt(t, databasePath, project.ID, task.ID, task.RunID, stepID, toolCallID)
	setTaskAndRunStatus(t, databasePath, task.ID, task.RunID, core.StatusWaitingApproval)
	updated, err := service.ControlTask(context.Background(), task.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "cancel-control", "cancel-control-key")
	if err != nil || updated.Status != core.StatusCanceled {
		t.Fatalf("cancel task=%#v err=%v", updated, err)
	}
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	var stepStatus, toolStatus, approvalStatus, note, receiptStatus, receiptError string
	if err := db.QueryRow(`SELECT status FROM steps WHERE id = ?`, stepID).Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM tool_calls WHERE id = ?`, toolCallID).Scan(&toolStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status, decision_note FROM approval_requests WHERE id = ?`, approvalID).Scan(&approvalStatus, &note); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status, error_code FROM side_effect_receipts WHERE id = ?`, receiptID).Scan(&receiptStatus, &receiptError); err != nil {
		t.Fatal(err)
	}
	if stepStatus != core.StatusCanceled || toolStatus != "rejected" || approvalStatus != core.ApprovalRejected || note != "任务已取消" || receiptStatus != core.SideEffectReceiptUnknown || receiptError != "SIDE_EFFECT_STATUS_UNKNOWN" {
		t.Fatalf("cancel state step=%s tool=%s approval=%s note=%q receipt=%s/%s", stepStatus, toolStatus, approvalStatus, note, receiptStatus, receiptError)
	}
}

func markTaskForRetry(t *testing.T, databasePath, taskID, runID, taskStatus, stepStatus string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var stepID string
	if err := db.QueryRow(`SELECT id FROM steps WHERE run_id = ? ORDER BY position LIMIT 1`, runID).Scan(&stepID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE steps SET status = ?, error_code = 'TEST_RETRY', updated_at = ? WHERE id = ?`, stepStatus, now, stepID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET status = ?, error_code = 'TEST_RETRY', updated_at = ? WHERE id = ?`, taskStatus, now, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, taskStatus, now, runID); err != nil {
		t.Fatal(err)
	}
	return stepID
}

func insertRetryStep(t *testing.T, databasePath, projectID, runID, sourceStepID string, retryCount int, status string) string {
	t.Helper()
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	stepID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	input := fmt.Sprintf(`{"goal":"retry checkpoint","model_retry_count":%d,"model_retry_source_step_id":%q}`, retryCount, sourceStepID)
	var position int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, runID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, error_code, created_at, updated_at) VALUES (?, ?, ?, ?, 'model_response', ?, ?, ?, 'PROVIDER_PROTOCOL_ERROR', ?, ?)`, stepID, testOrgID, projectID, runID, position, status, input, now, now); err != nil {
		t.Fatal(err)
	}
	return stepID
}

func insertPlainStep(t *testing.T, databasePath, projectID, runID, status string) string {
	t.Helper()
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	stepID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var position int64
	if err := db.QueryRow(`SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, runID).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at) VALUES (?, ?, ?, ?, 'model_response', ?, ?, '{}', ?, ?)`, stepID, testOrgID, projectID, runID, position, status, now, now); err != nil {
		t.Fatal(err)
	}
	return stepID
}

func insertPendingApproval(t *testing.T, databasePath, projectID, taskID, runID string) (string, string, string) {
	t.Helper()
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	stepID := insertPlainStep(t, databasePath, projectID, runID, core.StatusWaitingApproval)
	toolCallID := uuid.NewString()
	approvalID := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 99, 'test_external', '{}', 'hash:approval', 'L2', 'ask', 'waiting_approval', ?, ?)`, toolCallID, testOrgID, projectID, taskID, runID, stepID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO approval_requests (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, tool_name, risk_level, status, reason, frozen_params_json, input_hash, requested_by, requested_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'test_external', 'L2', 'pending', 'test approval', '{}', 'hash:approval', ?, ?)`, approvalID, testOrgID, projectID, taskID, runID, stepID, toolCallID, testUserID, now); err != nil {
		t.Fatal(err)
	}
	return stepID, toolCallID, approvalID
}

func setTaskAndRunStatus(t *testing.T, databasePath, taskID, runID, status string) {
	t.Helper()
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE tasks SET status = ?, updated_at = ? WHERE id = ?`, status, now, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, status, now, runID); err != nil {
		t.Fatal(err)
	}
}

func openTaskControlDatabase(t *testing.T, databasePath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func insertUnknownReceipt(t *testing.T, databasePath, projectID, taskID, runID, stepID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	toolCallID := uuid.NewString()
	receiptID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, error_code, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 99, 'test_external', '{}', 'hash:test', 'L2', 'allow', 'failed', '', ?, ?)`, toolCallID, testOrgID, projectID, taskID, runID, stepID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO side_effect_receipts (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, invocation_id, input_hash, idempotent, status, exit_code, output_json, error_code, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'hash:test', 0, 'unknown', -1, '{}', 'UNKNOWN', ?, '')`, receiptID, testOrgID, projectID, taskID, runID, stepID, toolCallID, uuid.NewString(), now); err != nil {
		t.Fatal(err)
	}
}

func insertStartedReceipt(t *testing.T, databasePath, projectID, taskID, runID, stepID, toolCallID string) string {
	t.Helper()
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	receiptID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO side_effect_receipts (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, invocation_id, input_hash, idempotent, status, exit_code, output_json, error_code, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'hash:test', 0, 'started', -1, '{}', '', ?, '')`, receiptID, testOrgID, projectID, taskID, runID, stepID, toolCallID, uuid.NewString(), now); err != nil {
		t.Fatal(err)
	}
	return receiptID
}
