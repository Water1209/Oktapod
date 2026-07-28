package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestHTTPTeamMemberContract(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Owner"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	project := postJSONWithKey[struct {
		ID string `json:"id"`
	}](t, server.URL+"/api/v1/projects", map[string]any{"name": "HTTP Team", "kind": "team", "scene": "work"}, "http-team-project-key")
	initial := getJSON[struct {
		Items []struct {
			DisplayName string `json:"display_name"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/members")
	if len(initial.Items) != 1 || initial.Items[0].DisplayName != "Test User" {
		t.Fatalf("initial members=%#v", initial)
	}
	added := postJSONWithKey[struct {
		UserID string `json:"user_id"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/members", map[string]any{"email": "http-member@example.test", "display_name": "HTTP Member", "role": "member"}, "http-team-member-key")
	if added.UserID == "" {
		t.Fatal("member response has no user id")
	}
	after := getJSON[struct {
		Items []struct {
			DisplayName string `json:"display_name"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/members")
	if len(after.Items) != 2 {
		t.Fatalf("members after add=%#v", after)
	}
	invitation := postJSONWithKey[struct {
		ID          string `json:"id"`
		InviteToken string `json:"invite_token"`
		Status      string `json:"status"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/invitations", map[string]any{"email": "http-invitee@example.test", "display_name": "HTTP Invitee", "role": "approver"}, "http-invitation-create-key")
	if invitation.ID == "" || invitation.InviteToken == "" || invitation.Status != "pending" {
		t.Fatalf("invitation=%#v", invitation)
	}
	invitations := getJSON[struct {
		Items []struct {
			Status      string `json:"status"`
			InviteToken string `json:"invite_token"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/invitations")
	if len(invitations.Items) != 1 || invitations.Items[0].Status != "pending" || invitations.Items[0].InviteToken != "" {
		t.Fatalf("listed invitations=%#v", invitations)
	}
	acceptBody, err := json.Marshal(map[string]any{"token": invitation.InviteToken})
	if err != nil {
		t.Fatal(err)
	}
	acceptRequest, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/invitations/accept", bytes.NewReader(acceptBody))
	if err != nil {
		t.Fatal(err)
	}
	acceptRequest.Header.Set("Content-Type", "application/json")
	acceptRequest.Header.Set("Idempotency-Key", "http-invitation-accept-key")
	acceptResponse, err := http.DefaultClient.Do(acceptRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer acceptResponse.Body.Close()
	if acceptResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(acceptResponse.Body)
		t.Fatalf("accept invitation status=%d body=%s", acceptResponse.StatusCode, body)
	}
	accepted := struct {
		Status         string `json:"status"`
		AcceptedUserID string `json:"accepted_user_id"`
		SessionToken   string `json:"session_token"`
	}{}
	if err := json.NewDecoder(acceptResponse.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Status != "accepted" || accepted.AcceptedUserID == "" || accepted.SessionToken == "" {
		t.Fatalf("accepted invitation=%#v", accepted)
	}
	var hasSessionCookie, hasCSRFCookie bool
	for _, cookie := range acceptResponse.Cookies() {
		switch cookie.Name {
		case "oktapod_session":
			hasSessionCookie = cookie.HttpOnly && cookie.Value == accepted.SessionToken
		case "oktapod_csrf":
			hasCSRFCookie = !cookie.HttpOnly && cookie.Value != ""
		}
	}
	if !hasSessionCookie || !hasCSRFCookie {
		t.Fatalf("invitation acceptance did not issue session/csrf cookies: %#v", acceptResponse.Cookies())
	}
	session, err := repository.ResolveAuthSession(context.Background(), accepted.SessionToken)
	if err != nil || session.UserID != accepted.AcceptedUserID {
		t.Fatalf("accepted session=%#v err=%v", session, err)
	}
}

func TestProjectInvitationLifecycleIsSingleUseAndScopeBound(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	owner := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Owner"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := owner.CreateProject(context.Background(), core.CreateProjectInput{Name: "Invitation lifecycle", Kind: core.ProjectKindTeam, Scene: core.SceneWork}, "invite-project")
	if err != nil {
		t.Fatal(err)
	}
	invitation, err := owner.CreateProjectInvitation(context.Background(), project.ID, core.CreateProjectInvitationInput{Email: "invitee@example.test", DisplayName: "Invitee", Role: core.ProjectRoleMember, ExpiresIn: 3600}, "invite-create", "invite-create-key")
	if err != nil || invitation.Status != core.InvitationStatusPending || invitation.InviteToken == "" {
		t.Fatalf("invitation=%#v err=%v", invitation, err)
	}
	listed, err := owner.ListProjectInvitations(context.Background(), project.ID)
	if err != nil || len(listed) != 1 || listed[0].InviteToken != "" || listed[0].Status != core.InvitationStatusPending {
		t.Fatalf("listed invitations=%#v err=%v", listed, err)
	}
	nonOwner := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: "00000000-0000-7000-8000-000000000099", DisplayName: "Non Owner"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	if _, err := nonOwner.ListProjectInvitations(context.Background(), project.ID); err == nil {
		t.Fatal("non-owner listed project invitations")
	}
	accepted, err := owner.AcceptProjectInvitation(context.Background(), invitation.InviteToken, "invite-accept", "invite-accept-key")
	if err != nil || accepted.Status != core.InvitationStatusAccepted || accepted.AcceptedUserID == "" || accepted.SessionToken == "" {
		t.Fatalf("accepted=%#v err=%v", accepted, err)
	}
	session, err := repository.ResolveAuthSession(context.Background(), accepted.SessionToken)
	if err != nil || session.UserID != accepted.AcceptedUserID {
		t.Fatalf("accepted session=%#v err=%v", session, err)
	}
	replayed, err := owner.AcceptProjectInvitation(context.Background(), invitation.InviteToken, "invite-accept-replay", "invite-accept-replay-key")
	if err != nil || replayed.AcceptedUserID != accepted.AcceptedUserID || replayed.Status != core.InvitationStatusAccepted || replayed.SessionToken != "" {
		t.Fatalf("accepted replay=%#v err=%v", replayed, err)
	}
	member := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: accepted.AcceptedUserID, DisplayName: "Invitee"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	if projects, err := member.ListProjects(context.Background()); err != nil || len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("accepted member projects=%#v err=%v", projects, err)
	}
	second, err := owner.CreateProjectInvitation(context.Background(), project.ID, core.CreateProjectInvitationInput{Email: "revoke@example.test", DisplayName: "Revoke", Role: core.ProjectRoleApprover, ExpiresIn: 3600}, "invite-revoke", "invite-revoke-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RevokeProjectInvitation(context.Background(), second.ID, "invite-revoke-action", "invite-revoke-action-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.AcceptProjectInvitation(context.Background(), second.InviteToken, "invite-revoked-accept", "invite-revoked-accept-key"); err == nil {
		t.Fatal("revoked invitation was accepted")
	}
}

func TestTeamProjectMembershipHistoryPrivacyRevocationAndCrossOrganization(t *testing.T) {
	t.Parallel()

	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	owner := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Owner"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := owner.CreateProject(context.Background(), core.CreateProjectInput{Name: "Team privacy", Kind: core.ProjectKindTeam, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	sharedTask, err := owner.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "existing shared history", Mode: core.ModeAnswer, Visibility: core.VisibilityProject}, "00000000-0000-7000-8000-000000000180", "")
	if err != nil {
		t.Fatal(err)
	}
	privateTask, err := owner.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "private existence", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "00000000-0000-7000-8000-000000000181", "")
	if err != nil {
		t.Fatal(err)
	}
	sharedAsset, err := owner.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "shared.txt", Visibility: core.VisibilityProject, SizeHint: 6}, bytes.NewBufferString("shared"))
	if err != nil {
		t.Fatal(err)
	}
	privateAsset, err := owner.UploadAsset(context.Background(), project.ID, core.UploadAssetInput{Name: "private.txt", Visibility: core.VisibilityPrivate, SizeHint: 7}, bytes.NewBufferString("private"))
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RemoveProjectMember(context.Background(), project.ID, testUserID, "00000000-0000-7000-8000-000000000182", "remove-last-owner-key"); err == nil {
		t.Fatal("last owner removal unexpectedly succeeded")
	} else {
		var conflict *core.ResourceConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("last owner error=%T %v", err, err)
		}
	}
	member, err := owner.AddProjectMember(context.Background(), project.ID, core.AddProjectMemberInput{Email: "member@example.test", DisplayName: "Member", Role: core.ProjectRoleMember}, "00000000-0000-7000-8000-000000000183", "add-team-member-key")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := owner.AddProjectMember(context.Background(), project.ID, core.AddProjectMemberInput{Email: "member@example.test", DisplayName: "Member", Role: core.ProjectRoleMember}, "00000000-0000-7000-8000-000000000184", "add-team-member-key")
	if err != nil || replayed.UserID != member.UserID {
		t.Fatalf("member replay=%#v err=%v", replayed, err)
	}
	members, err := owner.ListProjectMembers(context.Background(), project.ID)
	if err != nil || len(members) != 2 || members[0].Role != core.ProjectRoleOwner || members[1].UserID != member.UserID {
		t.Fatalf("members=%#v err=%v", members, err)
	}
	memberService := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: member.UserID, DisplayName: member.DisplayName}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	if projects, err := memberService.ListProjects(context.Background()); err != nil || len(projects) != 1 || projects[0].ID != project.ID {
		t.Fatalf("member projects=%#v err=%v", projects, err)
	}
	if tasks, err := memberService.ListTasks(context.Background(), project.ID); err != nil || len(tasks) != 1 || tasks[0].ID != sharedTask.ID {
		t.Fatalf("member tasks=%#v err=%v private=%s", tasks, err, privateTask.ID)
	}
	if _, err := memberService.AppendConversationMessage(context.Background(), privateTask.ConversationID, core.CreateTaskInput{Goal: "private follow-up", Mode: core.ModeAnswer, Visibility: core.VisibilityPrivate}, "member-private-followup", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("member appended private conversation err=%v", err)
	}
	sharedFollowup, err := memberService.AppendConversationMessage(context.Background(), sharedTask.ConversationID, core.CreateTaskInput{Goal: "shared follow-up", Mode: core.ModeAnswer, Visibility: core.VisibilityProject}, "member-shared-followup", "")
	if err != nil || sharedFollowup.ConversationID != sharedTask.ConversationID || sharedFollowup.Visibility != core.VisibilityProject {
		t.Fatalf("member shared follow-up=%#v err=%v", sharedFollowup, err)
	}
	assets, err := memberService.ListAssets(context.Background(), project.ID, "", "", "")
	if err != nil || len(assets.Items) != 1 || assets.Items[0].ID != sharedAsset.ID || assets.VisibleBytes != sharedAsset.SizeBytes {
		t.Fatalf("member assets=%#v err=%v", assets, err)
	}
	if _, reader, err := memberService.OpenAssetContent(context.Background(), privateAsset.ID); !errors.Is(err, sql.ErrNoRows) || reader != nil {
		t.Fatalf("private blob leaked: reader=%v err=%v", reader, err)
	}
	if _, err := memberService.AddProjectMember(context.Background(), project.ID, core.AddProjectMemberInput{Email: "forbidden@example.test", DisplayName: "Forbidden", Role: core.ProjectRoleMember}, "00000000-0000-7000-8000-000000000185", "member-cannot-invite-key"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("member invitation err=%v, want opaque not found", err)
	}
	if err := owner.RemoveProjectMember(context.Background(), project.ID, member.UserID, "00000000-0000-7000-8000-000000000186", "remove-team-member-key"); err != nil {
		t.Fatal(err)
	}
	if projects, err := memberService.ListProjects(context.Background()); err != nil || len(projects) != 0 {
		t.Fatalf("revoked member projects=%#v err=%v", projects, err)
	}
	if _, err := memberService.GetProject(context.Background(), project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked project access err=%v", err)
	}
	if _, reader, err := memberService.OpenAssetContent(context.Background(), sharedAsset.ID); !errors.Is(err, sql.ErrNoRows) || reader != nil {
		t.Fatalf("revoked blob access reader=%v err=%v", reader, err)
	}

	otherOrgID := "00000000-0000-7000-8000-000000000187"
	otherUserID := "00000000-0000-7000-8000-000000000188"
	if err := repository.Bootstrap(context.Background(), core.Actor{ID: otherUserID, DisplayName: "Other Org"}, core.Organization{ID: otherOrgID, Name: "Other Org"}); err != nil {
		t.Fatal(err)
	}
	otherOrg := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: otherUserID, DisplayName: "Other Org"}, core.Organization{ID: otherOrgID, Name: "Other Org"}, func() string { return "" })
	if _, err := otherOrg.GetProject(context.Background(), project.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-org project err=%v", err)
	}
	if _, reader, err := otherOrg.OpenAssetContent(context.Background(), sharedAsset.ID); !errors.Is(err, sql.ErrNoRows) || reader != nil {
		t.Fatalf("cross-org blob reader=%v err=%v", reader, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE project_id = ? AND action IN ('project.member_upserted', 'project.member_removed')`, project.ID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}
}
