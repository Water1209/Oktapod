package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// DailySummaryService is deliberately narrower than application.Service so
// the runner remains deterministic and easy to test. It never receives a
// model gateway or a prompt.
type DailySummaryService interface {
	ListProjects(context.Context) ([]core.Project, error)
	CreateDailySummaryForDate(context.Context, string, string, string) (core.Memory, error)
}

// RunDailySummaryForDate creates at most one summary per visible project. The
// application layer uses project/date as a durable uniqueness key, so retries
// after a crash are safe even when this function is called again.
func RunDailySummaryForDate(ctx context.Context, service DailySummaryService, date string, logger *slog.Logger) (int, error) {
	projects, err := service.ListProjects(ctx)
	if err != nil {
		return 0, err
	}
	created := 0
	var firstErr error
	for _, project := range projects {
		key := fmt.Sprintf("daily-summary:%s:%s", project.ID, date)
		if _, summaryErr := service.CreateDailySummaryForDate(ctx, project.ID, date, key); summaryErr != nil {
			if firstErr == nil {
				firstErr = summaryErr
			}
			if logger != nil {
				logger.ErrorContext(ctx, "daily summary failed", "project_id", project.ID, "date", date, "error", summaryErr)
			}
			continue
		}
		created++
	}
	return created, firstErr
}

// RunDailySummaryLoop performs a bounded seven-day catch-up on startup and
// closes the previous UTC day whenever the process crosses midnight. The
// catch-up is bounded to avoid an unbounded restart storm; operators can call
// the explicit date API for older history.
func RunDailySummaryLoop(ctx context.Context, service DailySummaryService, logger *slog.Logger) error {
	now := time.Now().UTC()
	for daysAgo := 7; daysAgo >= 1; daysAgo-- {
		date := now.AddDate(0, 0, -daysAgo).Format("2006-01-02")
		if _, err := RunDailySummaryForDate(ctx, service, date, logger); err != nil && logger != nil {
			logger.ErrorContext(ctx, "daily summary catch-up incomplete", "date", date, "error", err)
		}
	}
	lastDate := now.Format("2006-01-02")
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case tick := <-ticker.C:
			current := tick.UTC()
			currentDate := current.Format("2006-01-02")
			if currentDate == lastDate {
				continue
			}
			if _, err := RunDailySummaryForDate(ctx, service, lastDate, logger); err != nil && logger != nil {
				logger.ErrorContext(ctx, "daily summary rollover incomplete", "date", lastDate, "error", err)
			}
			lastDate = currentDate
		}
	}
}
