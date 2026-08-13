package application

import (
	"context"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type projectInstructionWorkspace struct {
	files map[string]string
}

func (w projectInstructionWorkspace) Execute(_ context.Context, _ string, call core.ToolCall) core.ToolExecution {
	path, _ := call.Arguments["path"].(string)
	content, ok := w.files[path]
	if !ok {
		return core.ToolExecution{Status: core.StatusFailed}
	}
	return core.ToolExecution{Status: core.StatusSucceeded, Output: map[string]any{
		"path": path, "content": content, "content_hash": "sha256:" + strings.Repeat("a", 64),
	}}
}

func TestSnapshotProjectInstructionsPrefersOverrideAndFreezesHash(t *testing.T) {
	service := &Service{workspaceReader: projectInstructionWorkspace{files: map[string]string{
		"AGENTS.override.md": "override guidance",
		"AGENTS.md":          "base guidance",
	}}}
	items := service.snapshotProjectInstructions(t.Context(), "project-id")
	if len(items) != 1 || items[0]["path"] != "AGENTS.override.md" || items[0]["content"] != "override guidance" {
		t.Fatalf("instructions=%#v", items)
	}
	if !strings.HasPrefix(items[0]["content_hash"].(string), "sha256:") {
		t.Fatalf("content hash=%#v", items[0]["content_hash"])
	}
}

func TestSnapshotProjectInstructionsFallsBackAndCapsBytes(t *testing.T) {
	service := &Service{workspaceReader: projectInstructionWorkspace{files: map[string]string{
		"AGENTS.md": strings.Repeat("界", maxProjectInstructionBytes),
	}}}
	items := service.snapshotProjectInstructions(t.Context(), "project-id")
	if len(items) != 1 || items[0]["path"] != "AGENTS.md" {
		t.Fatalf("instructions=%#v", items)
	}
	if len([]byte(items[0]["content"].(string))) > maxProjectInstructionBytes || items[0]["truncated"] != true {
		t.Fatalf("instruction cap not applied: bytes=%d truncated=%#v", len([]byte(items[0]["content"].(string))), items[0]["truncated"])
	}
}
