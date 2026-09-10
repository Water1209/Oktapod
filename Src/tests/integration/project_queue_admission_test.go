package integration_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestProjectAdmissionWaitsForCanceledExecutorAcknowledgement(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID}, core.Organization{ID: testOrgID}, func() string { return "" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Cancel acknowledgement", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "running predecessor", Mode: core.ModeSingleAgent}, "", "cancel-ack-first")
	if err != nil || first.Task == nil {
		t.Fatalf("first: %#v %v", first, err)
	}
	step, err := repository.ClaimNextStep(t.Context(), "still-executing", time.Now(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	next, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "waiting successor", Mode: core.ModeSingleAgent}, "", "cancel-ack-next")
	if err != nil || !next.Queued {
		t.Fatalf("next: %#v %v", next, err)
	}
	if _, err := service.ControlTask(t.Context(), first.Task.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "", "cancel-ack-stop"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, next.QueueItem.ID); !errors.Is(err, core.ErrNoWork) {
		t.Fatalf("cancel released a still-executing predecessor: %v", err)
	}
	if blocker, err := repository.ConversationBlockingTask(t.Context(), queueTestScope(), project.ID, first.Conversation.ID); err != nil || blocker != first.Task.ID {
		t.Fatalf("missing canceled blocker: %q %v", blocker, err)
	}
	if err := repository.AcknowledgeStoppedStep(t.Context(), step); err != nil {
		t.Fatal(err)
	}
	claim, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, next.QueueItem.ID)
	if err != nil {
		t.Fatalf("stopped executor did not release project: %v", err)
	}
	if _, err := service.StartQueuedConversationTurn(t.Context(), claim); err != nil {
		t.Fatal(err)
	}
}

func TestProjectAdmissionCapturesBaselineAfterPredecessorAndRestart(t *testing.T) {
	repository, databasePath := newRepository(t)
	defer func() { _ = repository.Close() }()
	bootstrapRepository(t, repository)
	workspaceRoot := filepath.Join(filepath.Dir(databasePath), "workspaces")
	executor, err := workspace.NewLocal(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	newService := func() *application.Service {
		return application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID}, core.Organization{ID: testOrgID}, func() string { return "" }).WithChatToolExecutor(executor, workspaceRoot)
	}
	service := newService()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Late baseline", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workspaceRoot, project.ID)
	runGitForChangesTest(t, root, "config", "user.email", "queue@example.invalid")
	runGitForChangesTest(t, root, "config", "user.name", "Queue Test")
	if err := os.WriteFile(filepath.Join(root, "sentinel.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForChangesTest(t, root, "add", "sentinel.txt")
	runGitForChangesTest(t, root, "commit", "-m", "initial sentinel")
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "predecessor", Mode: core.ModeSingleAgent}, "", "baseline-first")
	if err != nil || first.Task == nil {
		t.Fatalf("first: %#v %v", first, err)
	}
	next, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "successor", Mode: core.ModeSingleAgent}, "", "baseline-next")
	if err != nil || !next.Queued || next.Task == nil || next.Task.ID != first.Task.ID {
		t.Fatalf("queued: %#v %v", next, err)
	}
	if err := os.WriteFile(filepath.Join(root, "sentinel.txt"), []byte("predecessor finished\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Use the successor instruction sentinel.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForChangesTest(t, root, "add", "sentinel.txt", "AGENTS.md")
	runGitForChangesTest(t, root, "commit", "-m", "predecessor writes completed")
	if _, err := service.ControlTask(t.Context(), first.Task.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "", "baseline-stop"); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository, err = sqlite.Open(t.Context(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	service = newService()
	claim, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, next.QueueItem.ID)
	if err != nil {
		t.Fatal(err)
	}
	started, err := service.StartQueuedConversationTurn(t.Context(), claim)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := service.GetRunChanges(t.Context(), started.RunID, "run_start")
	if err != nil {
		t.Fatal(err)
	}
	if changes.BaselineHead == "" || changes.BaselineHead != changes.CurrentHead || changes.DirtyAtStart || strings.Contains(changes.Diff, "+predecessor finished") {
		t.Fatalf("successor froze an old baseline: %+v", changes)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var snapshot string
	if err := db.QueryRowContext(t.Context(), `SELECT config_snapshot_json FROM runs WHERE id = ?`, started.RunID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, "successor instruction sentinel") {
		t.Fatalf("project instructions were frozen before admission: %s", snapshot)
	}
	if _, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, next.QueueItem.ID); !errors.Is(err, core.ErrNoWork) {
		t.Fatalf("started twice after restart: %v", err)
	}
}

func TestConversationAdmissionSerializesTurnsAndFencesStaleStarter(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID}, core.Organization{ID: testOrgID}, func() string { return "" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Project queue", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "first task", Mode: core.ModeSingleAgent}, "", "first-turn-001")
	if err != nil || first.Task == nil {
		t.Fatalf("first admission: %v", err)
	}
	second, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "second turn", Mode: core.ModeSingleAgent}, "", "second-turn-001")
	if err != nil || !second.Queued || second.Task == nil || second.Task.ID != first.Task.ID || second.Conversation.ID != first.Conversation.ID || second.BlockingTaskID != first.Task.ID {
		t.Fatalf("same project did not queue: %#v %v", second, err)
	}
	_, err = service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{ConversationID: first.Conversation.ID, Goal: "legacy direct busy", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "", "direct-busy-001")
	var busy *core.ResourceConflictError
	if !errors.As(err, &busy) {
		t.Fatalf("legacy direct entry did not reject busy project: %v", err)
	}
	other, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Other project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := service.SubmitProjectTurn(t.Context(), other.ID, core.ProjectTurnInput{Content: "independent task", Mode: core.ModeSingleAgent}, "", "parallel-turn-001")
	if err != nil || parallel.Task == nil {
		t.Fatalf("different project was serialized: %v", err)
	}
	if _, err := service.ControlTask(t.Context(), first.Task.ID, core.ControlTaskInput{Command: core.TaskCommandPause}, "", "pause-first-001"); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, ""); !errors.Is(err, core.ErrNoWork) {
		t.Fatalf("paused task released project: %v", err)
	}
	if _, err := service.ControlTask(t.Context(), first.Task.ID, core.ControlTaskInput{Command: core.TaskCommandCancel}, "", "cancel-first-001"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claimed := make(chan core.ConversationQueueItem, 2)
	for range 2 {
		wg.Go(func() {
			item, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, "")
			if err == nil {
				claimed <- item
			} else if !errors.Is(err, core.ErrNoWork) {
				t.Errorf("claim: %v", err)
			}
		})
	}
	wg.Wait()
	close(claimed)
	if len(claimed) != 1 {
		t.Fatalf("two workers claimed %d starters", len(claimed))
	}
	old := <-claimed
	if err := repository.RequeueConversationQueueItem(t.Context(), queueTestScope(), old.ID, old.ClaimVersion); err != nil {
		t.Fatal(err)
	}
	current, err := repository.ClaimProjectFollowUp(t.Context(), queueTestScope(), project.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if current.ClaimVersion <= old.ClaimVersion {
		t.Fatal("claim version did not advance")
	}
	if _, err := service.StartQueuedConversationTurn(t.Context(), old); !errors.As(err, &busy) {
		t.Fatalf("stale starter created a Run: %v", err)
	}
	if err := repository.RequeueConversationQueueItem(t.Context(), queueTestScope(), old.ID, old.ClaimVersion); err != nil {
		t.Fatal(err)
	}
	started, err := service.StartQueuedConversationTurn(t.Context(), current)
	if err != nil || started.ConversationID != second.Conversation.ID {
		t.Fatalf("current starter failed after stale release: %v", err)
	}
	if _, err := service.ControlTask(t.Context(), first.Task.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, "", "retry-busy-001"); err == nil {
		t.Fatal("retry bypassed project exclusion")
	}
}

