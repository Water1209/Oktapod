package integration_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestAgentTeamCoordinatorProcessKillRecoversAndConverges(t *testing.T) {
	if os.Getenv("OKTAPOD_AGENT_TEAM_PROCESS_CHILD") == "1" {
		runAgentTeamProcessChild(t)
		return
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-team-process-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "team-process-model"}}})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "team-process-request", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "Process recovery evidence is complete."}}}, "usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 6}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	workspaceRoot := filepath.Join(root, "workspaces")
	store, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRepository(t, store)
	service := application.NewService(store, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-team-process-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Agent Team process kill", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "recover coordinator after process kill", Mode: core.ModeAgentTeam, Visibility: core.VisibilityPrivate, TeamRoles: []core.TeamRole{{Role: "researcher", Name: "Research"}}}, "agent-team-process-task", "agent-team-process-idempotency")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=TestAgentTeamCoordinatorProcessKillRecoversAndConverges")
	child.Env = append(os.Environ(), "OKTAPOD_AGENT_TEAM_PROCESS_CHILD=1", "OKTAPOD_AGENT_TEAM_PROCESS_DB="+databasePath, "OKTAPOD_AGENT_TEAM_PROCESS_WORKSPACE="+workspaceRoot)
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() == "" {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("child did not claim coordinator step: %v", scanner.Err())
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
	time.Sleep(700 * time.Millisecond)
	worker := okruntime.NewWorker("after-agent-team-process-kill", reopened, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-team-process-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	for i := 0; i < 16; i++ {
		worked, runErr := worker.RunOnce(context.Background())
		if runErr != nil {
			t.Fatal(runErr)
		}
		if !worked {
			break
		}
	}
	recoveredService := application.NewService(reopened, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-team-process-key" })
	completed, err := recoveredService.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		t.Fatalf("recovered team task=%#v err=%v", completed, err)
	}
	events, err := recoveredService.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenRecovery := false
	for _, event := range events {
		seenRecovery = seenRecovery || event.EventType == "step.recovered"
	}
	if !seenRecovery {
		t.Fatalf("missing process-level coordinator recovery event: %#v", events)
	}
}

func runAgentTeamProcessChild(t *testing.T) {
	databasePath := os.Getenv("OKTAPOD_AGENT_TEAM_PROCESS_DB")
	workspaceRoot := os.Getenv("OKTAPOD_AGENT_TEAM_PROCESS_WORKSPACE")
	store, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	claimed, err := store.ClaimNextStep(context.Background(), "agent-team-child", time.Now().UTC(), time.Now().UTC().Add(400*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, claimed.ID)
	_ = os.Stdout.Sync()
	time.Sleep(10 * time.Second)
}
