package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type dailySummaryFake struct {
	projects []core.Project
	keys     []string
	failed   bool
}

func (f *dailySummaryFake) ListProjects(context.Context) ([]core.Project, error) {
	return f.projects, nil
}

func (f *dailySummaryFake) CreateDailySummaryForDate(_ context.Context, projectID, date, key string) (core.Memory, error) {
	f.keys = append(f.keys, projectID+"|"+date+"|"+key)
	if f.failed && projectID == "project-2" {
		return core.Memory{}, errors.New("simulated summary failure")
	}
	return core.Memory{ID: projectID, ProjectID: projectID, Kind: "daily_summary"}, nil
}

func TestRunDailySummaryForDateUsesStableProjectDateKey(t *testing.T) {
	fake := &dailySummaryFake{projects: []core.Project{{ID: "project-1"}, {ID: "project-2"}}}
	created, err := RunDailySummaryForDate(context.Background(), fake, "2026-07-18", nil)
	if err != nil || created != 2 {
		t.Fatalf("created=%d err=%v", created, err)
	}
	if len(fake.keys) != 2 || fake.keys[0] != "project-1|2026-07-18|daily-summary:project-1:2026-07-18" {
		t.Fatalf("keys=%v", fake.keys)
	}
}

func TestRunDailySummaryForDateContinuesOtherProjectsAfterFailure(t *testing.T) {
	fake := &dailySummaryFake{projects: []core.Project{{ID: "project-1"}, {ID: "project-2"}}, failed: true}
	created, err := RunDailySummaryForDate(context.Background(), fake, "2026-07-18", nil)
	if err == nil || created != 1 || len(fake.keys) != 2 {
		t.Fatalf("created=%d keys=%v err=%v", created, fake.keys, err)
	}
}
