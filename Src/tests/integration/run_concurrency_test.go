package integration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestDatabaseClaimLimitsDistinctConcurrentRunsToFour(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1",
		core.Actor{ID: testUserID, DisplayName: "Test User"},
		core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	for index := 0; index < 5; index++ {
		project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Concurrency", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{
			Goal: "task", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate,
		}, "concurrency-test", "")
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	seenRuns := map[string]struct{}{}
	for index := 0; index < 4; index++ {
		step, err := repository.ClaimNextStep(t.Context(), "worker-"+string(rune('a'+index)), now, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("claim %d: %v", index, err)
		}
		if step.OwnerID == "" {
			t.Fatalf("claim %d has no unique lease owner", index)
		}
		seenRuns[step.RunID] = struct{}{}
	}
	if len(seenRuns) != 4 {
		t.Fatalf("distinct active runs=%d", len(seenRuns))
	}
	if _, err := repository.ClaimNextStep(context.Background(), "worker-e", now, now.Add(time.Minute)); !errors.Is(err, core.ErrNoWork) {
		t.Fatalf("fifth distinct run claim err=%v", err)
	}
	expiredNow := now.Add(2 * time.Minute)
	if _, err := repository.ClaimNextStep(t.Context(), "worker-e", expiredNow, expiredNow.Add(time.Minute)); err != nil {
		t.Fatalf("expired lease did not release a durable slot: %v", err)
	}
}
