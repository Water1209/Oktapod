package core

import (
	"regexp"
	"strconv"
	"strings"
)

// TaskCompletionRequirements is the domain-level contract inferred from the
// user's goal and explicit acceptance criteria. It deliberately describes
// required evidence, not how an adapter or agent must produce that evidence.
type TaskCompletionRequirements struct {
	RequiresWebEvidence         bool
	RequiresWorkspaceMutation   bool
	RequiresVerification        bool
	RequiresDeliverable         bool
	RequiresSingleDeliverable   bool
	RequiresCompleteDeliverable bool
	RequiresCitations           bool
	DeliverableFormat           string
	MinimumCharacters           int64
}

var minimumCharacterPattern = regexp.MustCompile(`(?i)(?:不少于|至少|最低|最少|>=|≥|at\s+least|minimum(?:\s+of)?|min(?:imum)?[\s:：]+)\s*([0-9][0-9,，]*)\s*(万)?\s*(?:字|字符|汉字|characters?)`)
var maximumSearchCallsPattern = regexp.MustCompile(`(?i)(?:最多|不超过|至多|at\s+most|maximum(?:\s+of)?|max(?:imum)?[\s:：]*)[^。；;\n]{0,24}?([0-9]{1,3})\s*(?:次|calls?)[^。；;\n]{0,48}?(?:检索|搜索|search|research|tavily)`)
var forbiddenSearchPattern = regexp.MustCompile(`(?i)(?:禁止|不要|无需|不再|不得)[^。；;\n]{0,24}?(?:再)?(?:调用[^。；;\n]{0,16}?)?(?:检索|搜索|联网|上网|站点地图|search|research|crawl|extract|map|browse|discover)`)

type SearchCallBudget struct {
	Limit    int
	Enforced bool
}

// InferTaskCompletionRequirements converts natural-language intent into a
// small, deterministic completion contract. The model remains responsible for
// planning and execution; this parser only prevents claims that contradict
// observable receipts and deliverables.
func InferTaskCompletionRequirements(goal, acceptance string) TaskCompletionRequirements {
	text := strings.ToLower(strings.TrimSpace(goal + "\n" + acceptance))
	compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(text)
	requirements := TaskCompletionRequirements{
		RequiresWebEvidence: containsAny(text,
			"检索", "搜索", "联网", "互联网", "调研", "research", "search"),
	}
	searchBudget := InferSearchCallBudget(goal, acceptance)
	if (searchBudget.Enforced && searchBudget.Limit == 0) || containsAny(compact, "禁止再检索", "不要再检索", "无需再检索", "不再调用search", "禁止再调用search", "现有tavily检索证据已经足够", "existingsearchevidenceissufficient") {
		requirements.RequiresWebEvidence = false
	}

	hasFileReference := containsAny(text, "文件", "文档", "报告", "交付物", "file", "document", "report")
	hasArtifactAction := containsAny(text,
		"形成", "生成", "创建", "保存", "写入", "导出", "交付", "落盘",
		"create", "write", "save", "export", "deliver")
	hasMarkdownFormat := containsAny(compact, "md文件", "md文档", "markdown", ".md")
	mutationExcluded := containsAny(compact, "先不改", "不要改", "不修改", "只分析", "仅分析", "只检查", "仅检查", "只诊断", "仅诊断")
	if !mutationExcluded && containsAny(text,
		"修复", "修改", "实现", "新增", "创建", "重构", "改造", "写入", "保存", "生成文件", "形成文件",
		" fix ", "implement", "refactor", "edit ", "modify", "create file", "write file") {
		requirements.RequiresWorkspaceMutation = true
	}
	if containsAny(text, "测试", "验证", "校验", "核对", "验收", "确保正常", " test", "verify", "validate") {
		requirements.RequiresVerification = true
	}
	if (hasFileReference && hasArtifactAction) || containsAny(text, "写入工作区", "保存到", "落盘") {
		requirements.RequiresDeliverable = true
		requirements.RequiresWorkspaceMutation = true
	}
	if requirements.RequiresDeliverable && hasMarkdownFormat {
		requirements.DeliverableFormat = "markdown"
	}
	if match := minimumCharacterPattern.FindStringSubmatch(text); len(match) == 3 {
		digits := strings.NewReplacer(",", "", "，", "").Replace(match[1])
		if parsed, err := strconv.ParseInt(digits, 10, 64); err == nil && parsed > 0 {
			if match[2] == "万" {
				parsed *= 10_000
			}
			if parsed > 10_000_000 {
				parsed = 10_000_000
			}
			if requirements.RequiresDeliverable {
				requirements.MinimumCharacters = parsed
			}
		}
	}
	if requirements.RequiresDeliverable {
		requirements.RequiresSingleDeliverable = containsAny(compact,
			"一份报告", "一份完整", "一个完整", "单一报告", "单个报告", "合并为一份", "汇总为一份", "完整分析报告",
			"singlereport", "onereport", "singlefile", "onedocument") ||
			(requirements.MinimumCharacters > 0 && containsAny(text, "报告", "report"))
		requirements.RequiresCompleteDeliverable = containsAny(text, "完整", "complete", "comprehensive")
		requirements.RequiresCitations = containsAny(text, "来源", "链接", "引用", "参考资料", "source", "citation", "reference", "link")
	}
	return requirements
}

// InferSearchCallBudget turns explicit natural-language tool constraints into
// an execution contract. Enforced=false means no user-declared bound; an
// enforced zero is materially different and forbids every new search call.
func InferSearchCallBudget(goal, acceptance string) SearchCallBudget {
	text := strings.ToLower(strings.TrimSpace(goal + "\n" + acceptance))
	if forbiddenSearchPattern.MatchString(text) {
		return SearchCallBudget{Limit: 0, Enforced: true}
	}
	match := maximumSearchCallsPattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return SearchCallBudget{}
	}
	value, err := strconv.Atoi(match[1])
	if err != nil || value < 1 {
		return SearchCallBudget{}
	}
	if value > 100 {
		value = 100
	}
	return SearchCallBudget{Limit: value, Enforced: true}
}

// InferMaximumSearchCalls is retained for numeric-only callers. Use
// InferSearchCallBudget when an explicit zero must differ from no constraint.
func InferMaximumSearchCalls(goal, acceptance string) int {
	return InferSearchCallBudget(goal, acceptance).Limit
}

func IsSearchEvidenceTool(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == ToolWebSearch {
		return true
	}
	if !strings.HasPrefix(lower, "mcp__") || strings.Count(lower, "__") < 2 {
		return false
	}
	parts := strings.Split(lower, "__")
	toolName := parts[len(parts)-1]
	return strings.Contains(toolName, "search") || strings.Contains(toolName, "research") || strings.Contains(toolName, "crawl") || strings.Contains(toolName, "extract") ||
		toolName == "map" || strings.HasSuffix(toolName, "_map") || toolName == "browse" || strings.HasSuffix(toolName, "_browse") || toolName == "discover" || strings.HasSuffix(toolName, "_discover")
}

func containsAny(text string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(text, candidate) {
			return true
		}
	}
	return false
}
