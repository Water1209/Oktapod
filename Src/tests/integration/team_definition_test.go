package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTeamDefinitionIsVersionedAndPinnedToRun(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Team definition project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := service.CreateTeamDefinition(context.Background(), project.ID, core.CreateTeamDefinitionInput{Name: "Research team", Version: "1.0.0", Roles: []core.TeamRole{{Role: "researcher", Name: "Research"}, {Role: "reviewer", Name: "Review"}}, ConfigJSON: `{"max_model_calls":8}`}, "team-definition-create")
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.ListTeamDefinitions(context.Background(), project.ID)
	if err != nil || len(items) != 1 || items[0].Version != "1.0.0" {
		t.Fatalf("team definitions=%#v err=%v", items, err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Use pinned team", Mode: core.ModeAgentTeam, Visibility: core.VisibilityPrivate, TeamDefinitionID: definition.ID}, "team-definition-task", "team-definition-task-key")
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.GetRunContext(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	teamSnapshot, ok := snapshot["team_definition"].(map[string]any)
	if !ok || teamSnapshot["id"] != definition.ID || teamSnapshot["version"] != "1.0.0" {
		t.Fatalf("team snapshot=%#v", snapshot["team_definition"])
	}
	if _, err := service.SetTeamDefinitionStatus(context.Background(), definition.ID, core.CapabilityStatusDisabled, "team-definition-disable"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Disabled team must fail", Mode: core.ModeAgentTeam, Visibility: core.VisibilityPrivate, TeamDefinitionID: definition.ID}, "team-definition-disabled", "team-definition-disabled-key"); err == nil {
		t.Fatal("disabled TeamDefinition unexpectedly accepted")
	}
}
