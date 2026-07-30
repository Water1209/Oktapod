package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

const (
	testUserID = "00000000-0000-7000-8000-000000000101"
	testOrgID  = "00000000-0000-7000-8000-000000000102"
)

func TestPersonalTaskCompletesWithPersistedEventsAndUsage(t *testing.T) {
	t.Parallel()

	provider := newMockProvider(t)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)

	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Code project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Explain the recovery invariant", Acceptance: "Mention fencing token", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000103", "")
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	model := openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" })
	worker := okruntime.NewWorker("test-worker", repository, model, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	worked, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run worker: %v", err)
	}
	if !worked {
		t.Fatal("worker did not claim the queued step")
	}

	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if completed.Status != core.StatusSucceeded {
		t.Fatalf("task status = %s, want succeeded (error=%s result=%s)", completed.Status, completed.ErrorCode, completed.ResultSummary)
	}
	if completed.ResultSummary != "Fencing token prevents a stale worker from committing." {
		t.Fatalf("unexpected result: %q", completed.ResultSummary)
	}
	planItems, err := service.ListPlanItems(context.Background(), project.ID)
	if err != nil || len(planItems) != 1 || planItems[0].LinkedTaskID != completed.ID || planItems[0].Source != core.PlanSourceTask || planItems[0].Status != core.PlanStatusTodo {
		t.Fatalf("task plan item=%#v err=%v", planItems, err)
	}
	movedPlanItem, err := service.MovePlanItem(context.Background(), planItems[0].ID, core.MovePlanItemInput{Status: core.PlanStatusDone}, "move-plan-done-key")
	if err != nil || movedPlanItem.Status != core.PlanStatusDone {
		t.Fatalf("moved plan item=%#v err=%v", movedPlanItem, err)
	}
	unchangedTask, err := service.GetTask(context.Background(), completed.ID)
	if err != nil || unchangedTask.Status != core.StatusSucceeded {
		t.Fatalf("moving plan item mutated runtime task: task=%#v err=%v", unchangedTask, err)
	}
	if completed.ConversationID == "" || completed.ConversationID != task.ConversationID {
		t.Fatalf("conversation id was not persisted across task execution: created=%q completed=%q", task.ConversationID, completed.ConversationID)
	}
	conversations, err := service.ListConversations(context.Background(), project.ID)
	if err != nil || len(conversations) != 1 || conversations[0].MessageCount != 2 || conversations[0].LastMessage != completed.ResultSummary {
		t.Fatalf("conversations=%#v err=%v", conversations, err)
	}
	messages, err := service.ListMessages(context.Background(), completed.ConversationID, 0)
	if err != nil || len(messages) != 2 || messages[0].Role != "user" || messages[0].Content != "Explain the recovery invariant" || messages[1].Role != "assistant" || messages[1].Content != completed.ResultSummary || messages[0].ServerSeq != 1 || messages[1].ServerSeq != 2 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	messageTail, err := service.ListMessages(context.Background(), completed.ConversationID, 1)
	if err != nil || len(messageTail) != 1 || messageTail[0].ServerSeq != 2 {
		t.Fatalf("message tail=%#v err=%v", messageTail, err)
	}
	activity, err := service.ListProjectActivity(context.Background(), project.ID, "")
	if err != nil || len(activity) != 9 || activity[0].EventType != "step.succeeded" || activity[len(activity)-1].EventType != "plan.published" {
		t.Fatalf("activity=%#v err=%v", activity, err)
	}

	events, err := service.ListRunEvents(context.Background(), completed.RunID, 0)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	wantTypes := []string{"plan.published", "run.created", "step.started", "checkpoint.committed", "budget.reserved", "checkpoint.committed", "model.message", "plan.state_changed", "step.succeeded"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for index, event := range events {
		if event.StreamSeq != int64(index+1) || event.EventType != wantTypes[index] {
			t.Fatalf("event[%d] = seq %d type %s", index, event.StreamSeq, event.EventType)
		}
	}
	contextPlan, ok := events[len(events)-1].Payload["context_plan"].(map[string]any)
	if !ok || contextPlan["strategy"] == nil {
		t.Fatalf("step event did not expose deterministic context plan: %#v", events[len(events)-1].Payload)
	}

	verifyUsageLedger(t, databasePath)
	usage, err := service.GetRunUsage(context.Background(), completed.RunID)
	if err != nil || usage.ModelCalls != 1 || usage.InputTokens != 18 || usage.OutputTokens != 9 || usage.CachedTokens != 3 || usage.EstimatedCalls != 0 {
		t.Fatalf("usage=%#v err=%v", usage, err)
	}
	artifacts, err := service.ListRunArtifacts(context.Background(), completed.RunID)
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	accepted, err := service.AcceptTaskOutcome(context.Background(), completed.ID, "result verified", "00000000-0000-7000-8000-000000000110", "accept-outcome-key")
	if err != nil {
		t.Fatalf("accept outcome: %v", err)
	}
	replayed, err := service.AcceptTaskOutcome(context.Background(), completed.ID, "result verified", "00000000-0000-7000-8000-000000000111", "accept-outcome-key")
	if err != nil || replayed.ID != accepted.ID {
		t.Fatalf("acceptance replay=%#v err=%v", replayed, err)
	}
	acceptedTask, err := service.GetTask(context.Background(), completed.ID)
	if err != nil || acceptedTask.AcceptedOutcomeID != accepted.ID {
		t.Fatalf("accepted task=%#v err=%v", acceptedTask, err)
	}
	acceptedEvents, err := service.ListRunEvents(context.Background(), completed.RunID, 9)
	if err != nil || len(acceptedEvents) != 1 || acceptedEvents[0].EventType != "outcome.accepted" || acceptedEvents[0].StreamSeq != 10 {
		t.Fatalf("accepted events=%#v err=%v", acceptedEvents, err)
	}
}

