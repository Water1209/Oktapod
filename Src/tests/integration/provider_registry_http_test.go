package integration_test

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/adapters/secretstore"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestHTTPProviderRegistryNeverEchoesCredentialAndListsModels(t *testing.T) {
	providerServer := newMockProvider(t)
	defer providerServer.Close()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	vault, err := secretstore.NewVaultKeyManager(t.Context(), repository, testOrgID, filepath.Dir(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	recoveryCode, _, err := vault.Initialize(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Confirm(t.Context(), recoveryCode); err != nil {
		t.Fatal(err)
	}
	secrets := secretstore.NewDatabase(repository, vault, testOrgID)
	service := application.NewService(repository, core.ProfilePersonal, providerServer.URL+"/v1",
		core.Actor{ID: testUserID, DisplayName: "Test User"},
		core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).
		WithVault(vault).
		WithProviderRegistry(secrets, func(provider core.ProviderInstance, credential string) application.ModelCatalog {
			return openaicompat.New(provider.Endpoint, provider.DefaultModelID, func() string { return credential })
		})
	var logs bytes.Buffer
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewJSONHandler(&logs, nil))))
	defer server.Close()

	const credential = "OKTAPOD-PLAINTEXT-SENTINEL-http-log-9f6fe990"
	created := postJSON[map[string]any](t, server.URL+"/api/v1/providers", map[string]any{
		"display_name": "Mock", "protocol_family": "openai_compatible",
		"endpoint": providerServer.URL + "/v1", "default_model_id": "mock-model",
		"credential": credential,
	})
	if created["credential_configured"] != true || created["credential"] != nil {
		t.Fatalf("provider response leaked or misreported credential: %#v", created)
	}
	providerID, _ := created["provider_id"].(string)
	catalog := getJSON[map[string]any](t, server.URL+"/api/v1/providers/"+providerID+"/models")
	items, _ := catalog["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("provider catalog=%#v", catalog)
	}
	response, err := http.Get(server.URL + "/api/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) == "" {
		t.Fatalf("list providers status=%d body=%s", response.StatusCode, body)
	}
	if strings.Contains(string(body), credential) {
		t.Fatalf("credential leaked in list response: %s", body)
	}
	if strings.Contains(logs.String(), credential) {
		t.Fatalf("credential leaked in HTTP logs: %s", logs.String())
	}
}
