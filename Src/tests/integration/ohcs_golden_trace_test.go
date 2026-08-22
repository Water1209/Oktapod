package integration_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

// goldenTrace is the stable, provider-neutral projection used by OHCS.
// Provider request IDs, timestamps and free-form payloads are deliberately
// excluded: compatibility is defined by persisted ordering and roles.
type goldenTrace struct {
	EventTypes   []string
	EventSeqs    []int64
	MessageRoles []string
	MessageSeqs  []int64
}

func TestOHCSGoldenTraceTextTurn(t *testing.T) {
	t.Parallel()

	provider := newMockProvider(t)
	defer provider.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)

	service := application.NewService(
		repository,
		core.ProfilePersonal,
		provider.URL+"/v1",
		core.Actor{ID: testUserID, DisplayName: "OHCS User"},
		core.Organization{ID: testOrgID, Name: "OHCS Org"},
		func() string { return "fake-test-key" },
	)
	project, err := service.CreateProject(
		context.Background(),
		core.CreateProjectInput{Name: "OHCS text turn", Kind: core.ProjectKindPersonal, Scene: core.SceneCode},
		"",
	)
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := service.CreateTask(
		context.Background(),
		project.ID,
		core.CreateTaskInput{
			Goal:       "Explain the recovery invariant",
			Acceptance: "Mention fencing token",
			Mode:       core.ModeSingleAgent,
			Visibility: core.VisibilityPrivate,
		},
		"00000000-0000-7000-8000-000000000701",
		"ohcs-text-turn",
	)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	worker := okruntime.NewWorker(
		"ohcs-text-worker",
		repository,
		openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }),
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("run worker: worked=%v err=%v", worked, runErr)
	}

	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatalf("list run events: %v", err)
	}
	messages, err := service.ListMessages(context.Background(), task.ConversationID, 0)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}

	got := projectGoldenTrace(events, messages)
	want := goldenTrace{
		EventTypes: []string{
			"run.created",
			"step.started",
			"checkpoint.committed",
			"budget.reserved",
			"checkpoint.committed",
			"model.message",
			"step.succeeded",
		},
		EventSeqs:    []int64{1, 2, 3, 4, 5, 6, 7},
		MessageRoles: []string{"user", "assistant"},
		MessageSeqs:  []int64{1, 2},
	}
	assertGoldenTrace(t, got, want)
}

func projectGoldenTrace(events []core.RunEvent, messages []core.Message) goldenTrace {
	trace := goldenTrace{
		EventTypes:   make([]string, 0, len(events)),
		EventSeqs:    make([]int64, 0, len(events)),
		MessageRoles: make([]string, 0, len(messages)),
		MessageSeqs:  make([]int64, 0, len(messages)),
	}
	for _, event := range events {
		trace.EventTypes = append(trace.EventTypes, event.EventType)
		trace.EventSeqs = append(trace.EventSeqs, event.StreamSeq)
	}
	for _, message := range messages {
		trace.MessageRoles = append(trace.MessageRoles, message.Role)
		trace.MessageSeqs = append(trace.MessageSeqs, message.ServerSeq)
	}
	return trace
}

func assertGoldenTrace(t *testing.T, got, want goldenTrace) {
	t.Helper()
	assertStringSlice(t, "event types", got.EventTypes, want.EventTypes)
	assertInt64Slice(t, "event sequences", got.EventSeqs, want.EventSeqs)
	assertStringSlice(t, "message roles", got.MessageRoles, want.MessageRoles)
	assertInt64Slice(t, "message sequences", got.MessageSeqs, want.MessageSeqs)
}

func assertStringSlice(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length=%d want=%d: %#v", label, len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s[%d]=%q want=%q; full=%#v", label, index, got[index], want[index], got)
		}
	}
}

func assertInt64Slice(t *testing.T, label string, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length=%d want=%d: %#v", label, len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s[%d]=%d want=%d; full=%#v", label, index, got[index], want[index], got)
		}
	}
}
