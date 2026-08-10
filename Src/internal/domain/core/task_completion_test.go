package core

import "testing"

func TestInferTaskCompletionRequirements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		goal         string
		acceptance   string
		web          bool
		mutation     bool
		verification bool
		deliverable  bool
		format       string
		minimum      int64
	}{
		{name: "original physical ai request", goal: "使用tavily工具帮我检索物理AI的产品调研报告，形成md文件", web: true, mutation: true, deliverable: true, format: "markdown"},
		{name: "explicit chinese length", goal: "深度调研物理 AI，保存 Markdown 报告", acceptance: "分析报告不少于30000字", web: true, mutation: true, deliverable: true, format: "markdown", minimum: 30000},
		{name: "ten-thousand shorthand", goal: "检索并写入工作区文档，至少3万字", web: true, mutation: true, deliverable: true, minimum: 30000},
		{name: "code change and test", goal: "修复登录错误并运行测试验证", mutation: true, verification: true},
		{name: "deliverable review", goal: "生成报告并核对文件", mutation: true, verification: true, deliverable: true},
		{name: "diagnosis only", goal: "只分析登录错误，先不改动代码", mutation: false},
		{name: "read existing markdown", goal: "读取 report.md 并分析问题", mutation: false, deliverable: false},
		{name: "long chat answer", goal: "解释这个架构，回答至少100字", mutation: false, deliverable: false},
		{name: "answer only", goal: "解释什么是 Physical AI", web: false, deliverable: false},
		{name: "continue with existing evidence", goal: "现有 Tavily 检索证据已经足够，禁止再检索；补足报告并核对", mutation: false, verification: true, web: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requirements := InferTaskCompletionRequirements(tt.goal, tt.acceptance)
			if requirements.RequiresWebEvidence != tt.web || requirements.RequiresWorkspaceMutation != tt.mutation || requirements.RequiresVerification != tt.verification || requirements.RequiresDeliverable != tt.deliverable || requirements.DeliverableFormat != tt.format || requirements.MinimumCharacters != tt.minimum {
				t.Fatalf("requirements=%#v", requirements)
			}
		})
	}
}

func TestInferMaximumSearchCalls(t *testing.T) {
	if got := InferMaximumSearchCalls("最多执行 3 次互不重复的 Tavily 检索", ""); got != 3 {
		t.Fatalf("maximum search calls=%d", got)
	}
	if got := InferMaximumSearchCalls("完成一份市场调研报告", ""); got != 0 {
		t.Fatalf("implicit search budget=%d", got)
	}
	budget := InferSearchCallBudget("现有证据已经足够，禁止再调用任何 search、research、crawl、extract 工具", "")
	if !budget.Enforced || budget.Limit != 0 {
		t.Fatalf("forbidden search budget=%#v", budget)
	}
	if requirements := InferTaskCompletionRequirements("继续调研，但禁止再调用任何 search、research、crawl、extract 工具", ""); requirements.RequiresWebEvidence {
		t.Fatalf("forbidden successor search still required a new web receipt: %#v", requirements)
	}
	budget = InferSearchCallBudget("现有报告已经完成，核对后直接交付，禁止联网", "")
	if !budget.Enforced || budget.Limit != 0 {
		t.Fatalf("forbidden network budget=%#v", budget)
	}
	if requirements := InferTaskCompletionRequirements("继续调研，现有报告已经完成，禁止联网", ""); requirements.RequiresWebEvidence {
		t.Fatalf("forbidden network successor still required a new web receipt: %#v", requirements)
	}
	budget = InferSearchCallBudget("完成一份市场调研报告", "")
	if budget.Enforced {
		t.Fatalf("implicit research unexpectedly constrained: %#v", budget)
	}
	for _, name := range []string{"mcp__remote__map", "mcp__remote__site_browse", "mcp__remote__discover"} {
		if !IsSearchEvidenceTool(name) {
			t.Fatalf("discovery tool %q escaped the search family", name)
		}
	}
	if IsSearchEvidenceTool("mcp__postgres__query") {
		t.Fatal("database query was misclassified as web search")
	}
}
