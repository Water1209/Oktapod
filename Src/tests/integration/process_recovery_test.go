package integration_test

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestWorkerLeaseCanBeRecoveredAfterStoreReopen(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	workspaceRoot := filepath.Join(root, "workspaces")
	first, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRepository(t, first)
	service := application.NewService(first, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Restart recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "recover after restart", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "restart-correlation", "restart-task"); err != nil {
		first.Close()
		t.Fatal(err)
	}
	claimed, err := first.ClaimNextStep(context.Background(), "crashed-worker", time.Now().UTC(), time.Now().UTC().Add(time.Second))
	if err != nil {
		first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	recovered, err := second.ClaimNextStep(context.Background(), "restarted-worker", time.Now().UTC().Add(2*time.Second), time.Now().UTC().Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ID != claimed.ID || !recovered.Recovery || recovered.AttemptNumber < 2 {
		t.Fatalf("recovered=%#v claimed=%#v", recovered, claimed)
	}
}

// This test deliberately terminates a child process after it claims a step.
// It proves recovery does not depend on a graceful Store.Close or Worker
// shutdown path. Process.Kill is available on both Windows and Unix; the
// repository's lease/fence protocol is what makes the next claim safe.
func TestWorkerLeaseCanBeRecoveredAfterProcessKill(t *testing.T) {
	if os.Getenv("OKTAPOD_PROCESS_RECOVERY_CHILD") == "1" {
		runProcessRecoveryChild(t)
		return
	}
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	workspaceRoot := filepath.Join(root, "workspaces")
	store, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRepository(t, store)
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Process kill recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "recover after process kill", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "process-kill-correlation", "process-kill-task"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	child := exec.Command(os.Args[0], "-test.run=TestWorkerLeaseCanBeRecoveredAfterProcessKill")
	child.Env = append(os.Environ(), "OKTAPOD_PROCESS_RECOVERY_CHILD=1", "OKTAPOD_PROCESS_RECOVERY_DB="+databasePath, "OKTAPOD_PROCESS_RECOVERY_WORKSPACE="+workspaceRoot)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("child did not claim a step: %v", scanner.Err())
	}
	if scanner.Text() == "" {
		t.Fatal("child claim marker was empty")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()

	reopened, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	time.Sleep(750 * time.Millisecond)
	recovered, err := reopened.ClaimNextStep(context.Background(), "after-kill-worker", time.Now().UTC(), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.Recovery || recovered.AttemptNumber < 2 {
		t.Fatalf("recovered=%#v; expected a recovery attempt after child process kill", recovered)
	}
}

func runProcessRecoveryChild(t *testing.T) {
	databasePath := os.Getenv("OKTAPOD_PROCESS_RECOVERY_DB")
	workspaceRoot := os.Getenv("OKTAPOD_PROCESS_RECOVERY_WORKSPACE")
	store, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	claimed, err := store.ClaimNextStep(context.Background(), "child-worker", time.Now().UTC(), time.Now().UTC().Add(500*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println(claimed.ID)
	_ = os.Stdout.Sync()
	time.Sleep(10 * time.Second)
}
