package integration_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const validBranchSummary = `## Branch Intent
Explore an alternative response.

## Completed
- The alternate branch was created.

## Key Decisions
- **Branch, do not rewrite**: Preserve immutable history.

## Open Items
- (none)

## Handoff Context
- The active leaf uses entry_seq.`

func TestPortableTranscriptImportRebuildsPrivateSessionEntriesIdempotently(t *testing.T) {
	service, _, closeStore := newProjectTurnService(t, &projectTurnModelStub{})
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Imported project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	input := core.ImportTranscriptInput{
		SourceFormat: "pi-jsonl-preview-v1",
		Conversations: []core.PortableTranscriptConversation{{
			Title: "Imported Thread",
			Messages: []core.PortableTranscriptMessage{
				{Role: "user", Content: "imported question", CreatedAt: time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)},
				{Role: "assistant", Content: "imported answer", CreatedAt: time.Date(2026, 7, 1, 10, 0, 1, 0, time.UTC)},
			},
		}},
	}
	result, err := service.ImportTranscript(t.Context(), project.ID, input, "portable-import")
	if err != nil || result.MessageCount != 2 || len(result.ConversationIDs) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	replayed, err := service.ImportTranscript(t.Context(), project.ID, input, "portable-import")
	if err != nil || replayed.MessageCount != result.MessageCount || replayed.ConversationIDs[0] != result.ConversationIDs[0] {
		t.Fatalf("replay=%#v err=%v", replayed, err)
	}
	conversations, err := service.ListConversations(t.Context(), project.ID)
	if err != nil || len(conversations) != 1 || conversations[0].Visibility != core.VisibilityPrivate {
		t.Fatalf("conversations=%#v err=%v", conversations, err)
	}
	tree, err := service.ListSessionEntries(t.Context(), result.ConversationIDs[0], 0, 100)
	if err != nil || len(tree.Entries) != 2 || !strings.Contains(tree.Entries[0].PayloadJSON, "imported question") {
		t.Fatalf("tree=%#v err=%v", tree, err)
	}
}

func TestSessionTreeNavigateBranchForkCloneLabelAndSummary(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{
		turnModelResult("tree-answer-1", "original answer"),
		turnModelResult("tree-answer-2", "branched answer"),
	}}
	service, _, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Tree project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "original question", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "tree-turn-1")
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListSessionEntries(t.Context(), first.Conversation.ID, 0, 1)
	if err != nil || len(page.Entries) != 1 || page.NextEntrySeq == 0 {
		t.Fatalf("first page=%#v err=%v", page, err)
	}
	full, err := service.ListSessionEntries(t.Context(), first.Conversation.ID, 0, 100)
	if err != nil || len(full.Entries) != 2 || full.Entries[0].EntrySeq != 1 || full.Entries[1].EntrySeq != 2 || full.Entries[0].RefID == "" {
		t.Fatalf("full tree=%#v err=%v", full, err)
	}
	if !strings.Contains(full.Entries[0].PayloadJSON, `"role":"user"`) || !strings.Contains(full.Entries[0].PayloadJSON, `"content":"original question"`) {
		t.Fatalf("message preview payload=%s", full.Entries[0].PayloadJSON)
	}
	originalUser := full.Entries[0]
	originalAssistant := full.Entries[1]
	navigated, err := service.NavigateSession(t.Context(), first.Conversation.ID, core.NavigateSessionInput{EntryID: originalUser.ID, ExpectedLeafEntryID: full.ActiveLeafEntryID})
	if err != nil || navigated.ComposerText != "original question" || navigated.Conversation.ActiveLeafEntryID != originalUser.ID {
		t.Fatalf("navigate=%#v err=%v", navigated, err)
	}
	if _, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{ConversationID: first.Conversation.ID, Content: "alternate question", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "tree-turn-2"); err != nil {
		t.Fatal(err)
	}
	if len(model.prompts) != 2 || strings.Contains(model.prompts[1], "original answer") || !strings.Contains(model.prompts[1], "original question") {
		t.Fatalf("branched model transcript=%#v", model.prompts)
	}
	branched, err := service.ListSessionEntries(t.Context(), first.Conversation.ID, 0, 100)
	if err != nil || len(branched.Entries) != 4 {
		t.Fatalf("branched tree=%#v err=%v", branched, err)
	}
	if branched.Entries[2].ParentEntryID != originalUser.ID {
		t.Fatalf("new branch parent=%s want=%s", branched.Entries[2].ParentEntryID, originalUser.ID)
	}
	path, err := service.GetSessionEntryPath(t.Context(), branched.ActiveLeafEntryID)
	if err != nil || len(path) != 3 || path[0].ID != originalUser.ID || path[1].ID != branched.Entries[2].ID || path[2].ID != branched.Entries[3].ID {
		t.Fatalf("active path=%#v err=%v", path, err)
	}
	forked, err := service.ForkSession(t.Context(), first.Conversation.ID, core.ForkSessionInput{EntryID: originalAssistant.ID})
	if err != nil || forked.ID == first.Conversation.ID || forked.ParentSessionID != first.Conversation.ID || forked.ForkedFromEntryID != originalAssistant.ID {
		t.Fatalf("forked=%#v err=%v", forked, err)
	}
	forkPage, err := service.ListSessionEntries(t.Context(), forked.ID, 0, 100)
	if err != nil || len(forkPage.Entries) != 2 || forkPage.Entries[0].RefID == originalUser.RefID {
		t.Fatalf("fork tree=%#v err=%v", forkPage, err)
	}
	cloned, err := service.CloneSession(t.Context(), first.Conversation.ID, "")
	if err != nil || cloned.ParentSessionID != first.Conversation.ID || cloned.ForkedFromEntryID != branched.ActiveLeafEntryID {
		t.Fatalf("cloned=%#v err=%v", cloned, err)
	}
	labelEntry, err := service.LabelSessionEntry(t.Context(), first.Conversation.ID, branched.ActiveLeafEntryID, core.LabelSessionEntryInput{Label: "working branch", ExpectedLeafEntryID: branched.ActiveLeafEntryID})
	if err != nil || labelEntry.Namespace != "system/label" || labelEntry.ContextInjection != "exclude" {
		t.Fatalf("label entry=%#v err=%v", labelEntry, err)
	}
	summaryEntry, err := service.AppendBranchSummary(t.Context(), first.Conversation.ID, core.BranchSummaryInput{Summary: validBranchSummary, ExpectedLeafEntryID: labelEntry.ID})
	if err != nil || summaryEntry.Kind != core.SessionEntryBranchSummary || summaryEntry.Summary != validBranchSummary {
		t.Fatalf("summary entry=%#v err=%v", summaryEntry, err)
	}
	after, err := service.ListSessionEntries(t.Context(), first.Conversation.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	targetLabeled := false
	for _, entry := range after.Entries {
		targetLabeled = targetLabeled || (entry.ID == branched.ActiveLeafEntryID && entry.Label == "working branch")
	}
	if !targetLabeled {
		t.Fatalf("target label not persisted: %#v", after.Entries)
	}
}

