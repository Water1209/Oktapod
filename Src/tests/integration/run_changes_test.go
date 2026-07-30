package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestRunChangesFreezesGitBaselineAndReadsCurrentDiff(t *testing.T) {
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	workspaceRoot := filepath.Join(filepath.Dir(databasePath), "workspaces")
	executor, err := workspace.NewLocal(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewService(
		repository,
		core.ProfilePersonal,
		"https://provider.invalid/v1",
		core.Actor{ID: testUserID, DisplayName: "Test User"},
		core.Organization{ID: testOrgID, Name: "Test Org"},
		func() string { return "" },
	).WithChatToolExecutor(executor, workspaceRoot)
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{
		Name: "Changes project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	projectRoot := filepath.Join(workspaceRoot, project.ID)
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitForChangesTest(t, projectRoot, "init")
	runGitForChangesTest(t, projectRoot, "config", "user.email", "test@example.invalid")
	runGitForChangesTest(t, projectRoot, "config", "user.name", "Oktapod Test")
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForChangesTest(t, projectRoot, "add", "tracked.txt")
	runGitForChangesTest(t, projectRoot, "commit", "-m", "baseline")

	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{
		Goal: "change the tracked file", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate,
	}, "run-changes", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "tracked.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changes, err := service.GetRunChanges(context.Background(), task.RunID, "run_start")
	if err != nil {
		t.Fatal(err)
	}
	if changes.BaselineHead == "" || changes.CurrentHead == "" || changes.BaselineHead != changes.CurrentHead {
		t.Fatalf("unexpected baseline/current head: %+v", changes)
	}
	if changes.DirtyAtStart || !strings.Contains(changes.Diff, "+after") {
		t.Fatalf("run changes did not preserve the clean baseline: %+v", changes)
	}
}

func runGitForChangesTest(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
