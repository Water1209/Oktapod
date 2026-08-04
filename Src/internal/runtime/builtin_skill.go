package runtime

import (
	_ "embed"
	"strings"
)

//go:embed builtin_skills/skill-creator/SKILL.md
var builtInSkillCreator string

func builtInSkillContext(prompt string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	triggered := strings.Contains(normalized, "skill creator") ||
		strings.Contains(normalized, "skill-creator") ||
		strings.Contains(normalized, "创建skill") ||
		strings.Contains(normalized, "创建 skill") ||
		strings.Contains(normalized, "制作skill") ||
		strings.Contains(normalized, "制作 skill") ||
		strings.Contains(normalized, "create a skill")
	if !triggered {
		return "", ""
	}
	body := strings.TrimSpace(builtInSkillCreator)
	if delimiter := strings.Index(body, "\n---\n"); strings.HasPrefix(body, "---\n") && delimiter >= 0 {
		body = strings.TrimSpace(body[delimiter+5:])
	}
	return "# Skill Creator@builtin-v1\n" + body, "Skill Creator"
}
