package application

import (
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// normalizeSkillManifest accepts both Oktapod's JSON envelope and the
// portable SKILL.md shape used by other agent runtimes. Markdown frontmatter
// is reduced to an explicit, bounded manifest rather than preserving unknown
// YAML fields that could silently widen runtime behavior.
func normalizeSkillManifest(raw, fallbackName string) (manifestJSON, name string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fallbackName, fmt.Errorf("Skill manifest is required")
	}
	var existing map[string]any
	if json.Unmarshal([]byte(raw), &existing) == nil {
		return raw, strings.TrimSpace(fallbackName), nil
	}
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", fallbackName, fmt.Errorf("Skill manifest must be JSON or SKILL.md with YAML frontmatter")
	}
	closing := strings.Index(normalized[4:], "\n---\n")
	if closing < 0 {
		return "", fallbackName, fmt.Errorf("SKILL.md frontmatter is not closed")
	}
	closing += 4
	frontmatter := normalized[4:closing]
	body := strings.TrimSpace(normalized[closing+5:])
	if body == "" {
		return "", fallbackName, fmt.Errorf("SKILL.md instruction body is required")
	}
	var metadata map[string]any
	if err := yaml.Unmarshal([]byte(frontmatter), &metadata); err != nil {
		return "", fallbackName, fmt.Errorf("invalid SKILL.md frontmatter: %w", err)
	}
	name = firstMetadataString(metadata, "name")
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	description := firstMetadataString(metadata, "description")
	if description == "" {
		description = firstInstructionLine(body)
	}
	manifest := map[string]any{
		"format":       "skill.md/v1",
		"description":  description,
		"instructions": body,
	}
	if triggers := metadataStringList(metadata, "triggers", "keywords"); len(triggers) > 0 {
		manifest["triggers"] = triggers
	}
	if tools := metadataStringList(metadata, "allowed-tools", "allowed_tools"); len(tools) > 0 {
		manifest["allowed_tools"] = tools
	}
	for _, key := range []string{"model-invocable", "model_invocable"} {
		value, exists := metadata[key]
		if !exists {
			continue
		}
		invocable, ok := value.(bool)
		if !ok {
			return "", fallbackName, fmt.Errorf("SKILL.md %s must be a boolean", key)
		}
		manifest["model_invocable"] = invocable
		break
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", fallbackName, err
	}
	return string(encoded), name, nil
}

func firstMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func metadataStringList(metadata map[string]any, keys ...string) []string {
	for _, key := range keys {
		value, exists := metadata[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case string:
			parts := strings.FieldsFunc(typed, func(char rune) bool { return char == ',' || char == '\n' })
			result := make([]string, 0, len(parts))
			for _, part := range parts {
				if part = strings.TrimSpace(part); part != "" {
					result = append(result, part)
				}
			}
			return result
		case []any:
			result := make([]string, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					result = append(result, strings.TrimSpace(text))
				}
			}
			return result
		}
	}
	return nil
}

func firstInstructionLine(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if line != "" {
			return line
		}
	}
	return "Imported Skill"
}
