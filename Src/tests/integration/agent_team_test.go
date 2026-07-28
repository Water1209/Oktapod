package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
	_ "modernc.org/sqlite"
)

func TestAgentTeamPersistsWorkerEvidenceBeforeSynthesis(t *testing.T) {
	t.Parallel()

	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-team-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "team-model"}}})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "team-request",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "Worker evidence is complete and ready for synthesis."}}},
				"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 8},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-team-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Team project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Research, build, and review a change", Acceptance: "Every worker must provide evidence", Mode: core.ModeAgentTeam, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000201", "")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("team-test-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-team-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	for i := 0; i < 8; i++ {
		worked, runErr := worker.RunOnce(context.Background())
		if runErr != nil {
			t.Fatal(runErr)
		}
		if !worked {
			break
		}
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != core.StatusSucceeded {
		t.Fatalf("agent team status=%s error=%s summary=%q", completed.Status, completed.ErrorCode, completed.ResultSummary)
	}
	artifacts, err := service.ListRunArtifacts(context.Background(), completed.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 3 {
		t.Fatalf("artifacts=%d, want 3 real worker evidence files: %#v", len(artifacts), artifacts)
	}
	workerEvidence := 0
	for _, artifact := range artifacts {
		if artifact.SourceType == "agent_team_worker" {
			workerEvidence++
			if artifact.Kind != "evidence" || artifact.ContentHash == "" {
				t.Fatalf("invalid worker artifact: %#v", artifact)
			}
		}
	}
	if workerEvidence != 3 {
		t.Fatalf("worker evidence artifacts=%d, want 3", workerEvidence)
	}
}

func TestAgentTeamAcceptsPerTaskRoleDefinition(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Dynamic team", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Run a two-role team", Mode: core.ModeAgentTeam, Visibility: core.VisibilityPrivate, TeamRoles: []core.TeamRole{{Role: "researcher", Name: "Research"}, {Role: "reviewer", Name: "Review"}}}, "team-role-correlation", "team-role-task")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+databasePath+"?")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var payload string
	if err := db.QueryRow(`SELECT payload_json FROM run_events WHERE run_id = ? AND event_type = 'agent_team.created'`, task.RunID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"max_workers":2`) {
		t.Fatalf("team event payload=%s", payload)
	}
}

func TestAgentTeamRetriesOneTransientWorkerFailureAndPassesTerminationGate(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-team-retry-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "team-retry-model"}}})
		case "/v1/chat/completions":
			if calls.Add(1) == 2 {
				http.Error(w, "transient provider failure", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "team-retry-request", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "Evidence is complete."}}}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 7}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-team-retry-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Team retry", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "recover one worker", Mode: core.ModeAgentTeam, Visibility: core.VisibilityPrivate, TeamRoles: []core.TeamRole{{Role: "researcher", Name: "Research"}}}, "team-retry-correlation", "team-retry-task")
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("team-retry-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-team-retry-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	for i := 0; i < 10; i++ {
		worked, runErr := worker.RunOnce(context.Background())
		if runErr != nil {
			t.Fatal(runErr)
		}
		if !worked {
			break
		}
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		t.Fatalf("task=%#v err=%v provider_calls=%d", completed, err, calls.Load())
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenRetry, seenGate := false, false
	for _, event := range events {
		seenRetry = seenRetry || event.EventType == "agent.worker_retry_queued"
		seenGate = seenGate || event.EventType == "agent_team.termination_gate"
	}
	if !seenRetry || !seenGate {
		t.Fatalf("recovery events missing retry=%v gate=%v events=%#v", seenRetry, seenGate, events)
	}
}

func TestAgentTeamRecoversExpiredCoordinatorLeaseAfterWorkerRestart(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-team-recovery-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "team-recovery-model"}}})
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "team-recovery-request", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "Recovered evidence is complete."}}}, "usage": map[string]any{"prompt_tokens": 5, "completion_tokens": 7}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-team-recovery-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Team process recovery", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "recover coordinator after process loss", Mode: core.ModeAgentTeam, Visibility: core.VisibilityPrivate, TeamRoles: []core.TeamRole{{Role: "researcher", Name: "Research"}}}, "team-process-recovery", "team-process-recovery")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a process that died after claiming the coordinator step but
	// before committing its result. The next Worker must fence and recover it.
	if _, err := repository.ClaimNextStep(context.Background(), "crashed-team-worker", time.Now().UTC(), time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("restarted-team-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-team-recovery-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	for i := 0; i < 12; i++ {
		worked, runErr := worker.RunOnce(context.Background())
		if runErr != nil {
			t.Fatal(runErr)
		}
		if !worked {
			break
		}
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		t.Fatalf("recovered team task=%#v err=%v", completed, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	seenRecovery := false
	for _, event := range events {
		seenRecovery = seenRecovery || event.EventType == "step.recovered"
	}
	if !seenRecovery {
		t.Fatalf("missing coordinator recovery event: %#v", events)
	}
}
