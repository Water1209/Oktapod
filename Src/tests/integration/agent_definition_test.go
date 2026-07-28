package integration_test

import (
	"context"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestVersionedAgentDefinitionPreflightAndLifecycle(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Agent project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := service.CreateAgentDefinition(context.Background(), project.ID, core.CreateAgentInput{Name: "Reviewer", Version: "1.0.0", Instructions: "Review evidence and list risks.", Model: "model-a", FallbackModel: "model-b", ConfigJSON: `{"requires_approval":true}`}, "agent-definition-1")
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	if agent.Status != core.CapabilityStatusQuarantined || agent.Version != "1.0.0" {
		t.Fatalf("agent=%#v", agent)
	}
	if _, err := service.CreateAgentDefinition(context.Background(), project.ID, core.CreateAgentInput{Name: "Unsafe", Version: "1.0.0", Instructions: "x", ConfigJSON: `{"allow_shell":true}`}, "agent-definition-2"); err == nil {
		t.Fatal("unsafe agent config should be rejected")
	}
	items, err := service.ListAgentDefinitions(context.Background(), project.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	updated, err := service.SetAgentDefinitionStatus(context.Background(), agent.ID, core.CapabilityStatusDisabled, "agent-status-1")
	if err != nil || updated.Status != core.CapabilityStatusDisabled {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
}