func TestSessionTreeBusyGuardCASAndCycleDetection(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{turnModelResult("tree-guard-answer", "answer")}}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Tree guard", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "guard question", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "tree-guard-turn")
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListSessionEntries(t.Context(), turn.Conversation.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NavigateSession(t.Context(), turn.Conversation.ID, core.NavigateSessionInput{EntryID: page.Entries[0].ID, ExpectedLeafEntryID: "stale-leaf"}); err == nil {
		t.Fatal("stale leaf CAS unexpectedly succeeded")
	} else {
		var conflict *core.StateConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("stale leaf error=%T %v", err, err)
		}
	}
	if _, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{ConversationID: turn.Conversation.ID, Goal: "active work", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "tree-busy-task", ""); err != nil {
		t.Fatal(err)
	}
	current, err := service.ListSessionEntries(t.Context(), turn.Conversation.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NavigateSession(t.Context(), turn.Conversation.ID, core.NavigateSessionInput{EntryID: page.Entries[0].ID, ExpectedLeafEntryID: current.ActiveLeafEntryID}); err == nil {
		t.Fatal("busy guard unexpectedly allowed navigation")
	} else {
		var conflict *core.StateConflictError
		if !errors.As(err, &conflict) || conflict.Status != "run_active" {
			t.Fatalf("busy guard error=%T %v", err, err)
		}
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE session_entries SET parent_entry_id = ? WHERE id = ?`, page.Entries[1].ID, page.Entries[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetSessionEntryPath(t.Context(), page.Entries[1].ID); err == nil || !strings.Contains(err.Error(), "SESSION_TREE_CYCLE") {
		t.Fatalf("cycle detection error=%v", err)
	}
}

func TestBranchSummaryGenerationUsesFiveSectionModelContract(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{
		turnModelResult("summary-answer", "answer"),
		turnModelResult("branch-summary-call", validBranchSummary),
	}}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	defer closeStore()
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Summary project", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "summarize this branch", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "summary-turn")
	if err != nil {
		t.Fatal(err)
	}
	page, err := service.ListSessionEntries(t.Context(), turn.Conversation.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := service.AppendBranchSummary(t.Context(), turn.Conversation.ID, core.BranchSummaryInput{ExpectedLeafEntryID: page.ActiveLeafEntryID})
	if err != nil || entry.Summary != validBranchSummary {
		t.Fatalf("generated summary=%#v err=%v", entry, err)
	}
	if len(model.prompts) != 2 || !strings.Contains(model.prompts[1], "## Branch Intent") || strings.Contains(model.prompts[1], "## Goal") {
		t.Fatalf("branch summary prompt=%#v", model.prompts)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var recorded int
	if err := db.QueryRow(`SELECT COUNT(*) FROM conversation_model_calls WHERE conversation_id = ? AND purpose = 'branch_summary' AND through_entry_id = ?`, turn.Conversation.ID, entry.ID).Scan(&recorded); err != nil || recorded != 1 {
		t.Fatalf("branch summary model call=%d err=%v", recorded, err)
	}
}

func TestSessionTreeActiveLeafAndPathRecoverAfterRestart(t *testing.T) {
	model := &projectTurnModelStub{results: []core.ModelResult{turnModelResult("restart-tree-answer", "persisted answer")}}
	service, databasePath, closeStore := newProjectTurnService(t, model)
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Restart tree", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	turn, err := service.SubmitProjectTurn(t.Context(), project.ID, core.ProjectTurnInput{Content: "persist this path", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "", "restart-tree-turn")
	if err != nil {
		t.Fatal(err)
	}
	before, err := service.ListSessionEntries(t.Context(), turn.Conversation.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	closeStore()
	repository, err := sqlite.Open(t.Context(), databasePath, filepath.Join(filepath.Dir(databasePath), "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	restarted := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	after, err := restarted.ListSessionEntries(t.Context(), turn.Conversation.ID, 0, 100)
	if err != nil || after.ActiveLeafEntryID != before.ActiveLeafEntryID || len(after.Entries) != len(before.Entries) {
		t.Fatalf("restarted tree=%#v before=%#v err=%v", after, before, err)
	}
	path, err := restarted.GetSessionEntryPath(t.Context(), after.ActiveLeafEntryID)
	if err != nil || len(path) != 2 || path[1].ID != after.ActiveLeafEntryID {
		t.Fatalf("restarted path=%#v err=%v", path, err)
	}
}
