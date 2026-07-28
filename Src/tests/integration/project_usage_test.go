package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestProjectUsageAggregatesWithoutPrivateContent(t *testing.T) {
	t.Parallel()
	provider := newMockProvider(t)
	defer provider.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Usage Owner"}, core.Organization{ID: testOrgID, Name: "Usage Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Usage project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Title: "Private title must not be in aggregate", Goal: "aggregate usage", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "usage-correlation", "usage-task"); err != nil {
		t.Fatal(err)
	}
	model := openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" })
	worker := okruntime.NewWorker("usage-worker", repository, model, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("worker worked=%v err=%v", worked, err)
	}
	usage, err := service.GetProjectUsage(context.Background(), project.ID, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCalls != 1 || usage.InputTokens != 18 || usage.OutputTokens != 9 || len(usage.ByModel) != 1 || usage.ByModel[0].Key == "" || len(usage.ByMember) != 1 || usage.ByMember[0].Label != "Test User" || len(usage.ByAgent) != 1 {
		t.Fatalf("unexpected project usage: %#v", usage)
	}
	if (usage.CostStatus != "estimated" && usage.CostStatus != "confirmed") || usage.CostDecimal == "" {
		t.Fatalf("expected priced usage cost status, got %#v", usage)
	}
	for _, bucket := range usage.ByDay {
		if bucket.Label == "Private title must not be in aggregate" {
			t.Fatal("private task title leaked into usage aggregate")
		}
	}
}
