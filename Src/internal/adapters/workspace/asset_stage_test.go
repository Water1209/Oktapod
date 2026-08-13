package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestStageAssetCreatesReadOnlyRunScopedInputAndCleansIt(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	runID := "00000000-0000-7000-8000-000000000101"
	assetID := "00000000-0000-7000-8000-000000000102"
	content := []byte("frozen attachment")
	hash := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	result, err := executor.StageAsset(context.Background(), workspaceTestProjectID, runID, assetID, "source notes.md", bytes.NewReader(content), int64(len(content)), hash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Path, ".oktapod/inputs/"+runID+"/") || result.ContentHash != hash {
		t.Fatalf("result=%#v", result)
	}
	staged, err := os.ReadFile(filepath.Join(projectRoot, filepath.FromSlash(result.Path)))
	if err != nil || !bytes.Equal(staged, content) {
		t.Fatalf("staged=%q err=%v", staged, err)
	}
	if err := executor.CleanupRunInputs(context.Background(), workspaceTestProjectID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, ".oktapod", "inputs", runID)); !os.IsNotExist(err) {
		t.Fatalf("run inputs were not cleaned: %v", err)
	}
}

func TestWorkspaceListExcludesInternalInputsAndIncludesModifiedTime(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	if err := os.MkdirAll(filepath.Join(projectRoot, ".oktapod", "inputs", "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".oktapod", "inputs", "run", "secret.pdf"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".git", "objects", "internal"), []byte("hidden"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "output.pdf"), []byte("%PDF-1.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed := executeWorkspaceTest(t, executor, core.ToolWorkspaceListFiles, map[string]any{"recursive": true, "cursor": float64(0), "limit": float64(200)})
	if listed.Status != core.StatusSucceeded {
		t.Fatalf("listed=%#v", listed)
	}
	items, ok := listed.Output["items"].([]map[string]any)
	if !ok {
		t.Fatalf("items=%T", listed.Output["items"])
	}
	found := false
	for _, item := range items {
		path, _ := item["path"].(string)
		if strings.HasPrefix(path, ".oktapod") || strings.HasPrefix(path, ".git") {
			t.Fatalf("internal path leaked: %s", path)
		}
		if path == "output.pdf" {
			found = item["modified_at"] != ""
		}
	}
	if !found {
		t.Fatalf("public output or modified_at missing: %#v", items)
	}
}
