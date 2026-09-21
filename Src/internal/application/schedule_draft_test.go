package application_test

import (
	"context"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type schedulePreviewModel struct {
	response string
}

func (m schedulePreviewModel) Complete(context.Context, string, string, int) (core.ModelResult, error) {
	return core.ModelResult{Content: m.response}, nil
}

func TestSubmitProjectTurnCreatesScheduleFromExplicitNaturalLanguage(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithAdvisorModel(schedulePreviewModel{response: `{"name":"每日简报","cron_expression":"0 9 * * *","timezone":"Asia/Shanghai","goal":"整理项目最近进展并生成简报","clarification":"","create_requested":true}`})
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Schedule chat", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{
		Content: "创建一个每个工作日上午9点的定时任务，整理项目最近的进展并生成简报",
		Mode:    core.ModeAuto,
	}, "test-schedule-chat", "schedule-chat-key")
	if err != nil {
		t.Fatal(err)
	}
	if result.Route != application.TurnRouteChat {
		t.Fatalf("route=%s", result.Route)
	}
	if len(result.Messages) != 2 || result.Messages[1].Role != "assistant" || result.Messages[1].Content == "" {
		t.Fatalf("messages=%+v", result.Messages)
	}
	schedules, err := service.ListSchedules(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 || schedules[0].Name != "每日简报" || schedules[0].Goal != "整理项目最近进展并生成简报" {
		t.Fatalf("schedules=%+v", schedules)
	}
}

func TestSubmitProjectTurnDoesNotCreateScheduleWithoutModelConfirmation(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithAdvisorModel(schedulePreviewModel{response: `{"name":"每日简报","cron_expression":"0 9 * * *","timezone":"Asia/Shanghai","goal":"整理项目最近进展并生成简报","clarification":"","create_requested":false}`})
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Schedule discussion", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{
		Content: "创建定时任务这个功能是怎么设计的？",
		Mode:    core.ModeAuto,
	}, "test-schedule-discussion", "schedule-discussion-key")
	if err != nil {
		t.Fatal(err)
	}
	if result.Route != application.TurnRouteChat {
		t.Fatalf("route=%s", result.Route)
	}
	schedules, err := service.ListSchedules(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 0 {
		t.Fatalf("unexpected schedules=%+v", schedules)
	}
}
