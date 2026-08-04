package application

import (
	"encoding/json"
	"fmt"
	"regexp"
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
	allowed := map[string]bool{"name": true, "description": true, "license": true, "allowed-tools": true, "metadata": true}
	for key := range metadata {
		if !allowed[key] {
			return "", fallbackName, fmt.Errorf("SKILL.md frontmatter field %q is not supported", key)
		}
	}
	name = firstMetadataString(metadata, "name")
	if name == "" {
		name = strings.TrimSpace(fallbackName)
	}
	if len(name) > 64 || !regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`).MatchString(name) {
		return "", fallbackName, fmt.Errorf("SKILL.md name must use lowercase letters, digits, and single hyphens (max 64 characters)")
	}
	description := firstMetadataString(metadata, "description")
	if description == "" {
		return "", fallbackName, fmt.Errorf("SKILL.md description is required")
	}
	if len(description) > 1024 || strings.ContainsAny(description, "<>") {
		return "", fallbackName, fmt.Errorf("SKILL.md description must be at most 1024 characters and cannot contain angle brackets")
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
	if license := firstMetadataString(metadata, "license"); license != "" {
		manifest["license"] = license
	}
	if extra, ok := metadata["metadata"].(map[string]any); ok && len(extra) > 0 {
		manifest["metadata"] = extra
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
