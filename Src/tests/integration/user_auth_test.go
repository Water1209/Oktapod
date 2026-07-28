package integration_test

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
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

func TestUnifiedLocalUsersHavePrivateProjects(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "Oktapod") })
	server := httptest.NewServer(httpapi.NewHandlerWithUI(service, slog.New(slog.NewTextHandler(io.Discard, nil)), ui))
	defer server.Close()

	first := newSessionClient(t)
	response := doJSON(t, first, http.MethodGet, server.URL+"/api/v1/bootstrap", nil, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated bootstrap status=%d", response.StatusCode)
	}
	_ = response.Body.Close()

	registerUser(t, first, server.URL, "architect.one", "架构师一", randomTestPassword(t))
	response = doJSON(t, first, http.MethodGet, server.URL+"/api/v1/bootstrap", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated bootstrap status=%d", response.StatusCode)
	}
	var bootstrap struct {
		User struct {
			Username string `json:"username"`
		} `json:"user"`
	}
	if err := json.NewDecoder(response.Body).Decode(&bootstrap); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if bootstrap.User.Username != "architect.one" {
		t.Fatalf("bootstrap username=%q", bootstrap.User.Username)
	}

	response = doJSON(t, first, http.MethodPost, server.URL+"/api/v1/projects", map[string]any{"name": "Private one", "kind": "personal", "scene": "code"}, "")
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", response.StatusCode)
	}
	_ = response.Body.Close()

	csrf := cookieValue(first, server.URL, "oktapod_csrf")
	response = doJSON(t, first, http.MethodPost, server.URL+"/api/v1/projects", map[string]any{"name": "Private one", "kind": "personal", "scene": "code"}, csrf)
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("create project status=%d body=%s", response.StatusCode, body)
	}
	var project struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	second := newSessionClient(t)
	registerUser(t, second, server.URL, "architect.two", "架构师二", randomTestPassword(t))
	response = doJSON(t, second, http.MethodGet, server.URL+"/api/v1/projects", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("second user list status=%d", response.StatusCode)
	}
	var projects struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(response.Body).Decode(&projects); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(projects.Items) != 0 {
		t.Fatalf("second user can see first user's projects: %#v", projects.Items)
	}

	response = doJSON(t, second, http.MethodGet, server.URL+"/api/v1/projects/"+project.ID, nil, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("cross-user project status=%d body=%s", response.StatusCode, body)
	}
}

func TestLocalRegistrationPasswordLengthBoundary(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	password := randomTestPassword(t)

	if _, err := service.RegisterLocalUser(t.Context(), core.LocalRegisterInput{Username: "short.password", DisplayName: "Short", Password: password[:7]}); err == nil {
		t.Fatal("seven-character password was accepted")
	}
	if _, err := service.RegisterLocalUser(t.Context(), core.LocalRegisterInput{Username: "valid.password", DisplayName: "Valid", Password: password[:8]}); err != nil {
		t.Fatalf("eight-character password was rejected: %v", err)
	}
}

func newSessionClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func randomTestPassword(t *testing.T) string {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func registerUser(t *testing.T, client *http.Client, baseURL, username, displayName, password string) {
	t.Helper()
	response := doJSON(t, client, http.MethodPost, baseURL+"/api/v1/auth/register", map[string]any{"username": username, "display_name": displayName, "password": password}, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("register status=%d body=%s", response.StatusCode, body)
	}
}

func doJSON(t *testing.T, client *http.Client, method, target string, value any, csrf string) *http.Response {
	t.Helper()
	var body io.Reader
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "application/json")
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "auth-test-idempotency-"+usernameFromValue(value))
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func usernameFromValue(value any) string {
	if values, ok := value.(map[string]any); ok {
		if username, ok := values["username"].(string); ok {
			return username
		}
		if name, ok := values["name"].(string); ok {
			return name
		}
	}
	return "request"
}

func cookieValue(client *http.Client, rawURL, name string) string {
	request, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	for _, cookie := range client.Jar.Cookies(request.URL) {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}
