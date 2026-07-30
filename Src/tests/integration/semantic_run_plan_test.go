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
	if plan.Revision != 1 || len(plan.Items) != 3 || plan.Items[0].Title != "收集今日关键政策与产业动态" || plan.Items[0].Status != core.RunPlanItemRunning {
		t.Fatalf("semantic plan=%#v", plan)
	}
}

func TestSingleItemSemanticPlanReplacesInitialPlanningPlaceholder(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-model", "tool_calling": true}}})
			return
		}
		arguments, _ := json.Marshal(map[string]any{
			"explanation": "创建并验证一个可打开的测试页面。",
			"items": []map[string]any{{
				"title":       "创建并验证 HTML 测试页面",
				"description": "保存页面文件并确认页面内容可以正常加载。",
				"status":      "in_progress",
			}},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []map[string]any{{
					"id": "publish-single-plan", "type": "function",
					"function": map[string]any{"name": core.ToolUpdatePlan, "arguments": string(arguments)},
				}},
			}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10},
		})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Single Plan User"}, core.Organization{ID: testOrgID, Name: "Plan Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Single semantic plan", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "创建一个 HTML 测试文件并验证", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "single-semantic-plan", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("single-plan-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("publish single plan worked=%v err=%v", worked, runErr)
	}
	plan, err := service.GetActiveRunPlan(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 1 || len(plan.Items) != 1 || plan.Items[0].Title != "创建并验证 HTML 测试页面" || plan.Items[0].Status != core.RunPlanItemRunning {
		t.Fatalf("single semantic plan=%#v", plan)
	}
}

func TestInvalidInitialPlanQueuesModelRepair(t *testing.T) {
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-repair-model", "tool_calling": true}}})
			return
		}
		modelCalls++
		items := []map[string]any{}
		if modelCalls > 1 {
			items = []map[string]any{
				{"title": "创建 HTML 测试文件", "description": "在工作区保存目标 HTML 文件。", "status": "in_progress"},
				{"title": "读取并验证文件内容", "description": "重新读取文件并核对关键结构。", "status": "pending"},
			}
		}
		arguments, _ := json.Marshal(map[string]any{"explanation": "创建后读取验证。", "items": items})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []map[string]any{{
					"id": "publish-plan", "type": "function",
					"function": map[string]any{"name": core.ToolUpdatePlan, "arguments": string(arguments)},
				}},
			}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10},
		})
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Plan Repair User"}, core.Organization{ID: testOrgID, Name: "Plan Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Plan repair", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "创建并验证 HTML 文件", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "invalid-plan-repair", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("invalid-plan-repair-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("invalid plan worked=%v err=%v", worked, runErr)
	}
	running, err := service.GetTask(context.Background(), task.ID)
	if err != nil || running.Status != core.StatusRunning {
		t.Fatalf("invalid plan should queue model repair task=%#v err=%v", running, err)
	}
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("repaired plan worked=%v err=%v", worked, runErr)
	}
	plan, err := service.GetActiveRunPlan(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if modelCalls != 2 || len(plan.Items) != 2 || plan.Items[0].Title != "创建 HTML 测试文件" {
		t.Fatalf("repaired plan=%#v calls=%d", plan, modelCalls)
	}
}

func TestToolExecutionMaterializesGoalSpecificFallbackPlan(t *testing.T) {
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-required-model", "tool_calling": true}}})
			return
		}
		modelCalls++
		toolName := core.ToolWorkspaceWriteFile
		arguments := map[string]any{"path": "must-wait.html", "content": "<html></html>"}
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
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Plan Required User"}, core.Organization{ID: testOrgID, Name: "Plan Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Plan required", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "创建并验证 HTML 文件", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "plan-required", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("plan-required-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("premature tool worked=%v err=%v", worked, runErr)
	}
	running, err := service.GetTask(context.Background(), task.ID)
	if err != nil || (running.Status != core.StatusQueued && running.Status != core.StatusRunning) {
		t.Fatalf("fallback plan should keep task executable task=%#v err=%v", running, err)
	}
	plan, err := service.GetActiveRunPlan(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if modelCalls != 1 || len(plan.Items) != 1 || plan.Items[0].Title != "创建并验证 HTML 文件" || plan.Items[0].Status != core.RunPlanItemRunning {
		t.Fatalf("goal-specific fallback did not replace placeholder: %#v calls=%d", plan, modelCalls)
	}
}

