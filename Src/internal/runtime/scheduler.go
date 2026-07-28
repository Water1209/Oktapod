package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type ScheduleRepository interface {
	GetDueSchedule(ctx context.Context, now time.Time) (core.Schedule, error)
	ClaimScheduleFire(ctx context.Context, schedule core.Schedule, nextRunAt time.Time) (core.ScheduleFire, error)
	CreateScheduledTask(ctx context.Context, schedule core.Schedule, fire core.ScheduleFire, correlationID string) (core.Task, error)
	AdvanceScheduleAfterFire(ctx context.Context, scheduleID string, plannedAt, nextRunAt time.Time, fireStatus, taskID string) error
}

type Scheduler struct {
	repository   ScheduleRepository
	logger       *slog.Logger
	pollInterval time.Duration
	now          func() time.Time
}

func NewScheduler(repository ScheduleRepository, logger *slog.Logger) *Scheduler {
	return &Scheduler{repository: repository, logger: logger, pollInterval: 2 * time.Second, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.pollInterval)
	defer ticker.Stop()
	for {
		worked, err := s.RunOnce(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			s.logger.ErrorContext(ctx, "scheduler iteration failed", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) RunOnce(ctx context.Context) (bool, error) {
	now := s.now().UTC()
	schedule, err := s.repository.GetDueSchedule(ctx, now)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("load due schedule: %w", err)
	}
	location, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return true, fmt.Errorf("schedule %s has invalid timezone: %w", schedule.ID, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	spec, err := parser.Parse(schedule.CronExpression)
	if err != nil {
		return true, fmt.Errorf("schedule %s has invalid cron expression: %w", schedule.ID, err)
	}
	plannedLocal := schedule.NextRunAt.In(location)
	nextLocal := spec.Next(plannedLocal)
	for !nextLocal.After(now.In(location)) {
		nextLocal = spec.Next(nextLocal)
	}
	nextRunAt := nextLocal.UTC()
	// A delayed scheduler loop may discover a fire after its grace window. The
	// policy is deterministic and recorded as a fire event, so operators can
	// distinguish an intentional skip/wait from a runtime failure.
	lateness := now.Sub(schedule.NextRunAt)
	if lateness > time.Duration(schedule.MisfireGraceSeconds)*time.Second {
		switch schedule.MisfirePolicy {
		case core.ScheduleMisfireSkip:
			fire, claimErr := s.repository.ClaimScheduleFire(ctx, schedule, nextRunAt)
			if claimErr != nil {
				return true, fmt.Errorf("claim misfired schedule fire: %w", claimErr)
			}
			if fire.Status == core.ScheduleFirePending {
				if advanceErr := s.repository.AdvanceScheduleAfterFire(ctx, schedule.ID, schedule.NextRunAt, nextRunAt, core.ScheduleFireSkipped, ""); advanceErr != nil {
					return true, fmt.Errorf("mark misfired schedule fire skipped: %w", advanceErr)
				}
			}
			return true, nil
		case core.ScheduleMisfireAsk:
			fire, claimErr := s.repository.ClaimScheduleFire(ctx, schedule, nextRunAt)
			if claimErr != nil {
				return true, fmt.Errorf("claim misfired schedule fire: %w", claimErr)
			}
			if fire.Status == core.ScheduleFirePending {
				if advanceErr := s.repository.AdvanceScheduleAfterFire(ctx, schedule.ID, schedule.NextRunAt, nextRunAt, core.ScheduleFireWaiting, ""); advanceErr != nil {
					return true, fmt.Errorf("mark misfired schedule fire waiting: %w", advanceErr)
				}
			}
			return true, nil
		case core.ScheduleMisfireRunNow:
			// Continue with the normal task creation path.
		}
	}
	fire, err := s.repository.ClaimScheduleFire(ctx, schedule, nextRunAt)
	if err != nil {
		return true, fmt.Errorf("claim schedule fire: %w", err)
	}
	if fire.Status != core.ScheduleFirePending {
		return true, nil
	}
	task, err := s.repository.CreateScheduledTask(ctx, schedule, fire, fmt.Sprintf("schedule:%s:%s", schedule.ID, fire.ID))
	if err != nil {
		if updateErr := s.repository.AdvanceScheduleAfterFire(ctx, schedule.ID, schedule.NextRunAt, nextRunAt, core.ScheduleFireSkipped, ""); updateErr != nil {
			return true, fmt.Errorf("create scheduled task: %v; mark fire skipped: %w", err, updateErr)
		}
		return true, fmt.Errorf("create scheduled task: %w", err)
	}
	if err := s.repository.AdvanceScheduleAfterFire(ctx, schedule.ID, schedule.NextRunAt, nextRunAt, core.ScheduleFireCreated, task.ID); err != nil {
		return true, fmt.Errorf("mark schedule fire created: %w", err)
	}
	return true, nil
}
