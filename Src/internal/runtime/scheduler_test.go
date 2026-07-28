package runtime

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type schedulerFakeRepository struct {
	schedule       core.Schedule
	fire           core.ScheduleFire
	task           core.Task
	claimed        int
	advanced       int
	advancedStatus string
}

func (f *schedulerFakeRepository) GetDueSchedule(context.Context, time.Time) (core.Schedule, error) {
	return f.schedule, nil
}
func (f *schedulerFakeRepository) ClaimScheduleFire(_ context.Context, schedule core.Schedule, _ time.Time) (core.ScheduleFire, error) {
	f.claimed++
	f.fire = core.ScheduleFire{ID: "fire-1", ScheduleID: schedule.ID, PlannedAt: schedule.NextRunAt, Status: core.ScheduleFirePending}
	return f.fire, nil
}
func (f *schedulerFakeRepository) CreateScheduledTask(_ context.Context, schedule core.Schedule, fire core.ScheduleFire, _ string) (core.Task, error) {
	f.task = core.Task{ID: "task-1", ProjectID: schedule.ProjectID, Goal: schedule.Goal}
	return f.task, nil
}
func (f *schedulerFakeRepository) AdvanceScheduleAfterFire(_ context.Context, _ string, _ time.Time, _ time.Time, status string, _ string) error {
	f.advanced++
	f.advancedStatus = status
	return nil
}

func TestSchedulerRunOnceCreatesStandardTaskAndAdvancesFire(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	fake := &schedulerFakeRepository{schedule: core.Schedule{ID: "schedule-1", ProjectID: "project-1", Name: "Digest", CronExpression: "0 * * * *", Timezone: "UTC", Goal: "Summarize", NextRunAt: now.Add(-time.Hour), Status: core.ScheduleStatusEnabled}}
	scheduler := NewScheduler(fake, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	scheduler.now = func() time.Time { return now }
	worked, err := scheduler.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
	if fake.claimed != 1 || fake.advanced != 1 || fake.task.ID != "task-1" {
		t.Fatalf("fake=%#v", fake)
	}
}

func TestSchedulerNoWorkIsNotAnError(t *testing.T) {
	fake := &schedulerNoWorkRepository{}
	scheduler := NewScheduler(fake, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	worked, err := scheduler.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("worked=%v err=%v", worked, err)
	}
}

func TestSchedulerMisfireSkipDoesNotCreateTask(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	fake := &schedulerFakeRepository{schedule: core.Schedule{ID: "schedule-1", ProjectID: "project-1", CronExpression: "0 * * * *", Timezone: "UTC", Goal: "Digest", NextRunAt: now.Add(-2 * time.Hour), MisfirePolicy: core.ScheduleMisfireSkip, MisfireGraceSeconds: 60}}
	scheduler := NewScheduler(fake, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	scheduler.now = func() time.Time { return now }
	worked, err := scheduler.RunOnce(context.Background())
	if err != nil || !worked || fake.claimed != 1 || fake.advancedStatus != core.ScheduleFireSkipped || fake.task.ID != "" {
		t.Fatalf("worked=%v err=%v fake=%#v", worked, err, fake)
	}
}

func TestSchedulerMisfireAskLeavesWaitingFire(t *testing.T) {
	now := time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC)
	fake := &schedulerFakeRepository{schedule: core.Schedule{ID: "schedule-1", ProjectID: "project-1", CronExpression: "0 * * * *", Timezone: "UTC", Goal: "Digest", NextRunAt: now.Add(-2 * time.Hour), MisfirePolicy: core.ScheduleMisfireAsk, MisfireGraceSeconds: 60}}
	scheduler := NewScheduler(fake, slog.New(slog.NewTextHandler(testDiscard{}, nil)))
	scheduler.now = func() time.Time { return now }
	worked, err := scheduler.RunOnce(context.Background())
	if err != nil || !worked || fake.claimed != 1 || fake.advancedStatus != core.ScheduleFireWaiting || fake.task.ID != "" {
		t.Fatalf("worked=%v err=%v fake=%#v", worked, err, fake)
	}
}

type schedulerNoWorkRepository struct{}

func (*schedulerNoWorkRepository) GetDueSchedule(context.Context, time.Time) (core.Schedule, error) {
	return core.Schedule{}, sql.ErrNoRows
}
func (*schedulerNoWorkRepository) ClaimScheduleFire(context.Context, core.Schedule, time.Time) (core.ScheduleFire, error) {
	panic("not called")
}
func (*schedulerNoWorkRepository) CreateScheduledTask(context.Context, core.Schedule, core.ScheduleFire, string) (core.Task, error) {
	panic("not called")
}
func (*schedulerNoWorkRepository) AdvanceScheduleAfterFire(context.Context, string, time.Time, time.Time, string, string) error {
	panic("not called")
}

type testDiscard struct{}

func (testDiscard) Write(p []byte) (int, error) { return len(p), nil }
