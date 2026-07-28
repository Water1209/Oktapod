package integration_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTaskBudgetIsPersistedWithRunAndVisibleToGuard(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Budget project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "bounded task", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 2, MaxTokens: 4096}, "budget-correlation-1", "budget-task-1")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+"?")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var calls, tokens int64
	if err := db.QueryRow(`SELECT max_model_calls, max_tokens FROM run_budgets WHERE run_id = ?`, task.RunID).Scan(&calls, &tokens); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || tokens != 4096 {
		t.Fatalf("budget=%d/%d", calls, tokens)
	}
}

func TestTaskBudgetInheritsProjectDefaultsAndFreezesPerRun(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Project budget defaults", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	if project.MaxModelCalls != core.DefaultTaskMaxModelCalls || project.MaxTokens != core.DefaultTaskMaxTokens {
		t.Fatalf("new project budget=%d/%d", project.MaxModelCalls, project.MaxTokens)
	}
	firstTask, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "inherit controlled project budget", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "budget-project-default", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+"?")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var calls, tokens int64
	if err := db.QueryRow(`SELECT max_model_calls, max_tokens FROM run_budgets WHERE run_id = ?`, firstTask.RunID).Scan(&calls, &tokens); err != nil {
		t.Fatal(err)
	}
	if calls != core.DefaultTaskMaxModelCalls || tokens != core.DefaultTaskMaxTokens {
		t.Fatalf("default budget=%d/%d", calls, tokens)
	}
	unlimitedCalls, unlimitedTokens := int64(0), int64(0)
	updated, err := service.UpdateProjectSettings(context.Background(), project.ID, core.UpdateProjectSettingsInput{MaxModelCalls: &unlimitedCalls, MaxTokens: &unlimitedTokens}, "project-budget-unlimited")
	if err != nil || updated.MaxModelCalls != 0 || updated.MaxTokens != 0 {
		t.Fatalf("unlimited project=%#v err=%v", updated, err)
	}
	secondTask, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "inherit unlimited project budget", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "budget-project-unlimited", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT max_model_calls, max_tokens FROM run_budgets WHERE run_id = ?`, secondTask.RunID).Scan(&calls, &tokens); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || tokens != 0 {
		t.Fatalf("unlimited run budget=%d/%d", calls, tokens)
	}
	if err := db.QueryRow(`SELECT max_model_calls, max_tokens FROM run_budgets WHERE run_id = ?`, firstTask.RunID).Scan(&calls, &tokens); err != nil {
		t.Fatal(err)
	}
	if calls != core.DefaultTaskMaxModelCalls || tokens != core.DefaultTaskMaxTokens {
		t.Fatalf("project update rewrote frozen run budget=%d/%d", calls, tokens)
	}
	partialTokens := int64(4096)
	if _, err := service.UpdateProjectSettings(context.Background(), project.ID, core.UpdateProjectSettingsInput{MaxModelCalls: &unlimitedCalls, MaxTokens: &partialTokens}, "project-budget-partial"); err == nil {
		t.Fatal("project accepted a partially unlimited budget")
	}
}

func TestRunBudgetExtensionRequiresApprovalAndLeavesAuditEvent(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Budget approval", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "bounded task", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 2, MaxTokens: 4096}, "budget-correlation-2", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateRunBudget(context.Background(), task.RunID, core.UpdateRunBudgetInput{MaxModelCalls: 3, MaxTokens: 8192}, "budget-extension-denied"); err == nil {
		t.Fatal("budget extension without approval succeeded")
	}
	usage, err := service.UpdateRunBudget(context.Background(), task.RunID, core.UpdateRunBudgetInput{MaxModelCalls: 3, MaxTokens: 8192, Approve: true}, "budget-extension-approved")
	if err != nil {
		t.Fatal(err)
	}
	if usage.BudgetStatus != "approved_exception" || usage.BudgetMaxCalls != 3 || usage.BudgetMaxTokens != 8192 {
		t.Fatalf("usage=%#v", usage)
	}
	if _, err := service.UpdateRunBudget(context.Background(), task.RunID, core.UpdateRunBudgetInput{}, "budget-unlimited-denied"); err == nil {
		t.Fatal("removing a Run budget without approval succeeded")
	}
	usage, err = service.UpdateRunBudget(context.Background(), task.RunID, core.UpdateRunBudgetInput{Approve: true}, "budget-unlimited-approved")
	if err != nil || usage.BudgetStatus != "approved_exception" || usage.BudgetMaxCalls != 0 || usage.BudgetMaxTokens != 0 {
		t.Fatalf("unlimited usage=%#v err=%v", usage, err)
	}
	if _, err := service.UpdateRunBudget(context.Background(), task.RunID, core.UpdateRunBudgetInput{MaxModelCalls: 1, MaxTokens: 1024}, "budget-shrink"); err != nil {
		t.Fatal(err)
	}
	usage, err = service.GetRunUsage(context.Background(), task.RunID)
	if err != nil || usage.BudgetStatus != "active" || usage.BudgetMaxCalls != 1 {
		t.Fatalf("shrunk usage=%#v err=%v", usage, err)
	}
}

func TestUnlimitedBudgetAllowsReservation(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Unlimited reservation", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	unlimited := int64(0)
	if _, err := service.UpdateProjectSettings(context.Background(), project.ID, core.UpdateProjectSettingsInput{MaxModelCalls: &unlimited, MaxTokens: &unlimited}, "unlimited-reservation-project"); err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "reserve without ceiling", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "budget-unlimited-reserve", "")
	if err != nil {
		t.Fatal(err)
	}
	step, err := repository.ClaimNextStep(context.Background(), "unlimited-worker", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.GetRunContext(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ReserveModelBudget(context.Background(), run, step, 50_000_000, "unlimited-reserve"); err != nil {
		t.Fatalf("unlimited reservation failed: %v", err)
	}
}

func TestModelBudgetReservationBlocksConcurrentOvercommit(t *testing.T) {
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Reservation project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "reserve", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, MaxModelCalls: 1, MaxTokens: 1536}, "budget-reserve", "")
	if err != nil {
		t.Fatal(err)
	}
	step, err := repository.ClaimNextStep(context.Background(), "reservation-worker", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.GetRunContext(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 2)
	var reservations sync.WaitGroup
	for _, correlationID := range []string{"reserve-1", "reserve-replay"} {
		reservations.Add(1)
		go func() {
			defer reservations.Done()
			<-start
			errors <- repository.ReserveModelBudget(context.Background(), run, step, 1536, correlationID)
		}()
	}
	close(start)
	reservations.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent idempotent reservation failed: %v", err)
		}
	}
	if _, err := repository.ClaimNextStep(context.Background(), "reservation-worker-2", time.Now().UTC(), time.Now().UTC().Add(time.Minute)); err == nil {
		t.Fatal("expected no second step in the single-step run")
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+"?")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var reservedCalls, reservedTokens int64
	if err := db.QueryRow(`SELECT reserved_model_calls, reserved_tokens FROM run_budgets WHERE run_id = ?`, task.RunID).Scan(&reservedCalls, &reservedTokens); err != nil {
		t.Fatal(err)
	}
	if reservedCalls != 1 || reservedTokens != 1536 {
		t.Fatalf("reservation=%d/%d", reservedCalls, reservedTokens)
	}
}
