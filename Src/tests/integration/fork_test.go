package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestForkTaskCreatesIndependentRunWithLineage(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Fork project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "fork-project")
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Title: "Source", Goal: "Do the work", Acceptance: "Check it", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "fork-source", "fork-source")
	if err != nil {
		t.Fatal(err)
	}
	fork, err := service.ForkTask(context.Background(), source.ID, "fork-child", "fork-child")
	if err != nil {
		t.Fatal(err)
	}
	if fork.ConversationID == source.ConversationID {
		t.Fatal("fork must execute in an independent conversation")
	}
	if fork.ID == source.ID || fork.RunID == source.RunID || fork.Title != "Fork: Source" || fork.Goal != source.Goal {
		t.Fatalf("source=%#v fork=%#v", source, fork)
	}
	var snapshot string
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRowContext(context.Background(), `SELECT config_snapshot_json FROM runs WHERE id = ?`, fork.RunID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(snapshot), &config); err != nil {
		t.Fatal(err)
	}
	if config["source_task_id"] != source.ID || config["source_run_id"] != source.RunID || config["source"] != "fork" {
		t.Fatalf("fork lineage=%#v", config)
	}
}
