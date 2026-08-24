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
var forbiddenSearchPattern = regexp.MustCompile(`(?i)(?:(?:禁止|不要|无需|不再|不得)[^。；;\n]{0,24}?(?:再)?(?:调用[^。；;\n]{0,16}?)?(?:检索|搜索|联网|上网|站点地图|search|research|crawl|extract|map|browse|discover)|不(?:再)?联网|不(?:再)?上网|离线(?:完成|执行|处理|核对|验收)?|offline|without\s+(?:web|internet|network)\s+access|no\s+(?:web|internet|network)\s+access)`)
var intentClauseSeparator = regexp.MustCompile(`(?i)[。！？!?；;，,\n\r]+|(?:但是|但|而是|然后|随后|接着)|\b(?:but|then|instead)\b`)
var forbiddenMutationPattern = regexp.MustCompile(`(?i)(?:不要|禁止|不得|无需|无须|不需要|请勿|不应|不能|先不|不|do\s+not|don't|must\s+not|no\s+need\s+to|without)[^。！？!?；;，,\n\r]{0,24}?(?:修复|修改|改动|改造|编辑|变更|重构|新增|创建|生成|写入|保存|导出|落盘|提交|转换|转成|转为|fix|modify|edit|change|refactor|implement|create|generate|write|save|export|commit|convert)`)

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
	mutationText := positiveMutationIntentText(text)
	mutationCompact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(mutationText)
	requirements := TaskCompletionRequirements{
		RequiresWebEvidence: containsAny(text,
			"检索", "搜索", "联网", "互联网", "调研", "research", "search"),
	}
	searchBudget := InferSearchCallBudget(goal, acceptance)
	if (searchBudget.Enforced && searchBudget.Limit == 0) || containsAny(compact, "禁止再检索", "不要再检索", "无需再检索", "不再调用search", "禁止再调用search", "现有tavily检索证据已经足够", "existingsearchevidenceissufficient") {
		requirements.RequiresWebEvidence = false
	}

	hasFileReference := containsAny(mutationText, "文件", "文档", "报告", "交付物", "file", "document", "report")
	hasArtifactAction := containsAny(mutationText,
		"形成", "生成", "创建", "保存", "写入", "导出", "交付", "落盘", "转换", "转成", "转为",
		"create", "write", "save", "export", "deliver", "convert")
	hasMarkdownFormat := containsAny(compact, "md文件", "md文档", "markdown", ".md")
	hasPDFFormat := containsAny(compact, "pdf", ".pdf", "便携式文档")
	hasPDFConversion := containsAny(mutationCompact, "转pdf", "转成pdf", "转为pdf", "转换pdf", "转换为pdf", "md转pdf")
	hasGitVersionAction := containsAny(mutationCompact, "git提交", "gitcommit", "git版本", "保存版本", "提交所选", "提交以下已选")
	if containsAny(mutationText,
		"修复", "修改", "实现", "新增", "创建", "重构", "改造", "写入", "保存", "生成文件", "形成文件",
		" fix ", "implement", "refactor", "edit ", "modify", "create file", "write file") {
		requirements.RequiresWorkspaceMutation = true
	}
	if containsAny(text, "测试", "验证", "校验", "核对", "验收", "确保正常", " test", "verify", "validate") {
		requirements.RequiresVerification = true
	}
	if (hasFileReference && hasArtifactAction && !hasGitVersionAction) || (hasPDFFormat && (hasArtifactAction || hasPDFConversion)) || containsAny(mutationText, "写入工作区", "保存到", "落盘") {
		requirements.RequiresDeliverable = true
		requirements.RequiresWorkspaceMutation = true
	}
	if requirements.RequiresDeliverable && hasPDFConversion {
		requirements.DeliverableFormat = "pdf"
	} else if requirements.RequiresDeliverable && hasMarkdownFormat {
		requirements.DeliverableFormat = "markdown"
	} else if requirements.RequiresDeliverable && hasPDFFormat {
		requirements.DeliverableFormat = "pdf"
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

// positiveMutationIntentText keeps only clauses that can express an affirmative
// workspace change. Negated and explicitly read-only clauses must not satisfy a
// completion gate merely because they contain words such as "修改" or "write".
// Clause-level filtering preserves mixed requests such as "不要修改旧文件，但创建新文件".
func positiveMutationIntentText(text string) string {
	clauses := intentClauseSeparator.Split(text, -1)
	positive := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(clause)
		if forbiddenMutationPattern.MatchString(clause) || containsAny(compact,
			"只分析", "仅分析", "只检查", "仅检查", "只诊断", "仅诊断", "只读取", "仅读取", "只查看", "仅查看", "只回复", "仅回复", "只需回复",
			"readonly", "onlyanalyze", "onlyanalyse", "onlyinspect", "onlydiagnose", "onlyread", "onlyreview", "onlyreply") {
			continue
		}
		positive = append(positive, clause)
	}
	return strings.Join(positive, "\n")
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
	if lower == ToolWebSearch || lower == ToolWebFetch {
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
