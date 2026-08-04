package application

import (
	"encoding/json"
	"testing"
)

func TestNormalizeSkillManifestImportsPortableMarkdown(t *testing.T) {
	raw := `---
name: release-check
description: Verify a release with evidence
allowed-tools: web_search, workspace_run_command
metadata:
  category: release
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
	if metadata, ok := manifest["metadata"].(map[string]any); !ok || metadata["category"] != "release" {
		t.Fatalf("metadata=%#v", manifest["metadata"])
	}
}

func TestNormalizeSkillManifestRejectsUnknownFrontmatter(t *testing.T) {
	raw := "---\nname: release-check\ndescription: Verify releases.\nmodel-invocable: no\n---\n\nVerify the release."
	if _, _, err := normalizeSkillManifest(raw, "skill"); err == nil {
		t.Fatal("non-boolean model-invocable was accepted")
	}
}

func TestNormalizeSkillManifestRejectsUnstructuredMarkdown(t *testing.T) {
	if _, _, err := normalizeSkillManifest("# no frontmatter", "skill"); err == nil {
		t.Fatal("unstructured markdown was accepted")
	}
}