func TestConversationAppendKeepsSequenceAndSnapshotsSelectedContext(t *testing.T) {
	t.Parallel()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Conversation project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	memory, err := service.CreateMemory(context.Background(), project.ID, core.CreateMemoryInput{Kind: "manual", Title: "Release checklist", Content: "Run unit tests before publishing.", Visibility: core.VisibilityPrivate}, "memory-context")
	if err != nil {
		t.Fatal(err)
	}
	asset, err := service.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "checklist.md", Visibility: core.VisibilityPrivate, MediaType: "text/markdown"}, strings.NewReader("# Checklist\n- run tests\n"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Draft a release checklist", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "conversation-first", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.AppendConversationMessage(context.Background(), first.ConversationID, core.CreateTaskInput{Goal: "Now apply the selected checklist", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate, ContextAssetIDs: []string{asset.ID}, ContextMemoryIDs: []string{memory.ID}}, "conversation-second", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.ConversationID != first.ConversationID || second.ID == first.ID {
		t.Fatalf("append task did not preserve conversation: first=%#v second=%#v", first, second)
	}
	messages, err := service.ListMessages(context.Background(), first.ConversationID, 0)
	if err != nil || len(messages) != 2 || messages[0].ServerSeq != 1 || messages[1].ServerSeq != 2 || messages[1].Content != "Now apply the selected checklist" {
		t.Fatalf("conversation messages=%#v err=%v", messages, err)
	}
	run, err := repository.GetRunContext(context.Background(), second.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !containsSnapshotID(snapshot["context_asset_ids"], asset.ID) || !containsSnapshotID(snapshot["context_memory_ids"], memory.ID) {
		t.Fatalf("selected context missing from snapshot: %#v", snapshot)
	}
}

func containsSnapshotID(value any, want string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestConversationDeletionRequiresIdleTaskAndRemovesHistory(t *testing.T) {
	t.Parallel()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Delete thread", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "delete-thread-project")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{Goal: "Queued work", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "delete-thread-task", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteConversation(t.Context(), task.ConversationID, "delete-active"); err == nil {
		t.Fatal("active conversation deletion was accepted")
	} else {
		var conflict *core.ResourceConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("active deletion error=%T %v", err, err)
		}
	}
	renamed, err := service.RenameConversation(t.Context(), task.ConversationID, "新的 Thread 名称")
	if err != nil || renamed.Title != "新的 Thread 名称" {
		t.Fatalf("renamed conversation=%#v err=%v", renamed, err)
	}
	if _, err := service.ControlTask(t.Context(), task.ID, core.ControlTaskInput{Command: "cancel"}, "cancel-before-delete", "cancel-before-delete"); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteConversation(t.Context(), task.ConversationID, "delete-idle"); err != nil {
		t.Fatal(err)
	}
	if conversations, err := service.ListConversations(t.Context(), project.ID); err != nil || len(conversations) != 0 {
		t.Fatalf("conversations=%#v err=%v", conversations, err)
	}
	if _, err := service.GetTask(t.Context(), task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted task error=%v", err)
	}
}

