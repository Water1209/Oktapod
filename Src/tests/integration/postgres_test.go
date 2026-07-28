package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	postgresdb "github.com/oktapod/oktapod/Src/internal/adapters/db/postgres"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// Run with OKTAPOD_TEST_POSTGRES_DSN against an isolated PostgreSQL database.
// The test is intentionally opt-in so the default Personal test suite remains
// hermetic and never requires a local database daemon.
func TestPostgresRepositorySmoke(t *testing.T) {
	dsn := os.Getenv("OKTAPOD_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set OKTAPOD_TEST_POSTGRES_DSN to run the PostgreSQL/RLS smoke test")
	}
	ctx := context.Background()
	store, err := postgresdb.Open(ctx, dsn, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000091", DisplayName: "PG Smoke"}
	org := core.Organization{ID: "00000000-0000-7000-8000-000000000092", Name: "PG Smoke Org"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	scoped := application.WithScope(ctx, core.Scope{OrganizationID: org.ID, UserID: actor.ID})
	project, err := store.CreateProject(scoped, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, core.CreateProjectInput{
		Name:  "PostgreSQL smoke",
		Kind:  "team",
		Scene: "code",
	}, core.Idempotency{Key: "pg-smoke-project", RequestHash: "pg-smoke-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListProjects(scoped, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProject(scoped, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, project.ID); err != nil {
		t.Fatal(err)
	}
	usage, err := store.GetProjectUsage(scoped, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, project.ID, time.Now().Add(-24*time.Hour))
	if err != nil || usage.ProjectID != project.ID || usage.ModelCalls != 0 {
		t.Fatalf("empty postgres project usage=%#v err=%v", usage, err)
	}
	ownerService := application.NewService(store, core.ProfileTeam, "https://provider.invalid/v1", actor, org, func() string { return "" })
	memory, err := ownerService.CreateMemory(scoped, project.ID, core.CreateMemoryInput{Kind: "manual", Title: "PG context", Content: "Use the project checklist.", Visibility: core.VisibilityProject, SourceRefsJSON: `["pg-event-1"]`}, "pg-memory-context")
	if err != nil {
		t.Fatal(err)
	}
	updatedMemory, err := ownerService.UpdateMemory(scoped, memory.ID, core.UpdateMemoryInput{Title: "PG context updated", Content: "Use the updated project checklist.", Visibility: core.VisibilityProject}, "pg-memory-update")
	if err != nil || updatedMemory.SourceHash == memory.SourceHash || updatedMemory.Title != "PG context updated" || updatedMemory.SourceRefsJSON != `[]` {
		t.Fatalf("updated postgres memory=%#v err=%v", updatedMemory, err)
	}
	if inactive, err := ownerService.SetMemoryStatus(scoped, memory.ID, core.MemoryStatusInactive, "pg-memory-status-off"); err != nil || inactive.Status != core.MemoryStatusInactive {
		t.Fatalf("inactive postgres memory=%#v err=%v", inactive, err)
	}
	if active, err := ownerService.SetMemoryStatus(scoped, memory.ID, core.MemoryStatusActive, "pg-memory-status-on"); err != nil || active.Status != core.MemoryStatusActive {
		t.Fatalf("active postgres memory=%#v err=%v", active, err)
	}
	asset, err := ownerService.UploadAsset(scoped, project.ID, core.UploadAssetInput{Name: "pg-context.md", Visibility: core.VisibilityProject, MediaType: "text/markdown"}, strings.NewReader("# PG context\n"))
	if err != nil {
		t.Fatal(err)
	}
	firstTask, err := ownerService.CreateTask(scoped, project.ID, core.CreateTaskInput{Goal: "PG first turn", Mode: core.ModeAnswer, Visibility: core.VisibilityProject}, "pg-first-turn", "")
	if err != nil {
		t.Fatal(err)
	}
	if history, err := ownerService.ListInboxHistory(scoped, 20); err != nil || history == nil {
		t.Fatalf("postgres inbox history query=%#v err=%v", history, err)
	}
	otherActor := core.Actor{ID: "00000000-0000-7000-8000-000000000093", DisplayName: "Other Org User"}
	otherOrg := core.Organization{ID: "00000000-0000-7000-8000-000000000094", Name: "Other Org"}
	if err := store.Bootstrap(ctx, otherActor, otherOrg); err != nil {
		t.Fatal(err)
	}
	otherScope := application.WithScope(ctx, core.Scope{OrganizationID: otherOrg.ID, UserID: otherActor.ID})
	otherProject, err := store.CreateProject(otherScope, core.Scope{OrganizationID: otherOrg.ID, UserID: otherActor.ID}, core.CreateProjectInput{Name: "Other project", Kind: core.ProjectKindTeam, Scene: core.SceneWork}, core.Idempotency{Key: "pg-other-project", RequestHash: "pg-other-project-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetProject(scoped, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, otherProject.ID); err == nil {
		t.Fatalf("cross-organization project read should be denied, err=%v", err)
	}
	invitation, err := store.CreateProjectInvitation(scoped, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, project.ID, core.CreateProjectInvitationInput{
		Email:       "pg-invite@example.test",
		DisplayName: "PG Invite",
		Role:        core.ProjectRoleMember,
		ExpiresIn:   3600,
	}, "pg-smoke-invite", core.Idempotency{Key: "pg-smoke-invite", RequestHash: "pg-smoke-invite-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if invitation.InviteToken == "" {
		t.Fatal("expected a one-time invitation token")
	}
	accepted, err := store.AcceptProjectInvitation(context.Background(), invitation.InviteToken, "pg-smoke-accept", core.Idempotency{Key: "pg-smoke-accept", RequestHash: "pg-smoke-accept-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.SessionToken == "" {
		t.Fatal("expected invitation acceptance to issue a transient session")
	}
	session, err := store.ResolveAuthSession(context.Background(), accepted.SessionToken)
	if err != nil || session.OrganizationID != org.ID || session.UserID != accepted.AcceptedUserID {
		t.Fatalf("accepted session=%#v err=%v", session, err)
	}
	memberScope := application.WithScope(ctx, core.Scope{OrganizationID: org.ID, UserID: accepted.AcceptedUserID})
	if _, err := store.GetProject(memberScope, core.Scope{OrganizationID: org.ID, UserID: accepted.AcceptedUserID}, project.ID); err != nil {
		t.Fatalf("accepted member cannot read project under RLS scope: %v", err)
	}
	memberService := application.NewService(store, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: accepted.AcceptedUserID, DisplayName: "PG Invite"}, org, func() string { return "" })
	followup, err := memberService.AppendConversationMessage(memberScope, firstTask.ConversationID, core.CreateTaskInput{Goal: "PG follow-up", Mode: core.ModeAnswer, Visibility: core.VisibilityProject, ContextAssetIDs: []string{asset.ID}, ContextMemoryIDs: []string{memory.ID}}, "pg-follow-up", "")
	if err != nil || followup.ConversationID != firstTask.ConversationID {
		t.Fatalf("member follow-up=%#v err=%v", followup, err)
	}
	if err := ownerService.DeleteMemory(scoped, memory.ID, "pg-memory-delete", "pg-memory-delete"); err != nil {
		t.Fatal(err)
	}
}
