package integration_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTaskBindsEnabledAgentModelIntoImmutableRunSnapshot(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Agent binding", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := service.CreateAgentDefinition(context.Background(), project.ID, core.CreateAgentInput{Name: "Researcher", Version: "1.0.0", Instructions: "Research the task.", Model: "model-agent", FallbackModel: "model-fallback", ConfigJSON: `{"temperature":0}`}, "agent-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetAgentDefinitionStatus(context.Background(), agent.ID, core.CapabilityStatusEnabled, "agent-enable"); err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Use the selected agent", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, AgentID: agent.ID}, "corr-agent", "task-agent")
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
	agentSnapshot, ok := snapshot["agent"].(map[string]any)
	if !ok || agentSnapshot["id"] != agent.ID || agentSnapshot["model"] != "model-agent" {
		t.Fatalf("agent snapshot=%#v", snapshot["agent"])
	}
}

func TestTaskModelOverrideIsBoundToRunSnapshot(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Model override", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Use selected model", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate, Model: "model-selected", FallbackModel: "model-selected-fallback"}, "corr-model-override", "task-model-override")
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
	if snapshot["model_override"] != "model-selected" || snapshot["fallback_model_override"] != "model-selected-fallback" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}

func TestProjectDefaultModelPolicyIsBoundWhenTaskHasNoOverride(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Project model policy", Kind: core.ProjectKindPersonal, Scene: core.SceneWork, DefaultModel: "project-model", FallbackModel: "project-fallback"}, "project-model-policy")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Use project model", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "corr-project-model-policy", "task-project-model-policy")
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
	if snapshot["project_default_model"] != "project-model" || snapshot["project_fallback_model"] != "project-fallback" {
		t.Fatalf("snapshot=%#v", snapshot)
	}
}
