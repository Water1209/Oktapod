package application

import (
	"encoding/json"
	"testing"
)

func TestNormalizeSkillManifestImportsPortableMarkdown(t *testing.T) {
	raw := `---
name: release-check
description: Verify a release with evidence
triggers:
  - 发布检查
  - release check
allowed-tools: web_search, workspace_run_command
model-invocable: false
---
# Release check

Verify tests and cite current release notes.`
	manifestJSON, name, err := normalizeSkillManifest(raw, "SKILL")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	if name != "release-check" || manifest["format"] != "skill.md/v1" || manifest["instructions"] == "" {
		t.Fatalf("name=%q manifest=%#v", name, manifest)
	}
	if triggers, ok := manifest["triggers"].([]any); !ok || len(triggers) != 2 {
		t.Fatalf("triggers=%#v", manifest["triggers"])
	}
	if manifest["model_invocable"] != false {
		t.Fatalf("model_invocable=%#v", manifest["model_invocable"])
	}
}

func TestNormalizeSkillManifestRejectsNonBooleanModelInvocable(t *testing.T) {
	raw := "---\nname: release-check\nmodel-invocable: no\n---\n\nVerify the release."
	if _, _, err := normalizeSkillManifest(raw, "skill"); err == nil {
		t.Fatal("non-boolean model-invocable was accepted")
	}
}

func TestNormalizeSkillManifestRejectsUnstructuredMarkdown(t *testing.T) {
	if _, _, err := normalizeSkillManifest("# no frontmatter", "skill"); err == nil {
		t.Fatal("unstructured markdown was accepted")
	}
}
