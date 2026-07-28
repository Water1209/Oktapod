package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type httpProviderSecrets struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *httpProviderSecrets) Get(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[ref]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}

func (s *httpProviderSecrets) Set(_ context.Context, ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[ref] = value
	return nil
}

func (s *httpProviderSecrets) Delete(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, ref)
	return nil
}

func TestHTTPProviderRegistryNeverEchoesCredentialAndListsModels(t *testing.T) {
	providerServer := newMockProvider(t)
	defer providerServer.Close()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	secrets := &httpProviderSecrets{values: map[string]string{}}
	service := application.NewService(repository, core.ProfilePersonal, providerServer.URL+"/v1",
		core.Actor{ID: testUserID, DisplayName: "Test User"},
		core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).
		WithProviderRegistry(secrets, func(provider core.ProviderInstance, credential string) application.ModelCatalog {
			return openaicompat.New(provider.Endpoint, provider.DefaultModelID, func() string { return credential })
		})
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()

	const credential = "fake-test-key"
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
}
