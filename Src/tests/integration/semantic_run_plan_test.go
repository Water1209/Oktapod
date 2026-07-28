package integration_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

type semanticPlanSearch struct{}

func (semanticPlanSearch) Execute(_ context.Context, call core.ToolCall) core.ToolExecution {
	return core.ToolExecution{
		ToolCallID: call.ID,
		Status:     core.StatusSucceeded,
		Output: map[string]any{
			"results": []any{map[string]any{"title": "Evidence", "url": "https://example.test/evidence", "snippet": "verified"}},
		},
	}
}

func TestModelPublishesGoalSpecificSemanticPlanBeforeExecution(t *testing.T) {
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-model", "tool_calling": true}}})
			return
		}
		modelCalls++
		if modelCalls == 1 {
			arguments, _ := json.Marshal(map[string]any{
				"explanation": "先建立证据范围，再形成带来源的结论。",
				"items": []map[string]any{
					{"title": "收集今日关键政策与产业动态", "description": "覆盖权威来源并记录发布时间。", "status": "in_progress"},
					{"title": "交叉核验高影响事件", "description": "对关键事实使用独立来源复核。", "status": "pending"},
					{"title": "形成分级资讯简报", "description": "按影响和不确定性输出结论及来源。", "status": "pending"},
				},
			})
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []map[string]any{{
					"id": "publish-plan", "type": "function", "function": map[string]any{"name": core.ToolUpdatePlan, "arguments": string(arguments)},
				}},
			}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "计划已建立。"}}}, "usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5}})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Plan User"}, core.Organization{ID: testOrgID, Name: "Plan Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Semantic plan", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "检索今天的重要资讯", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "semantic-plan", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("semantic-plan-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("publish plan worked=%v err=%v", worked, runErr)
	}
	plan, err := service.GetActiveRunPlan(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 2 || len(plan.Items) != 3 || plan.Items[0].Title != "收集今日关键政策与产业动态" || plan.Items[0].Status != core.RunPlanItemRunning {
		t.Fatalf("semantic plan=%#v", plan)
	}
}

func TestRuntimePlanStructureRewriteProjectsProgressOntoStableRevision(t *testing.T) {
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-model", "tool_calling": true}}})
			return
		}
		modelCalls++
		var toolName string
		var arguments any
		switch modelCalls {
		case 1:
			toolName = core.ToolUpdatePlan
			arguments = map[string]any{"items": []map[string]any{
				{"title": "检索基础模型证据", "description": "检索并核验基础模型来源。", "status": "in_progress"},
				{"title": "分析仿真训练路线", "description": "分析仿真训练证据。", "status": "pending"},
				{"title": "输出技术简报", "description": "交付带来源的简报。", "status": "pending"},
			}}
		case 2:
			toolName = core.ToolWebSearch
			arguments = map[string]any{"query": "physical ai foundation model", "max_results": 3}
		case 3:
			toolName = core.ToolUpdatePlan
			arguments = map[string]any{"items": []map[string]any{
				{"title": "基础模型证据已核验", "description": "已经获得来源。", "status": "completed"},
				{"title": "仿真与数据联合分析", "description": "继续分析。", "status": "in_progress"},
				{"title": "撰写简报", "description": "形成正文。", "status": "pending"},
				{"title": "写入文件", "description": "新增的结构不应替换原计划。", "status": "pending"},
			}}
		case 4:
			toolName = core.ToolUpdatePlan
			arguments = map[string]any{"items": []map[string]any{
				{"title": "检索基础模型证据", "description": "检索并核验基础模型来源。", "status": "completed"},
				{"title": "分析仿真训练路线", "description": "分析仿真训练证据。", "status": "in_progress"},
				{"title": "输出技术简报", "description": "交付带来源的简报。", "status": "pending"},
			}}
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "done"}}}})
			return
		}
		encoded, _ := json.Marshal(arguments)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []map[string]any{{
					"id": "call", "type": "function", "function": map[string]any{"name": toolName, "arguments": string(encoded)},
				}},
			}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10},
		})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Plan User"}, core.Organization{ID: testOrgID, Name: "Plan Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Stable semantic plan", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "调研 Physical AI", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "stable-semantic-plan", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("stable-plan-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWebSearchExecutor(semanticPlanSearch{})
	for iteration := 0; iteration < 5; iteration++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
	}
	plan, err := service.GetActiveRunPlan(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 2 || len(plan.Items) != 3 {
		t.Fatalf("stable plan=%#v", plan)
	}
	if plan.Items[0].Title != "检索基础模型证据" || plan.Items[0].Status != core.RunPlanItemCompleted ||
		plan.Items[1].Title != "分析仿真训练路线" || plan.Items[1].Status != core.RunPlanItemRunning ||
		plan.Items[2].Title != "输出技术简报" || plan.Items[2].Status != core.RunPlanItemPending {
		t.Fatalf("projected plan=%#v", plan)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundIgnored := false
	foundNoProgressRecovery := false
	for _, event := range events {
		foundIgnored = foundIgnored || event.EventType == "plan.structure_change_ignored"
		foundNoProgressRecovery = foundNoProgressRecovery || event.EventType == "plan.no_progress_ignored"
	}
	if !foundIgnored {
		t.Fatal("missing plan.structure_change_ignored audit event")
	}
	if !foundNoProgressRecovery {
		t.Fatal("missing plan.no_progress_ignored recovery event")
	}
}