func TestModelPlanRevisionContinuesAfterHistoricalPlanCompleted(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-model", "tool_calling": true}}})
			return
		}
		arguments, _ := json.Marshal(map[string]any{
			"explanation": "基于已保存证据继续交付。",
			"items": []map[string]any{
				{"title": "整理平台证据", "description": "汇总已经取得的官方来源。", "status": "in_progress"},
				{"title": "写入调研文档", "description": "生成可直接阅读的 Markdown 文件。", "status": "pending"},
			},
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []map[string]any{{
					"id": "publish-plan-after-completion", "type": "function",
					"function": map[string]any{"name": core.ToolUpdatePlan, "arguments": string(arguments)},
				}},
			}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 10},
		})
	}))
	defer provider.Close()

	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Historical Plan User"}, core.Organization{ID: testOrgID, Name: "Historical Plan Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Historical plan", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "继续生成调研文档", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "historical-plan", "")
	if err != nil {
		t.Fatal(err)
	}
	db := openTaskControlDatabase(t, databasePath)
	if _, err := db.Exec(`UPDATE run_plan_revisions SET status = 'completed' WHERE run_id = ?`, task.RunID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	worker := okruntime.NewWorker("historical-plan-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("publish plan after completion worked=%v err=%v", worked, runErr)
	}
	plan, err := service.GetActiveRunPlan(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Revision != 2 || len(plan.Items) != 2 || plan.Items[0].Title != "整理平台证据" {
		t.Fatalf("continued plan=%#v", plan)
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
	if plan.Revision != 1 || len(plan.Items) != 3 {
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

func TestPlanStatusRegressionIsAutomaticallyRepaired(t *testing.T) {
	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "plan-recovery-model", "tool_calling": true}}})
			return
		}
		modelCalls++
		var toolName string
		var arguments any
		switch modelCalls {
		case 1:
			toolName = core.ToolUpdatePlan
			arguments = map[string]any{"items": []map[string]any{
				{"title": "检索权威证据", "description": "获得可引用来源。", "status": "in_progress"},
				{"title": "形成证据摘要", "description": "保留已完成进度并交付。", "status": "pending"},
			}}
		case 2:
			toolName = core.ToolWebSearch
			arguments = map[string]any{"query": "verified source", "max_results": 3}
		case 3:
			toolName = core.ToolUpdatePlan
			arguments = map[string]any{"items": []map[string]any{
				{"title": "检索权威证据", "description": "已获得可引用来源。", "status": "completed"},
				{"title": "形成证据摘要", "description": "正在整理证据。", "status": "in_progress"},
			}}
		case 4:
			toolName = core.ToolUpdatePlan
			arguments = map[string]any{"items": []map[string]any{
				{"title": "检索权威证据", "description": "错误地回退已完成项。", "status": "pending"},
				{"title": "形成证据摘要", "description": "仍在整理。", "status": "in_progress"},
			}}
		case 5:
			toolName = core.ToolUpdatePlan
			arguments = map[string]any{"items": []map[string]any{
				{"title": "检索权威证据", "description": "保持完成。", "status": "completed"},
				{"title": "形成证据摘要", "description": "摘要已形成。", "status": "completed"},
			}}
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "已基于权威证据完成摘要。"}}},
				"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 10},
			})
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
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Plan Recovery User"}, core.Organization{ID: testOrgID, Name: "Plan Recovery Org"}, func() string { return "fake-plan-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Plan regression recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "检索权威证据并形成摘要", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "plan-regression-recovery", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("plan-regression-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-plan-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWebSearchExecutor(semanticPlanSearch{})
	var completed core.Task
	for iteration := 0; iteration < 10; iteration++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("iteration=%d worked=%v err=%v", iteration, worked, runErr)
		}
		completed, err = service.GetTask(context.Background(), task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if completed.Status == core.StatusSucceeded {
			break
		}
		if completed.Status == core.StatusBlocked || completed.Status == core.StatusFailed {
			t.Fatalf("plan regression escaped automatic recovery: %#v", completed)
		}
	}
	if completed.Status != core.StatusSucceeded {
		t.Fatalf("task did not finish after plan repair: %#v", completed)
	}
	plan, err := service.GetActiveRunPlan(context.Background(), task.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 || plan.Items[0].Status != core.RunPlanItemCompleted || plan.Items[1].Status != core.RunPlanItemCompleted {
		t.Fatalf("repaired plan=%#v", plan)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundRegressionRecovery := false
	foundModelRetry := false
	for _, event := range events {
		foundRegressionRecovery = foundRegressionRecovery || event.EventType == "plan.status_regression_ignored"
		foundModelRetry = foundModelRetry || event.EventType == "model.retry_queued"
	}
	if !foundRegressionRecovery || !foundModelRetry {
		t.Fatalf("missing recovery events: %#v", events)
	}
}