func TestMissingModelCredentialBlocksInsteadOfPretendingSuccess(t *testing.T) {
	t.Parallel()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Work project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Answer safely", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000104", "")
	if err != nil {
		t.Fatal(err)
	}

	model := openaicompat.New("https://provider.invalid/v1", "", func() string { return "" })
	worker := okruntime.NewWorker("test-worker", repository, model, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("worker result worked=%v err=%v", worked, err)
	}
	blocked, err := service.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Status != core.StatusBlocked || blocked.ErrorCode != "MODEL_CREDENTIAL_MISSING" {
		t.Fatalf("task status=%s error=%s, want blocked/MODEL_CREDENTIAL_MISSING", blocked.Status, blocked.ErrorCode)
	}
}

func TestConversationAndActivityDoNotLeakPrivateTaskToProjectMember(t *testing.T) {
	t.Parallel()

	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	ownerService := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Owner"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := ownerService.CreateProject(context.Background(), core.CreateProjectInput{Name: "Visibility project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	privateTask, err := ownerService.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "private existence must stay hidden", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000130", "")
	if err != nil {
		t.Fatal(err)
	}

	memberID := "00000000-0000-7000-8000-000000000131"
	if err := repository.Bootstrap(context.Background(), core.Actor{ID: memberID, DisplayName: "Project member"}, core.Organization{ID: testOrgID, Name: "Test Org"}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)`, project.ID, memberID, now, now); err != nil {
		t.Fatal(err)
	}
	memberService := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: memberID, DisplayName: "Project member"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	if tasks, err := memberService.ListTasks(context.Background(), project.ID); err != nil || len(tasks) != 0 {
		t.Fatalf("private task leaked through task list: tasks=%#v err=%v", tasks, err)
	}
	if conversations, err := memberService.ListConversations(context.Background(), project.ID); err != nil || len(conversations) != 0 {
		t.Fatalf("private conversation leaked: conversations=%#v err=%v", conversations, err)
	}
	if activity, err := memberService.ListProjectActivity(context.Background(), project.ID, ""); err != nil || len(activity) != 0 {
		t.Fatalf("private activity leaked: activity=%#v err=%v", activity, err)
	}
	if planItems, err := memberService.ListPlanItems(context.Background(), project.ID); err != nil || len(planItems) != 0 {
		t.Fatalf("private plan item leaked: plan_items=%#v err=%v", planItems, err)
	}
	if messages, err := memberService.ListMessages(context.Background(), privateTask.ConversationID, 0); err != nil || len(messages) != 0 {
		t.Fatalf("private messages leaked: messages=%#v err=%v", messages, err)
	}

	sharedTask, err := ownerService.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "project-visible message", Mode: core.ModeAnswer, Visibility: core.VisibilityProject}, "00000000-0000-7000-8000-000000000132", "")
	if err != nil {
		t.Fatal(err)
	}
	if tasks, err := memberService.ListTasks(context.Background(), project.ID); err != nil || len(tasks) != 1 || tasks[0].ID != sharedTask.ID {
		t.Fatalf("project task not visible: tasks=%#v err=%v", tasks, err)
	}
	if conversations, err := memberService.ListConversations(context.Background(), project.ID); err != nil || len(conversations) != 1 || conversations[0].ID != sharedTask.ConversationID {
		t.Fatalf("project conversation not visible: conversations=%#v err=%v", conversations, err)
	}
	if activity, err := memberService.ListProjectActivity(context.Background(), project.ID, ""); err != nil || len(activity) != 2 || activity[0].TaskID != sharedTask.ID || activity[1].EventType != "plan.published" {
		t.Fatalf("project activity not visible: activity=%#v err=%v", activity, err)
	}
	if planItems, err := memberService.ListPlanItems(context.Background(), project.ID); err != nil || len(planItems) != 1 || planItems[0].LinkedTaskID != sharedTask.ID {
		t.Fatalf("project plan item not visible: plan_items=%#v err=%v", planItems, err)
	}
	if messages, err := memberService.ListMessages(context.Background(), sharedTask.ConversationID, 0); err != nil || len(messages) != 1 || messages[0].Content != "project-visible message" {
		t.Fatalf("project message not visible: messages=%#v err=%v", messages, err)
	}
}

func TestWorkspaceToolPlanPersistsApprovalReceiptAndValidation(t *testing.T) {
	t.Parallel()

	provider := newToolMockProvider(t)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Tool project", Kind: core.ProjectKindTeam, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := service.AddProjectMember(context.Background(), project.ID, core.AddProjectMemberInput{Email: "approval-member@example.test", DisplayName: "Approval Member", Role: core.ProjectRoleMember}, "approval-member", "approval-member")
	if err != nil {
		t.Fatal(err)
	}
	memberService := application.NewService(repository, core.ProfileTeam, provider.URL+"/v1", core.Actor{ID: member.UserID, DisplayName: member.DisplayName}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Create a checked file and run the requested validation", Acceptance: "The file exists and go version exits zero", Mode: core.ModeSingleAgent, Visibility: core.VisibilityProject}, "00000000-0000-7000-8000-000000000120", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("tool-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)

	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("model planning worked=%v err=%v", worked, err)
	}
	planned, err := service.GetTask(context.Background(), task.ID)
	if err != nil || planned.Status != core.StatusQueued {
		t.Fatalf("planned=%#v err=%v", planned, err)
	}
	approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%#v err=%v", approvals, err)
	}
	approval := approvals[0]
	if approval.ToolName != core.ToolWorkspaceRunCommand || approval.RiskLevel != "L2" || approval.InputHash == "" || approval.FrozenParams["program"] != "go" {
		t.Fatalf("approval does not freeze the requested command: %#v", approval)
	}
	if _, err := memberService.DecideApproval(context.Background(), approval.ID, core.DecideApprovalInput{Decision: core.ApprovalApproved}, "member-cannot-approve", "member-cannot-approve"); err == nil {
		t.Fatal("project member unexpectedly decided an approval")
	} else {
		var forbidden *core.ForbiddenError
		if !errors.As(err, &forbidden) {
			t.Fatalf("member approval error=%T %v", err, err)
		}
	}

	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("write execution worked=%v err=%v", worked, err)
	}
	written, err := os.ReadFile(filepath.Join(filepath.Dir(databasePath), "workspaces", project.ID, "notes", "verified.txt"))
	if err != nil || string(written) != "Oktapod controlled write\n" {
		t.Fatalf("written=%q err=%v", string(written), err)
	}
	waiting, err := service.GetTask(context.Background(), task.ID)
	if err != nil || waiting.Status != core.StatusWaitingApproval {
		t.Fatalf("waiting=%#v err=%v", waiting, err)
	}

	decided, err := service.DecideApproval(context.Background(), approval.ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Note: "run the frozen validation"}, "00000000-0000-7000-8000-000000000121", "approve-command-key")
	if err != nil || decided.Status != core.ApprovalApproved || decided.DecidedAt == nil {
		t.Fatalf("decided=%#v err=%v", decided, err)
	}
	replayed, err := service.DecideApproval(context.Background(), approval.ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Note: "run the frozen validation"}, "00000000-0000-7000-8000-000000000122", "approve-command-key")
	if err != nil || replayed.ID != decided.ID {
		t.Fatalf("approval replay=%#v err=%v", replayed, err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("command execution worked=%v err=%v", worked, err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("model continuation worked=%v err=%v", worked, err)
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded || !strings.Contains(completed.ResultSummary, "notes/verified.txt") {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	artifacts, err := service.ListRunArtifacts(context.Background(), task.RunID)
	if err != nil || len(artifacts) != 1 || artifacts[0].SourceType != "workspace_file" || artifacts[0].Name != "notes/verified.txt" {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	artifact, content, err := service.OpenArtifactContent(context.Background(), artifacts[0].ID)
	if err != nil {
		t.Fatalf("open artifact: %v", err)
	}
	opened, readErr := io.ReadAll(content)
	_ = content.Close()
	if readErr != nil || artifact.Name != "notes/verified.txt" || string(opened) != "Oktapod controlled write\n" {
		t.Fatalf("opened artifact=%#v content=%q err=%v", artifact, string(opened), readErr)
	}

	verifyToolGovernanceLedger(t, databasePath)
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"tool.planned", "approval.requested", "approval.decided", "tool.execution_started", "tool.completed", "validation.completed"} {
		if !slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == required }) {
			t.Fatalf("missing %s in events %#v", required, events)
		}
	}
	history, err := service.ListInboxHistory(context.Background(), 20)
	if err != nil || !slices.ContainsFunc(history, func(item core.InboxHistoryItem) bool {
		return item.Kind == "approval" && item.TaskID == task.ID && strings.Contains(item.Summary, "批准")
	}) {
		t.Fatalf("approval history=%#v err=%v", history, err)
	}
}

func TestProjectApprovalScopeSkipsRepeatedProgramApprovalAndCanBeRevoked(t *testing.T) {
	t.Parallel()

	modelCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-approval-scope-test"}}})
		case "/v1/chat/completions":
			modelCalls++
			w.Header().Set("Content-Type", "application/json")
			if modelCalls%2 == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "provider-command-plan",
					"choices": []map[string]any{{"message": map[string]any{
						"role":    "assistant",
						"content": "Run the validated Go version command.",
						"tool_calls": []map[string]any{
							{"id": "provider-command", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceRunCommand, "arguments": `{"program":"go","args":["version"]}`}},
						},
					}}},
					"usage": map[string]any{"prompt_tokens": 22, "completion_tokens": 8},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "provider-command-final",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "go version 已完成执行。"}}},
				"usage":   map[string]any{"prompt_tokens": 28, "completion_tokens": 9},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Approval Scope User"}, core.Organization{ID: testOrgID, Name: "Approval Scope Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Conversation approval scope", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Validate the workspace Go toolchain", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "approval-scope-task-1", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("approval-scope-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)

	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("first planning worked=%v err=%v", worked, runErr)
	}
	approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("pending approvals=%#v err=%v", approvals, err)
	}
	decided, err := service.DecideApproval(context.Background(), approvals[0].ID, core.DecideApprovalInput{Decision: core.ApprovalApproved, Scope: core.ApprovalScopeProject, Confirmed: true}, "approval-scope-decision", "")
	if err != nil || decided.Status != core.ApprovalApproved {
		t.Fatalf("decided=%#v err=%v", decided, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	var ruleCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM project_approval_rules WHERE project_id = ? AND tool_name = ? AND rule_version = 2 AND match_hash <> '' AND revoked_at = ''`, project.ID, core.ToolWorkspaceRunCommand).Scan(&ruleCount); err != nil {
		_ = db.Close()
		t.Fatalf("query approval rules: %v", err)
	}
	_ = db.Close()
	if ruleCount != 1 {
		t.Fatalf("project approval rule count=%d", ruleCount)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("first execution attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	first, err := service.GetTask(context.Background(), task.ID)
	if err != nil || first.Status != core.StatusSucceeded {
		t.Fatalf("first task=%#v err=%v", first, err)
	}

	second, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Validate Go again in a new conversation", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "approval-scope-task-2", "")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
			t.Fatalf("second execution attempt %d worked=%v err=%v", attempt, worked, runErr)
		}
	}
	secondCompleted, err := service.GetTask(context.Background(), second.ID)
	if err != nil || secondCompleted.Status != core.StatusSucceeded {
		t.Fatalf("second task=%#v err=%v", secondCompleted, err)
	}
	if pending, err := service.ListApprovals(context.Background(), core.ApprovalPending); err != nil || len(pending) != 0 {
		t.Fatalf("project rule left pending approvals=%#v err=%v", pending, err)
	}
	events, err := service.ListRunEvents(context.Background(), second.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(events, func(event core.RunEvent) bool { return strings.HasPrefix(event.EventType, "approval.") }) {
		t.Fatalf("second run unexpectedly requested approval: %#v", events)
	}
	planned := slices.ContainsFunc(events, func(event core.RunEvent) bool {
		return event.EventType == "tool.planned" && event.Payload["policy_decision"] == core.PolicyAllow
	})
	if !planned {
		t.Fatalf("second run did not audit the project-rule match: %#v", events)
	}
	rules, err := service.ListProjectApprovalRules(context.Background(), project.ID)
	if err != nil || len(rules) != 1 || rules[0].RevokedAt != nil {
		t.Fatalf("project rules=%#v err=%v", rules, err)
	}
	if err := service.RevokeProjectApprovalRule(context.Background(), project.ID, rules[0].ID, "approval-rule-revoke"); err != nil {
		t.Fatal(err)
	}
	rules, err = service.ListProjectApprovalRules(context.Background(), project.ID)
	if err != nil || len(rules) != 1 || rules[0].RevokedAt == nil {
		t.Fatalf("revoked project rules=%#v err=%v", rules, err)
	}
	third, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Validate Go after revocation", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "approval-scope-task-3", "")
	if err != nil {
		t.Fatal(err)
	}
	if worked, runErr := worker.RunOnce(context.Background()); runErr != nil || !worked {
		t.Fatalf("third planning worked=%v err=%v", worked, runErr)
	}
	pending, err := service.ListApprovals(context.Background(), core.ApprovalPending)
	if err != nil || !slices.ContainsFunc(pending, func(item core.Approval) bool { return item.TaskID == third.ID }) {
		t.Fatalf("revoked rule did not restore approval requirement: %#v err=%v", pending, err)
	}
}
func TestExpiredApprovalRejectsAndBlocksRun(t *testing.T) {
	t.Parallel()

	provider := newToolMockProvider(t)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Expiry User"}, core.Organization{ID: testOrgID, Name: "Expiry Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Approval expiry", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Create a checked file and run the requested validation", Acceptance: "The file exists and go version exits zero", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "expiry-task", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("expiry-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("planning worked=%v err=%v", worked, err)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("write planning worked=%v err=%v", worked, err)
	}
	approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending)
	if err != nil || len(approvals) != 1 {
		t.Fatalf("approvals=%#v err=%v", approvals, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE approval_requests SET expires_at = '2000-01-01T00:00:00.000Z' WHERE id = ?`, approvals[0].ID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	expired, err := repository.ExpireApprovals(context.Background(), time.Now().UTC())
	if err != nil || expired != 1 {
		t.Fatalf("expired=%d err=%v", expired, err)
	}
	blocked, err := service.GetTask(context.Background(), task.ID)
	if err != nil || blocked.Status != core.StatusBlocked || blocked.ErrorCode != "APPROVAL_EXPIRED" {
		t.Fatalf("blocked task=%#v err=%v", blocked, err)
	}
	all, err := service.ListApprovals(context.Background(), core.ApprovalRejected)
	if err != nil || len(all) != 1 || all[0].ExpiresAt.IsZero() {
		t.Fatalf("expired approval=%#v err=%v", all, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil || !slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == "approval.expired" }) {
		t.Fatalf("expiry events=%#v err=%v", events, err)
	}
}

func TestFullAccessRunSuppressesApprovalWithinExplicitWindow(t *testing.T) {
	t.Parallel()

	provider := newToolMockProvider(t)
	defer provider.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, provider.URL+"/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "fake-test-key" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Full access project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{
		Goal:           "Create and validate a file without an approval prompt",
		Acceptance:     "The file exists and go version exits zero",
		Mode:           core.ModeSingleAgent,
		Visibility:     core.VisibilityPrivate,
		PermissionMode: core.PermissionModeFullAccess,
	}, "00000000-0000-7000-8000-000000000123", "")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := workspace.NewLocal(filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	worker := okruntime.NewWorker("full-access-worker", repository, openaicompat.New(provider.URL+"/v1", "", func() string { return "fake-test-key" }), slog.New(slog.NewTextHandler(os.Stderr, nil))).WithWorkspaceExecutor(executor)

	for attempt := 0; attempt < 6; attempt++ {
		if worked, runErr := worker.RunOnce(context.Background()); runErr != nil {
			t.Fatalf("worker attempt %d: %v", attempt, runErr)
		} else if !worked {
			break
		}
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		t.Fatalf("full access task=%#v err=%v", completed, err)
	}
	if approvals, err := service.ListApprovals(context.Background(), core.ApprovalPending); err != nil || len(approvals) != 0 {
		t.Fatalf("full access unexpectedly created pending approval: %#v err=%v", approvals, err)
	}
	written, err := os.ReadFile(filepath.Join(filepath.Dir(databasePath), "workspaces", project.ID, "notes", "verified.txt"))
	if err != nil || string(written) != "Oktapod controlled write\n" {
		t.Fatalf("full access write=%q err=%v", string(written), err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == "approval.requested" }) {
		t.Fatalf("full access emitted approval event: %#v", events)
	}
	if !slices.ContainsFunc(events, func(event core.RunEvent) bool { return event.EventType == "tool.completed" }) {
		t.Fatalf("full access did not complete tool execution: %#v", events)
	}
}

func TestExpiredLeaseCreatesRecoveryAttemptAndRejectsStaleCommit(t *testing.T) {
	t.Parallel()

	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Recovery project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	task, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "Recover this step", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000105", "")
	if err != nil {
		t.Fatal(err)
	}

	startedAt := time.Now().UTC()
	stale, err := repository.ClaimNextStep(context.Background(), "worker-stale", startedAt, startedAt.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.GetRunContext(context.Background(), stale.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkStepStarted(context.Background(), stale, run, "00000000-0000-7000-8000-000000000106"); err != nil {
		t.Fatal(err)
	}

	recovery, err := repository.ClaimNextStep(context.Background(), "worker-recovery", startedAt.Add(time.Second), startedAt.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !recovery.Recovery || recovery.AttemptNumber != 2 || recovery.FenceToken <= stale.FenceToken {
		t.Fatalf("recovery claim = %#v, stale = %#v", recovery, stale)
	}
	if err := repository.MarkStepStarted(context.Background(), recovery, run, "00000000-0000-7000-8000-000000000107"); err != nil {
		t.Fatal(err)
	}
	outcome := core.StepOutcome{Status: core.StatusSucceeded, ResultSummary: "recovered", Output: map[string]any{"verified": true}}
	if err := repository.FinishStep(context.Background(), stale, run, outcome, "00000000-0000-7000-8000-000000000108"); err == nil || !strings.Contains(err.Error(), "LEASE_LOST") {
		t.Fatalf("stale commit error = %v, want LEASE_LOST", err)
	}
	if err := repository.FinishStep(context.Background(), recovery, run, outcome, "00000000-0000-7000-8000-000000000109"); err != nil {
		t.Fatal(err)
	}
	completed, err := service.GetTask(context.Background(), task.ID)
	if err != nil || completed.Status != core.StatusSucceeded {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
	events, err := service.ListRunEvents(context.Background(), task.RunID, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"plan.published", "run.created", "step.started", "step.recovered", "step.started", "checkpoint.committed", "plan.state_changed", "step.succeeded"}
	if len(events) != len(want) {
		t.Fatalf("event count=%d want=%d: %#v", len(events), len(want), events)
	}
	for index, event := range events {
		if event.EventType != want[index] || event.StreamSeq != int64(index+1) {
			t.Fatalf("event[%d]=%s#%d want=%s#%d", index, event.EventType, event.StreamSeq, want[index], index+1)
		}
	}
	verifyAttempts(t, databasePath)
}

func newRepository(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	repository, err := sqlite.Open(context.Background(), databasePath, filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	return repository, databasePath
}

func bootstrapRepository(t *testing.T, repository *sqlite.Store) {
	t.Helper()
	if err := repository.Bootstrap(context.Background(), core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}); err != nil {
		t.Fatalf("bootstrap repository: %v", err)
	}
}

func newMockProvider(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-test-key" {
			t.Errorf("unexpected authorization header")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-test"}}})
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "provider-request-1",
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "Fencing token prevents a stale worker from committing."}}},
				"usage":   map[string]any{"prompt_tokens": 21, "completion_tokens": 9, "prompt_tokens_details": map[string]any{"cached_tokens": 3}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newToolMockProvider(t *testing.T) *httptest.Server {
	t.Helper()
	modelCalls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-tool-test"}}})
		case "/v1/chat/completions":
			modelCalls++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode tool request: %v", err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			tools, ok := request["tools"].([]any)
			toolNames := make(map[string]bool)
			for _, raw := range tools {
				if item, ok := raw.(map[string]any); ok {
					if function, ok := item["function"].(map[string]any); ok {
						if name, ok := function["name"].(string); ok {
							toolNames[name] = true
						}
					}
				}
			}
			if !ok || !toolNames[core.ToolWorkspaceWriteFile] || !toolNames[core.ToolWorkspaceRunCommand] || !toolNames[core.ToolWebSearch] {
				t.Errorf("required built-in tools not offered to the model: %#v", request["tools"])
				http.Error(w, "missing tools", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if modelCalls > 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":      "provider-tool-request-final",
					"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "已完成文件创建和验证。交付文件：notes/verified.txt"}}},
					"usage":   map[string]any{"prompt_tokens": 40, "completion_tokens": 12},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "provider-tool-request-1",
				"choices": []map[string]any{{"message": map[string]any{
					"role":    "assistant",
					"content": "Created the requested file and validated the workspace.",
					"tool_calls": []map[string]any{
						{"id": "provider-write", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": `{"path":"notes/verified.txt","content":"Oktapod controlled write\n"}`}},
						{"id": "provider-command", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceRunCommand, "arguments": `{"program":"go","args":["version"]}`}},
					},
				}}},
				"usage": map[string]any{"prompt_tokens": 32, "completion_tokens": 18},
			})
		default:
			http.NotFound(w, r)
		}
	}))
}

func verifyUsageLedger(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count, input, output, contextTokens, cacheRead int64
	var stopReason, usageVersion string
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(ul.input_tokens), 0), COALESCE(SUM(ul.output_tokens), 0),
		COALESCE(MAX(mc.context_tokens), 0), COALESCE(MAX(mc.cache_read_tokens), 0),
		COALESCE(MAX(mc.stop_reason), ''), COALESCE(MAX(mc.usage_semantics_version), '')
		FROM usage_ledger ul JOIN model_calls mc ON mc.id = ul.call_id`).
		Scan(&count, &input, &output, &contextTokens, &cacheRead, &stopReason, &usageVersion); err != nil {
		t.Fatalf("query usage ledger: %v", err)
	}
	if count != 1 || input != 18 || output != 9 || contextTokens != 21 || cacheRead != 3 || stopReason != string(core.StopReasonStop) || usageVersion != "v2" {
		t.Fatalf("usage count=%d input=%d output=%d context=%d cache_read=%d stop=%q version=%q", count, input, output, contextTokens, cacheRead, stopReason, usageVersion)
	}
}