func TestSameProjectConversationsExecuteIndependently(t *testing.T) {
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID}, core.Organization{ID: testOrgID}, func() string { return "" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Parallel conversations", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "first independent task", Mode: core.ModeSingleAgent}, "", "parallel-first")
	if err != nil || first.Task == nil {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "second independent task", Mode: core.ModeSingleAgent}, "", "parallel-second")
	if err != nil || second.Task == nil || second.Queued || second.Conversation.ID == first.Conversation.ID {
		t.Fatalf("second: %+v %v", second, err)
	}
	claimedRuns := map[string]bool{}
	for i := range 2 {
		step, err := repository.ClaimNextStep(t.Context(), []string{"worker-a", "worker-b"}[i], time.Now(), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		claimedRuns[step.RunID] = true
	}
	if !claimedRuns[first.Task.RunID] || !claimedRuns[second.Task.RunID] {
		t.Fatalf("both threads did not execute: %+v", claimedRuns)
	}
	db := openTaskControlDatabase(t, databasePath)
	defer db.Close()
	if _, err := db.Exec(`UPDATE tasks SET status = 'waiting_approval' WHERE id = ?; UPDATE runs SET status = 'waiting_approval' WHERE id = ?; UPDATE steps SET status = 'waiting_approval', lease_until = '' WHERE run_id = ?`, first.Task.ID, first.Task.RunID, first.Task.RunID); err != nil {
		t.Fatal(err)
	}
	queued, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "same conversation follow up", Mode: core.ModeSingleAgent}, "", "parallel-followup")
	if err != nil || !queued.Queued {
		t.Fatalf("same-thread follow-up: %+v %v", queued, err)
	}
	third, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "third independent task", Mode: core.ModeSingleAgent}, "", "parallel-third")
	if err != nil || third.Task == nil || third.Queued {
		t.Fatalf("approval blocked another thread: %+v %v", third, err)
	}
	step, err := repository.ClaimNextStep(t.Context(), "worker-c", time.Now(), time.Now().Add(time.Minute))
	if err != nil || step.RunID != third.Task.RunID {
		t.Fatalf("third could not execute: %+v %v", step, err)
	}
}
