package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/adapters/secretstore"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type oauthHTTPSecretStore struct {
	mu     sync.Mutex
	values map[string]string
}

func (store *oauthHTTPSecretStore) Get(_ context.Context, ref string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[ref]
	if !ok {
		return "", io.EOF
	}
	return value, nil
}

func (store *oauthHTTPSecretStore) Set(_ context.Context, ref, value string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.values[ref] = value
	return nil
}

func (store *oauthHTTPSecretStore) Delete(_ context.Context, ref string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.values, ref)
	return nil
}

type oauthHTTPCoordinator struct {
	session core.MCPOAuthSession
	state   string
}

func (coordinator *oauthHTTPCoordinator) Start(_ context.Context, capability core.Capability) (core.MCPOAuthSession, error) {
	coordinator.session.CapabilityID = capability.ID
	return coordinator.session, nil
}

func (coordinator *oauthHTTPCoordinator) LookupState(context.Context, string) (core.MCPOAuthSession, error) {
	return coordinator.session, nil
}

func (coordinator *oauthHTTPCoordinator) Complete(context.Context, string, string, string) (core.MCPOAuthSession, error) {
	coordinator.session.Status = core.MCPOAuthStatusConnected
	return coordinator.session, nil
}

func (coordinator *oauthHTTPCoordinator) Status(context.Context, string) (core.MCPOAuthSession, error) {
	return coordinator.session, nil
}

func (coordinator *oauthHTTPCoordinator) Cancel(context.Context, string) (core.MCPOAuthSession, error) {
	coordinator.session.Status = core.MCPOAuthStatusCanceled
	return coordinator.session, nil
}

func TestMCPOAuthHTTPFlowDoesNotExposeCredentialsOrProtocolState(t *testing.T) {
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	vault, err := secretstore.NewVaultKeyManager(context.Background(), repository, testOrgID, filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	recoveryCode, _, err := vault.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Confirm(context.Background(), recoveryCode); err != nil {
		t.Fatal(err)
	}
	secrets := &oauthHTTPSecretStore{values: map[string]string{}}
	now := time.Now().UTC()
	coordinator := &oauthHTTPCoordinator{state: "fixture-state-1234567890", session: core.MCPOAuthSession{
		ID: "019fc000-0000-7000-8000-000000000010", Status: core.MCPOAuthStatusWaiting,
		AuthorizationURL: "https://accounts.example.test/authorize?state=fixture-state-1234567890",
		ExpiresAt:        now.Add(10 * time.Minute), UpdatedAt: now,
	}}
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).
		WithProviderRegistry(secrets, nil).
		WithVault(vault).
		WithMCPOAuthCoordinator(coordinator)
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "OAuth MCP", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "oauth-mcp-project")
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{
		Kind: core.CapabilityKindMCP, Name: "oauth-fixture", Version: "1.0.0",
		ManifestJSON: `{"schema_version":"oktapod.mcp/v1","source":{"type":"manual"},"transport":{"type":"streamable_http","url":"https://mcp.example.test/mcp"},"auth":{"type":"oauth"}}`,
	}, "oauth-mcp-capability")
	if err != nil {
		t.Fatal(err)
	}
	if capability.CredentialRef == "" || capability.CredentialExists {
		t.Fatalf("OAuth capability must reserve an opaque SecretRef without inventing a credential: %#v", capability)
	}
	if _, err := service.ReviewCapability(context.Background(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "fixture reviewed"}, "oauth-review", "oauth-review"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	started := postJSON[core.MCPOAuthSession](t, server.URL+"/api/v1/capabilities/"+capability.ID+"/mcp/oauth/start", map[string]any{})
	if started.Status != core.MCPOAuthStatusWaiting || started.ID == "" || started.CapabilityID != capability.ID {
		t.Fatalf("started=%#v", started)
	}
	status := getJSON[core.MCPOAuthSession](t, server.URL+"/api/v1/capabilities/"+capability.ID+"/mcp/oauth/"+started.ID)
	if status.Status != core.MCPOAuthStatusWaiting {
		t.Fatalf("status=%#v", status)
	}
	canceled := postJSON[core.MCPOAuthSession](t, server.URL+"/api/v1/capabilities/"+capability.ID+"/mcp/oauth/"+started.ID+"/cancel", map[string]any{})
	if canceled.Status != core.MCPOAuthStatusCanceled {
		t.Fatalf("canceled=%#v", canceled)
	}
	coordinator.session.Status = core.MCPOAuthStatusWaiting
	response, err := http.Get(server.URL + "/api/v1/mcp/oauth/callback?state=" + coordinator.state + "&error=access_denied&error_description=fixture-sensitive-description")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "授权已取消") || strings.Contains(string(body), "fixture-sensitive-description") || strings.Contains(string(body), coordinator.state) {
		t.Fatalf("callback status=%d body=%s", response.StatusCode, body)
	}
}
