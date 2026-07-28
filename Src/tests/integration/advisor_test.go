package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestAdvisorPreviewAndOfflineApprovalExposeIncrementalEstimate(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Advisor project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "produce evidence", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "advisor-task", "")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.CreateAdvisorReport(context.Background(), task.RunID, core.CreateAdvisorInput{TriggerReason: "evidence conflict", Approve: false}, "advisor-preview")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Status != "preview" || preview.EstimatedModelCalls != 1 || preview.EstimatedTokens != 1536 || len(preview.Evidence) == 0 {
		t.Fatalf("preview=%#v", preview)
	}
	approved, err := service.CreateAdvisorReport(context.Background(), task.RunID, core.CreateAdvisorInput{TriggerReason: "evidence conflict", Approve: true}, "advisor-approved")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != "completed" || approved.Summary == "" {
		t.Fatalf("approved=%#v", approved)
	}
	usage, err := service.GetRunUsage(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ModelCalls != 0 || usage.InputTokens != 0 || usage.OutputTokens != 0 {
		t.Fatalf("offline advisor unexpectedly consumed a model call: %#v", usage)
	}
}

type fakeAdvisorModel struct{}

func (fakeAdvisorModel) Complete(context.Context, string, string, int) (core.ModelResult, error) {
	return core.ModelResult{CallID: "019f72ee-0000-7000-8000-000000000001", Provider: "fake-advisor", Endpoint: "https://provider.invalid/v1", Model: "advisor-small", RoutingReason: "advisor_trigger", Content: "证据基本完整；请补充失败步骤的复现命令。", Usage: core.ModelUsage{InputTokens: 120, OutputTokens: 24}, Latency: 10 * time.Millisecond}, nil
}

func TestModelBackedAdvisorApprovalRecordsUsageAndEvidence(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).WithAdvisorModel(fakeAdvisorModel{})
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Model Advisor", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "advisor-model-project")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "review evidence", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "advisor-model-task", "advisor-model-task")
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.CreateAdvisorReport(context.Background(), task.RunID, core.CreateAdvisorInput{TriggerReason: "two failures", Approve: false}, "advisor-model-preview")
	if err != nil || preview.EstimatedModelCalls != 1 || preview.EstimatedTokens != 1536 {
		t.Fatalf("preview=%#v err=%v", preview, err)
	}
	approved, err := service.CreateAdvisorReport(context.Background(), task.RunID, core.CreateAdvisorInput{TriggerReason: "two failures", Approve: true}, "advisor-model-approve")
	if err != nil || approved.Status != "completed" || approved.Summary != "证据基本完整；请补充失败步骤的复现命令。" {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	usage, err := service.GetRunUsage(context.Background(), task.RunID)
	if err != nil || usage.ModelCalls != 1 || usage.InputTokens != 120 || usage.OutputTokens != 24 {
		t.Fatalf("advisor usage=%#v err=%v", usage, err)
	}
}
