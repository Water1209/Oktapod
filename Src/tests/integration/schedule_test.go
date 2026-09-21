package integration_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestSchedulePersistsAndCreatesExactlyOneFire(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Schedule project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := service.CreateSchedule(context.Background(), project.ID, core.CreateScheduleInput{Name: "Daily digest", CronExpression: "0 9 * * *", Timezone: "UTC", Goal: "Summarize project activity", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "schedule-create-1")
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if schedule.Status != core.ScheduleStatusEnabled || schedule.NextRunAt.IsZero() {
		t.Fatalf("unexpected schedule: %#v", schedule)
	}
	replayed, err := service.CreateSchedule(context.Background(), project.ID, core.CreateScheduleInput{Name: "Daily digest", CronExpression: "0 9 * * *", Timezone: "UTC", Goal: "Summarize project activity", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "schedule-create-1")
	if err != nil || replayed.ID != schedule.ID {
		t.Fatalf("idempotency replay=%#v err=%v", replayed, err)
	}
	items, err := service.ListSchedules(context.Background(), project.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("schedules=%#v err=%v", items, err)
	}
	fire, err := repository.ClaimScheduleFire(context.Background(), schedule, schedule.NextRunAt.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("claim fire: %v", err)
	}
	if fire.Status != core.ScheduleFirePending {
		t.Fatalf("fire status=%s", fire.Status)
	}
	task, err := repository.CreateScheduledTask(context.Background(), schedule, fire, "schedule-test")
	if err != nil {
		t.Fatalf("create scheduled task: %v", err)
	}
	if task.Goal != schedule.Goal || task.ProjectID != project.ID {
		t.Fatalf("scheduled task=%#v", task)
	}
	if err := repository.AdvanceScheduleAfterFire(context.Background(), schedule.ID, schedule.NextRunAt, schedule.NextRunAt.Add(24*time.Hour), core.ScheduleFireCreated, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimScheduleFire(context.Background(), schedule, schedule.NextRunAt.Add(48*time.Hour)); err != nil {
		// the second claim updates the persisted next run and is expected to produce a new fire
		t.Fatal(err)
	}
	if updated, err := service.SetScheduleStatus(context.Background(), schedule.ID, core.ScheduleStatusDisabled, "schedule-status-1"); err != nil || updated.Status != core.ScheduleStatusDisabled {
		t.Fatalf("disable schedule=%#v err=%v", updated, err)
	}
}

func TestSubmitScheduledTurnCreatesOneDirectlyLinkedTask(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Scheduled task link", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "scheduled-task-project")
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := service.CreateSchedule(t.Context(), project.ID, core.CreateScheduleInput{
		Name: "Linked schedule", CronExpression: "* * * * *", Timezone: "UTC", Goal: "create one linked task", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate,
	}, "scheduled-task-create")
	if err != nil {
		t.Fatal(err)
	}
	fire, err := repository.ClaimScheduleFire(t.Context(), schedule, schedule.NextRunAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.SubmitScheduledTurn(t.Context(), schedule, fire)
	if err != nil {
		t.Fatalf("submit scheduled turn: %v", err)
	}
	replayed, err := service.SubmitScheduledTurn(t.Context(), schedule, fire)
	if err != nil || replayed.ID != task.ID {
		t.Fatalf("scheduled turn replay=%#v err=%v; want task %s", replayed, err, task.ID)
	}
	if task.ProjectID != project.ID || task.ConversationID == "" || task.Mode != core.ModeSingleAgent {
		t.Fatalf("scheduled task=%#v", task)
	}
	if err := repository.AdvanceScheduleAfterFire(t.Context(), schedule.ID, schedule.NextRunAt, schedule.NextRunAt.Add(time.Minute), core.ScheduleFireCreated, task.ID); err != nil {
		t.Fatal(err)
	}
	runs, err := service.ListScheduleRuns(t.Context(), schedule.ID)
	if err != nil || len(runs) != 1 || runs[0].Status != core.ScheduleFireCreated || runs[0].TaskID != task.ID {
		t.Fatalf("schedule runs=%#v err=%v", runs, err)
	}
	pending, err := repository.ListConversationQueue(t.Context(), queueTestScope(), task.ConversationID, "pending")
	if err != nil || len(pending) != 0 {
		t.Fatalf("scheduled turn unexpectedly queued follow-up=%#v err=%v", pending, err)
	}
}

// A scheduler is a replaceable process. Reopening the same store must not
// create a second fire for the same planned timestamp after the first process
// has already advanced the schedule and committed its task.
func TestSchedulerProcessRestartDoesNotDuplicateFire(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	workspaceRoot := filepath.Join(root, "workspaces")
	store, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRepository(t, store)
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Scheduler restart", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	schedule, err := service.CreateSchedule(context.Background(), project.ID, core.CreateScheduleInput{
		Name: "Restart-safe schedule", CronExpression: "* * * * *", Timezone: "UTC", Goal: "run once", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate,
	}, "scheduler-restart-create")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Make the first occurrence due, while keeping it inside the default
	// misfire grace window. This mirrors a process that was down briefly.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	dueAt := time.Now().UTC().Add(-5 * time.Second).Format(time.RFC3339Nano)
	if _, err := db.Exec(`UPDATE schedules SET next_run_at = ? WHERE id = ?`, dueAt, schedule.ID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	firstScheduler := okruntime.NewScheduler(first, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worked, err := firstScheduler.RunOnce(context.Background())
	if err != nil || !worked {
		first.Close()
		t.Fatalf("first scheduler run worked=%v err=%v", worked, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondScheduler := okruntime.NewScheduler(second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worked, err = secondScheduler.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("restarted scheduler worked=%v err=%v; expected no duplicate due fire", worked, err)
	}

	check, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var fireCount, taskCount int
	if err := check.QueryRow(`SELECT COUNT(*) FROM schedule_fires WHERE schedule_id = ?`, schedule.ID).Scan(&fireCount); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM tasks WHERE project_id = ? AND title = ?`, project.ID, schedule.Name).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if fireCount != 1 || taskCount != 1 {
		t.Fatalf("restart duplicated scheduled work: fires=%d tasks=%d", fireCount, taskCount)
	}
}

func TestConcurrentSchedulerStoresProduceOneFire(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	workspaceRoot := filepath.Join(root, "workspaces")
	store, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRepository(t, store)
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Concurrent scheduler", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	schedule, err := service.CreateSchedule(context.Background(), project.ID, core.CreateScheduleInput{Name: "One fire", CronExpression: "* * * * *", Timezone: "UTC", Goal: "one task", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "concurrent-scheduler-create")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE schedules SET next_run_at = ? WHERE id = ?`, time.Now().UTC().Add(-5*time.Second).Format(time.RFC3339Nano), schedule.ID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	first, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, scheduler := range []*okruntime.Scheduler{
		okruntime.NewScheduler(first, slog.New(slog.NewTextHandler(io.Discard, nil))),
		okruntime.NewScheduler(second, slog.New(slog.NewTextHandler(io.Discard, nil))),
	} {
		group.Add(1)
		go func(scheduler *okruntime.Scheduler) {
			defer group.Done()
			_, runErr := scheduler.RunOnce(context.Background())
			results <- runErr
		}(scheduler)
	}
	group.Wait()
	close(results)
	var runErrors []error
	for runErr := range results {
		if runErr != nil {
			runErrors = append(runErrors, runErr)
		}
	}
	check, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var fireCount, taskCount int
	if err := check.QueryRow(`SELECT COUNT(*) FROM schedule_fires WHERE schedule_id = ?`, schedule.ID).Scan(&fireCount); err != nil {
		t.Fatal(err)
	}
	if err := check.QueryRow(`SELECT COUNT(*) FROM tasks WHERE project_id = ? AND title = ?`, project.ID, schedule.Name).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if fireCount != 1 || taskCount != 1 || len(runErrors) > 1 {
		t.Fatalf("concurrent schedulers duplicated work or both failed: fires=%d tasks=%d errors=%v", fireCount, taskCount, runErrors)
	}
}
