package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
	"golang.org/x/oauth2"
)

type oauthTestSecretStore struct {
	mu     sync.Mutex
	values map[string]string
}

func (store *oauthTestSecretStore) Get(_ context.Context, ref string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.values[ref]
	if !ok {
		return "", fmt.Errorf("secret not found")
	}
	return value, nil
}

func (store *oauthTestSecretStore) Set(_ context.Context, ref, value string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.values[ref] = value
	return nil
}

func TestOAuthRestoredSessionRefreshesAndPersistsBeforeMCPHandshake(t *testing.T) {
	const refreshedAccess = "fixture-refreshed-access"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" {
				t.Fatalf("refresh request form=%#v err=%v", r.Form, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": refreshedAccess, "refresh_token": "fixture-refresh", "token_type": "Bearer", "expires_in": 3600})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer "+refreshedAccess {
				w.Header().Set("WWW-Authenticate", "Bearer")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeOAuthFixtureMCPResponse(t, w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := &oauth2.Config{ClientID: "fixture-client", Endpoint: oauth2.Endpoint{AuthURL: server.URL + "/authorize", TokenURL: server.URL + "/token"}, RedirectURL: "http://127.0.0.1:8787/api/v1/mcp/oauth/callback"}
	expired := &oauth2.Token{AccessToken: "fixture-expired-access", RefreshToken: "fixture-refresh", TokenType: "Bearer", Expiry: time.Now().Add(-time.Hour)}
	credential, err := encodeOAuthCredential(config, expired)
	if err != nil {
		t.Fatal(err)
	}
	saved := ""
	health, err := ConnectWithOptions(context.Background(), oauthFixtureManifest(server.URL+"/mcp"), credential, SessionOptions{CredentialUpdater: func(_ context.Context, payload string) error {
		saved = payload
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(health.Tools) != 1 || saved == "" {
		t.Fatalf("health=%#v saved=%t", health, saved != "")
	}
	_, token, exists, err := decodeOAuthCredential(saved)
	if err != nil || !exists || token.AccessToken != refreshedAccess {
		t.Fatalf("refreshed OAuth envelope was not persisted: exists=%t err=%v", exists, err)
	}
}

func TestOAuthCoordinatorCompletesAndCancelsBrowserFlows(t *testing.T) {
	server := newOAuthFixtureServer(t)
	defer server.Close()
	store := &oauthTestSecretStore{values: map[string]string{}}
	coordinator, err := NewOAuthCoordinator(context.Background(), store, "http://127.0.0.1:8787/api/v1/mcp/oauth/callback")
	if err != nil {
		t.Fatal(err)
	}
	capability := core.Capability{ID: "019fc000-0000-7000-8000-000000000001", Kind: core.CapabilityKindMCP, ManifestJSON: oauthFixtureManifest(server.URL + "/mcp"), CredentialRef: "mcp-capability/test/oauth"}
	session, err := coordinator.Start(context.Background(), capability)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != core.MCPOAuthStatusWaiting || session.AuthorizationURL == "" {
		t.Fatalf("session=%#v", session)
	}
	authorizationURL, err := url.Parse(session.AuthorizationURL)
	if err != nil || authorizationURL.Query().Get("state") == "" {
		t.Fatalf("authorization URL=%q err=%v", session.AuthorizationURL, err)
	}
	completed, err := coordinator.Complete(context.Background(), authorizationURL.Query().Get("state"), "fixture-code", "")
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != core.MCPOAuthStatusConnected {
		t.Fatalf("completed=%#v", completed)
	}
	stored, err := store.Get(context.Background(), capability.CredentialRef)
	if err != nil || !strings.Contains(stored, mcpOAuthCredentialSchemaV1) {
		t.Fatalf("OAuth envelope missing: err=%v", err)
	}

	second := capability
	second.ID = "019fc000-0000-7000-8000-000000000002"
	second.CredentialRef = "mcp-capability/test/oauth-cancel"
	pending, err := coordinator.Start(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := coordinator.Cancel(context.Background(), pending.ID)
	if err != nil || canceled.Status != core.MCPOAuthStatusCanceled {
		t.Fatalf("canceled=%#v err=%v", canceled, err)
	}
}

func TestOAuthAuthorizationURLPolicyRequiresHTTPSOrLoopback(t *testing.T) {
	for raw, allowed := range map[string]bool{
		"https://accounts.example.com/authorize": true,
		"http://127.0.0.1:9876/authorize":        true,
		"http://localhost:9876/authorize":        true,
		"http://accounts.example.com/authorize":  false,
		"file:///tmp/authorize":                  false,
	} {
		target, _ := url.Parse(raw)
		if authorizationURLAllowed(target) != allowed {
			t.Fatalf("url=%s allowed=%t", raw, allowed)
		}
	}
}

func newOAuthFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource/resource":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": server.URL + "/mcp", "authorization_servers": []string{server.URL}})
		case "/.well-known/oauth-authorization-server":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
				"token_endpoint": server.URL + "/token", "registration_endpoint": server.URL + "/register",
				"code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"offline_access"},
			})
		case "/register":
			var metadata map[string]any
			if err := json.NewDecoder(r.Body).Decode(&metadata); err != nil {
				t.Fatalf("decode registration: %v", err)
			}
			metadata["client_id"] = "fixture-client"
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(metadata)
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code") != "fixture-code" {
				t.Fatalf("token request form=%#v err=%v", r.Form, err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "fixture-access", "refresh_token": "fixture-refresh", "token_type": "Bearer", "expires_in": 3600})
		case "/mcp":
			if r.Header.Get("Authorization") != "Bearer fixture-access" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+server.URL+`/.well-known/oauth-protected-resource/resource"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeOAuthFixtureMCPResponse(t, w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	return server
}

func oauthFixtureManifest(endpoint string) string {
	return `{"schema_version":"oktapod.mcp/v1","source":{"type":"manual"},"transport":{"type":"streamable_http","url":"` + endpoint + `"},"auth":{"type":"oauth"}}`
}

func writeOAuthFixtureMCPResponse(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var request rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Fatalf("decode MCP request: %v", err)
	}
	w.Header().Set("Content-Type", "application/json")
	switch request.Method {
	case "server/discover":
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	case "initialize":
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "oauth-fixture", "version": "1.0.0"}}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"tools": []map[string]string{{"name": "search", "description": "fixture"}}}})
	default:
		t.Fatalf("unexpected MCP method %s", request.Method)
	}
}