func verifyAttempts(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT attempt_number, attempt_kind, status FROM step_attempts ORDER BY attempt_number")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var number int
		var kind, status string
		if err := rows.Scan(&number, &kind, &status); err != nil {
			t.Fatal(err)
		}
		values = append(values, fmt.Sprintf("%d:%s:%s", number, kind, status))
	}
	want := []string{"1:initial:lease_lost", "2:recovery:succeeded"}
	if !slices.Equal(values, want) {
		t.Fatalf("attempts=%v want=%v", values, want)
	}
}

func verifyToolGovernanceLedger(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var toolCalls, receipts, validations, pending int64
	if err := db.QueryRow(`
		SELECT (SELECT COUNT(*) FROM tool_calls WHERE status = 'succeeded'),
		       (SELECT COUNT(*) FROM side_effect_receipts WHERE status = 'succeeded'),
		       (SELECT COUNT(*) FROM validation_results WHERE status = 'passed'),
		       (SELECT COUNT(*) FROM approval_requests WHERE status = 'pending')`).Scan(&toolCalls, &receipts, &validations, &pending); err != nil {
		t.Fatal(err)
	}
	if toolCalls != 2 || receipts != 2 || validations != 2 || pending != 0 {
		t.Fatalf("tool calls=%d receipts=%d validations=%d pending=%d", toolCalls, receipts, validations, pending)
	}
}
