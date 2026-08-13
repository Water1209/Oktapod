package runtime

import (
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// PromptAssembler keeps identity, scene preferences, frozen project guidance,
// stage instructions, and the actually exposed tool catalog as independent
// layers. Scene guidance changes priorities only; execution authority remains
// exclusively owned by ExecutionPolicy and the approval router.
type PromptAssembler struct{}

func (PromptAssembler) Base(run core.RunContext) (string, string) {
	system := `# Identity
You are an autonomous agent operating inside an Oktapod project workspace. Complete the user's goal with the tools actually exposed to this Run.

# Operating contract
- Start from the requested outcome, context, output, and boundaries; choose sensible defaults when details do not materially change the result.
- Inspect relevant state before changing it. Keep actions inside the project scope and preserve unrelated user work.
- Diagnose tool failures and recover from their cause. Never invent execution, validation, source, or file evidence.
- Report the outcome faithfully and include concrete file paths for deliverables.`
	if run.Mode == core.ModeAnswer {
		system = `# Identity
You are the question-answering agent in an Oktapod project. Answer accurately and concisely, using only the tools actually exposed to this Run. Prefer verified project or source evidence over assumptions.`
	}

	scene := sceneFromSnapshot(run.ConfigSnapshotJSON)
	switch scene {
	case core.SceneCode:
		system += `

# Code workflow profile
Prioritize project instructions, dependency boundaries, focused edits, tests, Git diff review, and an auditable change summary. Workspace commands and Git are normal governed tools when exposed; the scene itself grants no permission.`
	default:
		system += `

# Work workflow profile
Prioritize selected attachments, source quality, document structure, requested formatting, and validation of the real deliverable. Scripts, workspace commands, and Git are normal governed tools when exposed; the scene itself grants no permission.`
	}

	if goruntime.GOOS == "windows" {
		system += "\n\n# Host platform\nThe workspace host is Windows. workspace_shell runs through cmd.exe /c; use Windows-native commands and paths."
	} else {
		system += "\n\n# Host platform\nThe workspace host is " + goruntime.GOOS + ". workspace_shell runs through bash -c."
	}
	if instructions := frozenProjectInstructions(run.ConfigSnapshotJSON); instructions != "" {
		system += "\n\n# Frozen project instructions\nThese instructions were loaded and frozen when the Run started. Closer workspace guidance overrides earlier guidance.\n" + instructions
	}

	parts := []string{"目标：" + run.Goal}
	if strings.TrimSpace(run.Acceptance) != "" {
		parts = append(parts, "验收标准："+run.Acceptance)
	}
	parts = append(parts, "请输出用户可直接阅读和验收的最终结果。")
	return system, strings.Join(parts, "\n\n")
}

func (PromptAssembler) ToolCatalog(definitions []ToolDefinition) string {
	if len(definitions) == 0 {
		return ""
	}
	lines := make([]string, 0, len(definitions)+2)
	lines = append(lines, "# Available tools", "Only the following tools are available in this turn; their schemas are authoritative:")
	for _, definition := range definitions {
		hint := strings.TrimSpace(definition.PromptHint)
		if hint == "" {
			hint = strings.TrimSpace(definition.Description)
		}
		line := "- `" + definition.Name + "`"
		if hint != "" {
			line += ": " + hint
		}
		if len(definition.Guidelines) > 0 {
			line += " Guidance: " + strings.Join(definition.Guidelines, " ")
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (PromptAssembler) Stage(stage string) string {
	switch stage {
	case "plan":
		return "本轮必须先单独调用 update_plan，提交完整、具体、面向用户结果的语义子任务清单。初次计划不得把任何项目标记 completed，且必须恰好把第一项标记 in_progress，其余为 pending；禁止用工具调用或通用占位语句冒充计划项。"
	case "steering_replan":
		return "本轮必须先单独调用 update_plan，基于用户 Steering 重规划未完成工作。保留真实完成项，最多一项为 in_progress，标题描述用户结果而不是工具调用。"
	case "permission_replan":
		return "本轮必须先单独调用 update_plan，基于已批准的授权范围更新完整语义计划。保留真实完成项，最多一项为 in_progress；不得把权限动作本身当作目标。"
	case "budget_finish":
		return "用户选择用现有结果收尾。停止扩展调研和重复校验；基于已经取得的证据与产物直接完成最终答复，明确仍存在的缺口。只有交付所必需的登记或最终验证可以继续调用工具。"
	default:
		return ""
	}
}

func sceneFromSnapshot(snapshot string) string {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) == nil {
		if scene, _ := config["scene"].(string); scene == core.SceneCode || scene == core.SceneWork {
			return scene
		}
	}
	return core.SceneWork
}

func frozenProjectInstructions(snapshot string) string {
	var config struct {
		Instructions []struct {
			Path        string `json:"path"`
			ContentHash string `json:"content_hash"`
			Content     string `json:"content"`
		} `json:"project_instructions"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil || len(config.Instructions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(config.Instructions))
	for _, instruction := range config.Instructions {
		if strings.TrimSpace(instruction.Content) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("## %s (%s)\n%s", instruction.Path, instruction.ContentHash, instruction.Content))
	}
	return strings.Join(parts, "\n\n")
}
