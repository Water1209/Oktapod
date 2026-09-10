package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTeamAdminCanIssueRevocableMemberSession(t *testing.T) {
	adminToken := randomTestPassword(t)
	t.Setenv("OKTAPOD_TEAM_ADMIN_TOKEN", adminToken)
	repository, _ := newRepository(t)
	defer repository.Close()
	if err := repository.Bootstrap(t.Context(), core.Actor{ID: testUserID, DisplayName: "Team Admin"}, core.Organization{ID: testOrgID, Name: "Team Org"}); err != nil {
		t.Fatal(err)
	}
	admin := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Team Admin"}, core.Organization{ID: testOrgID, Name: "Team Org"}, func() string { return "" })
	project, err := admin.CreateProject(context.Background(), core.CreateProjectInput{Name: "Team session project", Kind: core.ProjectKindTeam, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}
	member, err := admin.AddProjectMember(context.Background(), project.ID, core.AddProjectMemberInput{Email: "member@example.com", DisplayName: "Member", Role: core.ProjectRoleMember}, "team-session-member", "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := repository.CreateAuthSession(context.Background(), testOrgID, core.CreateAuthSessionInput{UserID: member.UserID, ExpiresInSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.ResolveAuthSession(context.Background(), session.Token)
	if err != nil || resolved.UserID != member.UserID {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if err := repository.RevokeAuthSession(context.Background(), session.ID, testOrgID, session.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ResolveAuthSession(context.Background(), session.Token); err == nil {
		t.Fatal("revoked session resolved")
	}

	requestBody := map[string]any{"user_id": member.UserID, "expires_in_seconds": 600}
	payload, _ := json.Marshal(requestBody)
	server := httptest.NewServer(httpapi.NewHandler(admin, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/sessions", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+adminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create session status=%d", response.StatusCode)
	}
}

func TestTeamProfileRequiresConfiguredBearerToken(t *testing.T) {
	adminToken := randomTestPassword(t)
	t.Setenv("OKTAPOD_TEAM_ADMIN_TOKEN", adminToken)
	repository, _ := newRepository(t)
	defer repository.Close()
	if err := repository.Bootstrap(t.Context(), core.Actor{ID: testUserID, DisplayName: "Team Admin"}, core.Organization{ID: testOrgID, Name: "Team Org"}); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Team Admin"}, core.Organization{ID: testOrgID, Name: "Team Org"}, func() string { return "" })
	server := httptest.NewServer(httpapi.NewHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	withoutToken, err := http.Get(server.URL + "/api/v1/bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	withoutToken.Body.Close()
	if withoutToken.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without token status=%d", withoutToken.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+adminToken)
	withToken, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	withToken.Body.Close()
	if withToken.StatusCode != http.StatusOK {
		t.Fatalf("with token status=%d", withToken.StatusCode)
	}
}

func TestPersonalHTTPRequiresPrincipalInsteadOfDefaultActorFallback(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Local User"}, core.Organization{ID: testOrgID, Name: "Personal Org"}, func() string { return "" })
	server := httptest.NewServer(httpapi.NewHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()

	response, err := http.Get(server.URL + "/api/v1/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated personal request status=%d", response.StatusCode)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "AUTH_REQUIRED" {
		t.Fatalf("problem=%#v", problem)
	}
}

func TestLocalPasswordLoginUsesSessionCookieAndCSRF(t *testing.T) {
	t.Setenv("OKTAPOD_TEAM_ADMIN_TOKEN", randomTestPassword(t))
	password := randomTestPassword(t)
	repository, _ := newRepository(t)
	defer repository.Close()
	if err := repository.Bootstrap(t.Context(), core.Actor{ID: testUserID, DisplayName: "Team Admin"}, core.Organization{ID: testOrgID, Name: "Team Org"}); err != nil {
		t.Fatal(err)
	}
	if err := repository.ConfigureLocalPassword(t.Context(), testOrgID, testUserID, password); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(repository, core.ProfileTeam, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Team Admin"}, core.Organization{ID: testOrgID, Name: "Team Org"}, func() string { return "" })
	server := httptest.NewServer(httpapi.NewHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	client := &http.Client{}
	body, _ := json.Marshal(map[string]string{"username": testUserID, "password": randomTestPassword(t)})
	bad, err := client.Post(server.URL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d", bad.StatusCode)
	}
	body, _ = json.Marshal(map[string]string{"username": testUserID, "password": password})
	loginRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login", bytes.NewReader(body))
	loginRequest.Header.Set("Content-Type", "application/json")
	login, err := client.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusOK || len(login.Cookies()) != 2 {
		t.Fatalf("login status=%d cookies=%#v", login.StatusCode, login.Cookies())
	}
	// Use a tiny explicit cookie jar so the test also verifies browser-like
	// cookie propagation without relying on bearer headers.
	jarClient := &http.Client{Jar: mustCookieJar(t)}
	loginRequest, _ = http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/login", bytes.NewReader(body))
	loginRequest.Header.Set("Content-Type", "application/json")
	login, err = jarClient.Do(loginRequest)
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	health, err := jarClient.Get(server.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("cookie health status=%d", health.StatusCode)
	}
	logoutRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/auth/logout", nil)
	for _, cookie := range jarClient.Jar.Cookies(loginRequest.URL) {
		if cookie.Name == "oktapod_csrf" {
			logoutRequest.Header.Set("X-CSRF-Token", cookie.Value)
		}
	}
	logout, err := jarClient.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	logout.Body.Close()
	if logout.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status=%d", logout.StatusCode)
	}
}

func mustCookieJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}
