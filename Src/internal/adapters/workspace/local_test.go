package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const workspaceTestProjectID = "00000000-0000-7000-8000-000000000099"

func TestWorkspaceReadListFindGrepEditAndConflict(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	if err := os.MkdirAll(filepath.Join(projectRoot, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	initial := []byte("first\nAPI_KEY=should-not-leak\nneedle here\nlast\n")
	path := filepath.Join(projectRoot, "src", "main.txt")
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	read := executeWorkspaceTest(t, executor, core.ToolWorkspaceReadFile, map[string]any{"path": "src/main.txt", "offset_line": float64(1), "limit_lines": float64(2)})
	if read.Status != core.StatusSucceeded || strings.Contains(read.Output["content"].(string), "should-not-leak") || read.Output["next_cursor"] != 3 {
		t.Fatalf("read=%+v", read)
	}
	listed := executeWorkspaceTest(t, executor, core.ToolWorkspaceListFiles, map[string]any{"recursive": true, "cursor": float64(0), "limit": float64(1)})
	if listed.Status != core.StatusSucceeded || listed.Output["next_cursor"] == nil {
		t.Fatalf("list=%+v", listed)
	}
	found := executeWorkspaceTest(t, executor, core.ToolWorkspaceFindFiles, map[string]any{"pattern": "*.txt", "cursor": float64(0), "limit": float64(20)})
	if found.Output["total_matches"] != 1 {
		t.Fatalf("find=%+v", found)
	}
	grep := executeWorkspaceTest(t, executor, core.ToolWorkspaceGrep, map[string]any{"query": "needle", "glob": "*.txt", "cursor": float64(0), "limit": float64(20)})
	if grep.Output["total_matches"] != 1 {
		t.Fatalf("grep=%+v", grep)
	}
	edited := executeWorkspaceTest(t, executor, core.ToolWorkspaceEditFile, map[string]any{
		"path": "src/main.txt", "preimage_hash": hashBytes(initial),
		"old_text": "needle here", "new_text": "replacement",
	})
	if edited.Status != core.StatusSucceeded {
		t.Fatalf("edit=%+v", edited)
	}
	conflict := executeWorkspaceTest(t, executor, core.ToolWorkspaceEditFile, map[string]any{
		"path": "src/main.txt", "preimage_hash": hashBytes(initial),
		"old_text": "replacement", "new_text": "wrong",
	})
	if conflict.ErrorCode != "WORKSPACE_EDIT_CONFLICT" {
		t.Fatalf("conflict=%+v", conflict)
	}
}

func TestWorkspaceAcceptsConciseReadAlias(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	if err := os.WriteFile(filepath.Join(projectRoot, "README.md"), []byte("alias works"), 0o600); err != nil {
		t.Fatal(err)
	}
	execution := executeWorkspaceTest(t, executor, "read", map[string]any{"path": "README.md"})
	if execution.Status != core.StatusSucceeded || execution.Output["content"] != "alias works" {
		t.Fatalf("alias execution=%#v", execution)
	}
}

func TestWorkspaceRejectsBinaryAndSymlinkTraversal(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	if err := os.WriteFile(filepath.Join(projectRoot, "binary.bin"), []byte{0, 1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := executeWorkspaceTest(t, executor, core.ToolWorkspaceReadFile, map[string]any{"path": "binary.bin"})
	if binary.Status != core.StatusFailed || binary.ErrorCode != "WORKSPACE_READ_FAILED" {
		t.Fatalf("binary=%+v", binary)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(projectRoot, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	symlink := executeWorkspaceTest(t, executor, core.ToolWorkspaceReadFile, map[string]any{"path": "link.txt"})
	if symlink.ErrorCode != "WORKSPACE_PATH_DENIED" {
		t.Fatalf("symlink=%+v", symlink)
	}
}

func TestGovernedHostEnvironmentIsolatesHomeSecretsAndGitConfiguration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SSH_AUTH_SOCK", "should-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "should-not-leak")
	t.Setenv("OKTAPOD_MODEL_API_KEY", "should-not-leak")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(root, "host.gitconfig"))

	environment, err := safeEnvironment(root)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, item := range environment {
		name, value, _ := strings.Cut(item, "=")
		values[name] = value
	}
	for _, denied := range []string{"SSH_AUTH_SOCK", "AWS_SECRET_ACCESS_KEY", "OKTAPOD_MODEL_API_KEY"} {
		if _, exists := values[denied]; exists {
			t.Fatalf("%s leaked into governed host environment", denied)
		}
	}
	if values["HOME"] == "" || values["HOME"] == os.Getenv("HOME") {
		t.Fatalf("HOME was not isolated: %q", values["HOME"])
	}
	if values["GIT_CONFIG_NOSYSTEM"] != "1" || values["GIT_TERMINAL_PROMPT"] != "0" || values["GIT_CONFIG_COUNT"] != "3" || values["GIT_CONFIG_VALUE_2"] != "false" {
		t.Fatalf("Git isolation is incomplete: %#v", values)
	}
}

func TestCommandOutputUsesBoundedExcerptBeforeArtifactPersistence(t *testing.T) {
	value := strings.Repeat("x", maxCommandOutput+32)
	excerpt, truncated := boundedTextExcerpt(value, maxCommandOutput)
	if !truncated || len(excerpt) <= maxCommandOutput || !strings.Contains(excerpt, "continued in Artifact") {
		t.Fatalf("unexpected bounded command excerpt: truncated=%v length=%d", truncated, len(excerpt))
	}
}

func TestWorkspaceShellNonZeroExitIsRecoverable(t *testing.T) {
	executor, _ := newWorkspaceTestExecutor(t)
	command := "exit 7"
	if goruntime.GOOS == "windows" {
		command = "exit /b 7"
	}
	execution := executeWorkspaceTest(t, executor, core.ToolWorkspaceShell, map[string]any{"command": command})
	if execution.Status != core.StatusFailed || execution.ErrorCode != "COMMAND_EXIT_NONZERO" || execution.ExitCode != 7 {
		t.Fatalf("shell execution=%+v", execution)
	}
}

func TestWorkspaceShellPreservesQuotedArguments(t *testing.T) {
	executor, _ := newWorkspaceTestExecutor(t)
	command := `printf '"quoted value"'`
	if goruntime.GOOS == "windows" {
		command = `echo "quoted value"`
	}
	execution := executeWorkspaceTest(t, executor, core.ToolWorkspaceShell, map[string]any{"command": command})
	if execution.Status != core.StatusSucceeded || !strings.Contains(execution.Output["stdout"].(string), `"quoted value"`) {
		t.Fatalf("quoted shell execution=%+v", execution)
	}
}

func TestWorkspaceCommandCancellationTerminatesProcessTree(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	if err := os.WriteFile(filepath.Join(projectRoot, "wait.go"), []byte("package main\nimport \"time\"\nfunc main() { for { time.Sleep(time.Second) } }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan core.ToolExecution, 1)
	go func() {
		done <- executor.Execute(ctx, workspaceTestProjectID, core.ToolCall{ID: "cancel-command", Name: core.ToolWorkspaceRunCommand, Arguments: map[string]any{
			"program": "go", "args": []any{"run", "wait.go"},
		}})
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case execution := <-done:
		if execution.Status != core.StatusFailed || execution.ErrorCode != "TOOL_CANCELED" {
			t.Fatalf("canceled command=%#v", execution)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("canceled command process tree did not terminate")
	}
}

func TestWorkspaceGitReadAndMutationPreconditions(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	runGitSetup(t, projectRoot, "init")
	runGitSetup(t, projectRoot, "config", "user.email", "test@example.invalid")
	runGitSetup(t, projectRoot, "config", "user.name", "Oktapod Test")
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "selected.txt"), []byte("selected initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, projectRoot, "add", "tracked.txt", "selected.txt")
	runGitSetup(t, projectRoot, "commit", "-m", "initial")
	status := executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	statusHash := status.Output["status_hash"].(string)
	baselineHead, _ := status.Output["head"].(string)
	if len(baselineHead) < 7 {
		t.Fatalf("git status did not expose a frozen HEAD: %+v", status)
	}
	branch := executeWorkspaceTest(t, executor, core.ToolGitBranch, map[string]any{"name": "feature/test", "expected_status_hash": statusHash})
	if branch.Status != core.StatusSucceeded {
		t.Fatalf("branch=%+v", branch)
	}
	stale := executeWorkspaceTest(t, executor, core.ToolGitBranch, map[string]any{"name": "feature/stale", "expected_status_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if stale.ErrorCode != "GIT_STATE_CONFLICT" {
		t.Fatalf("stale=%+v", stale)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := executeWorkspaceTest(t, executor, core.ToolGitDiff, map[string]any{"path": "tracked.txt", "staged": false})
	if diff.Status != core.StatusSucceeded || !strings.Contains(diff.Output["diff"].(string), "changed") {
		t.Fatalf("diff=%+v", diff)
	}
	baselineDiff := executeWorkspaceTest(t, executor, core.ToolGitDiff, map[string]any{"baseline": baselineHead})
	if baselineDiff.Status != core.StatusSucceeded || !strings.Contains(baselineDiff.Output["diff"].(string), "changed") {
		t.Fatalf("baseline diff=%+v", baselineDiff)
	}
	changedStatus := executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	restore := executeWorkspaceTest(t, executor, core.ToolGitRestore, map[string]any{"path": "tracked.txt", "expected_status_hash": changedStatus.Output["status_hash"].(string)})
	if restore.Status != core.StatusSucceeded {
		t.Fatalf("restore=%+v", restore)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, projectRoot, "add", "tracked.txt")
	stagedStatus := executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	commit := executeWorkspaceTest(t, executor, core.ToolGitCommit, map[string]any{"message": "update tracked", "expected_status_hash": stagedStatus.Output["status_hash"].(string)})
	if commit.Status != core.StatusSucceeded {
		t.Fatalf("commit=%+v", commit)
	}
	log := executeWorkspaceTest(t, executor, core.ToolGitLog, map[string]any{"limit": float64(5)})
	if log.Status != core.StatusSucceeded || !strings.Contains(log.Output["log"].(string), "update tracked") {
		t.Fatalf("log=%+v", log)
	}
	conflicts := executeWorkspaceTest(t, executor, core.ToolGitConflicts, map[string]any{})
	if conflicts.Status != core.StatusSucceeded || conflicts.Output["count"].(int) != 0 {
		t.Fatalf("conflicts=%+v", conflicts)
	}
	hunkSource := "one\n2\n3\n4\n5\n6\n7\n8\n9\nten\n"
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte(hunkSource), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitSetup(t, projectRoot, "add", "tracked.txt")
	runGitSetup(t, projectRoot, "commit", "-m", "prepare hunks")
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("ONE\n2\n3\n4\n5\n6\n7\n8\n9\nTEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hunkStatus := executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	hunk := executeWorkspaceTest(t, executor, core.ToolGitRestoreHunk, map[string]any{"path": "tracked.txt", "hunk_index": float64(0), "expected_status_hash": hunkStatus.Output["status_hash"].(string)})
	if hunk.Status != core.StatusSucceeded {
		t.Fatalf("hunk=%+v", hunk)
	}
	content, err := os.ReadFile(filepath.Join(projectRoot, "tracked.txt"))
	normalizedContent := strings.ReplaceAll(string(content), "\r\n", "\n")
	if err != nil || !strings.HasPrefix(normalizedContent, "one\n") || !strings.HasSuffix(normalizedContent, "TEN\n") {
		t.Fatalf("hunk content=%q err=%v", content, err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "selected.txt"), []byte("selected changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	selectedStatus := executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	selectedCommit := executeWorkspaceTest(t, executor, core.ToolGitCommitPaths, map[string]any{"message": "commit selected path", "paths": []any{"selected.txt"}, "expected_status_hash": selectedStatus.Output["status_hash"].(string)})
	if selectedCommit.Status != core.StatusSucceeded {
		t.Fatalf("selected commit=%+v", selectedCommit)
	}
	remaining := executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	if !strings.Contains(remaining.Output["status"].(string), "tracked.txt") || strings.Contains(remaining.Output["status"].(string), "selected.txt") {
		t.Fatalf("remaining status=%+v", remaining)
	}
}

func TestInitializeProjectTracksUntrackedDiffCommitAndRestore(t *testing.T) {
	executor, projectRoot := newWorkspaceTestExecutor(t)
	if err := executor.InitializeProject(context.Background(), workspaceTestProjectID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "created.txt"), []byte("created\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff := executeWorkspaceTest(t, executor, core.ToolGitDiff, map[string]any{})
	if diff.Status != core.StatusSucceeded || !strings.Contains(diff.Output["diff"].(string), "created.txt") {
		t.Fatalf("untracked diff=%+v", diff)
	}
	status := executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	commit := executeWorkspaceTest(t, executor, core.ToolGitCommitPaths, map[string]any{
		"message": "commit created file", "paths": []any{"created.txt"}, "expected_status_hash": status.Output["status_hash"].(string),
	})
	if commit.Status != core.StatusSucceeded {
		t.Fatalf("untracked commit=%+v", commit)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "discard.txt"), []byte("discard\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status = executeWorkspaceTest(t, executor, core.ToolGitStatus, map[string]any{})
	restore := executeWorkspaceTest(t, executor, core.ToolGitRestore, map[string]any{
		"path": "discard.txt", "expected_status_hash": status.Output["status_hash"].(string),
	})
	if restore.Status != core.StatusSucceeded {
		t.Fatalf("untracked restore=%+v", restore)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "discard.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked restore left file behind: %v", err)
	}
}

func newWorkspaceTestExecutor(t *testing.T) (*Local, string) {
	t.Helper()
	root := t.TempDir()
	projectRoot := filepath.Join(root, workspaceTestProjectID)
	if err := os.Mkdir(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	executor, err := NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	return executor, projectRoot
}

func executeWorkspaceTest(t *testing.T, executor *Local, name string, arguments map[string]any) core.ToolExecution {
	t.Helper()
	result := executor.Execute(context.Background(), workspaceTestProjectID, core.ToolCall{ID: "call-" + name, Name: name, Arguments: arguments})
	return result
}

func runGitSetup(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
