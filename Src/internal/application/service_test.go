package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestClassifyTaskModeUsesDeterministicRules(t *testing.T) {
	tests := []struct {
		name  string
		input core.CreateTaskInput
		mode  string
	}{
		{name: "simple answer", input: core.CreateTaskInput{Goal: "解释一下 fencing token"}, mode: core.ModeAnswer},
		{name: "workspace task", input: core.CreateTaskInput{Goal: "修改文件并运行测试"}, mode: core.ModeSingleAgent},
		{name: "acceptance task", input: core.CreateTaskInput{Goal: "给出结果", Acceptance: "必须通过测试"}, mode: core.ModeSingleAgent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mode, reason := classifyTaskMode(test.input)
			if mode != test.mode || reason == "" {
				t.Fatalf("mode=%s reason=%s want=%s", mode, reason, test.mode)
			}
		})
	}
}

func TestParseTurnRouteAcceptsOnlyStrictRoutes(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{`{"route":"chat"}`, TurnRouteChat},
		{`{"route":"task"}`, TurnRouteTask},
		{"chat", ""},
		{"task", ""},
		{`{"route":"CHAT"}`, ""},
		{`{"route":"unknown"}`, ""},
		{"```json\n{\"route\":\"chat\"}\n```", ""},
		{"I think this is neither", ""},
	}
	for _, test := range tests {
		got, _ := parseTurnRoute(test.content)
		if got != test.want {
			t.Fatalf("parseTurnRoute(%q)=%q, want %q", test.content, got, test.want)
		}
	}
}

func TestParseMCPInstallDirectiveAcceptsNaturalToolInstallRequests(t *testing.T) {
	tests := []struct {
		content string
		want    string
		matched bool
	}{
		{"安装 Playwright 的浏览器自动化工具", "Playwright 浏览器自动化", true},
		{"帮我安装浏览器自动化工具", "浏览器自动化", true},
		{"给项目接入浏览器自动化工具", "浏览器自动化", true},
		{"帮我安装 @playwright/mcp", "@playwright/mcp", true},
		{"安装 npm:@playwright/mcp", "npm:@playwright/mcp", true},
		{"install playwright tools", "playwright", true},
		{"安装文档生成工具", "文档生成", true},
		{"帮我写一份文档", "", false},
	}
	for _, test := range tests {
		got, matched := parseMCPInstallDirective(test.content)
		if got != test.want || matched != test.matched {
			t.Fatalf("parseMCPInstallDirective(%q)=(%q,%v), want (%q,%v)", test.content, got, matched, test.want, test.matched)
		}
	}
}

type recordingMCPRegistry struct {
	searches []string
	limits   []int
	results  [][]MCPRegistryServer
	err      error
}

func (r *recordingMCPRegistry) Search(_ context.Context, query string, limit int) ([]MCPRegistryServer, error) {
	r.searches = append(r.searches, query)
	r.limits = append(r.limits, limit)
	if len(r.results) > 0 {
		result := r.results[0]
		r.results = r.results[1:]
		return result, nil
	}
	return nil, nil
}

type erroringMCPRegistry struct{}

func (erroringMCPRegistry) Search(_ context.Context, _ string, _ int) ([]MCPRegistryServer, error) {
	return nil, errors.New("registry timeout")
}

func TestSearchMCPRegistryFallsBackToExplicitIdentifier(t *testing.T) {
	service := (&Service{}).WithMCPRegistry(&recordingMCPRegistry{
		results: [][]MCPRegistryServer{
			nil,
			{{Name: "io.github.microsoft/playwright-mcp", Title: "Playwright"}},
		},
	})

	servers, err := service.searchMCPRegistry(t.Context(), "Playwright 浏览器自动化")
	if err != nil {
		t.Fatal(err)
	}
	registry := service.mcpRegistry.(*recordingMCPRegistry)
	if len(servers) != 1 || servers[0].Name != "io.github.microsoft/playwright-mcp" {
		t.Fatalf("servers=%#v", servers)
	}
	if len(registry.searches) != 2 || registry.searches[0] != "Playwright 浏览器自动化" || registry.searches[1] != "Playwright" {
		t.Fatalf("searches=%#v", registry.searches)
	}
	if len(registry.limits) != 2 || registry.limits[0] != 8 || registry.limits[1] != 20 {
		t.Fatalf("limits=%#v", registry.limits)
	}
}

func TestSearchMCPRegistryDoesNotFallbackAfterSearchError(t *testing.T) {
	service := (&Service{}).WithMCPRegistry(erroringMCPRegistry{})

	_, err := service.searchMCPRegistry(t.Context(), "Playwright 浏览器自动化")
	if err == nil || err.Error() != "registry timeout" {
		t.Fatalf("err=%v, want registry timeout", err)
	}
}

func TestTaskInboxReasonDoesNotPresentFailedTaskAsSuccessful(t *testing.T) {
	task := core.Task{
		Status:        core.StatusFailed,
		ResultSummary: "工具执行完成，并通过确定性校验。",
	}

	reason := taskInboxReason(task)
	if !strings.Contains(reason, "任务已停止") || !strings.Contains(reason, "不会自动继续") {
		t.Fatalf("reason=%q must explain the terminal failure", reason)
	}
	if strings.Contains(reason, "工具执行完成") {
		t.Fatalf("reason=%q must not repeat the contradictory success summary", reason)
	}
}

func TestTaskInboxActionExcludesTerminalFailure(t *testing.T) {
	kind, reason := taskInboxAction(core.Task{Status: core.StatusFailed, ErrorCode: "TOOL_EXECUTION_FAILED"})
	if kind != "" || reason != "" {
		t.Fatalf("terminal failure must not create pending inbox action: kind=%q reason=%q", kind, reason)
	}

	kind, reason = taskInboxAction(core.Task{Status: core.StatusBlocked, ErrorCode: "MODEL_CREDENTIAL_MISSING"})
	if kind != "task_recovery" || !strings.Contains(reason, "模型配置或凭据") {
		t.Fatalf("blocked task must remain actionable: kind=%q reason=%q", kind, reason)
	}
}

func TestRepeatedActivitySuggestionsRequireThreeOccurrences(t *testing.T) {
	got := repeatedActivitySuggestions(map[string]int{
		"task.failed":    2,
		"task.succeeded": 3,
		"tool.completed": 5,
	})
	if len(got) != 2 || got[0] != "task.succeeded 出现 3 次，可评估沉淀为确定性规则/Workflow/Skill" || got[1] != "tool.completed 出现 5 次，可评估沉淀为确定性规则/Workflow/Skill" {
		t.Fatalf("suggestions=%v", got)
	}
}
