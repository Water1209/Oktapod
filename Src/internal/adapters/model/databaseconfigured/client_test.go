package databaseconfigured

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

type configRepositoryStub struct{ config core.ModelProviderConfig }

func (s *configRepositoryStub) GetModelProviderConfig(context.Context, string) (core.ModelProviderConfig, error) {
	return s.config, nil
}

type credentialStoreStub struct{}

func (credentialStoreStub) Get(context.Context, string) (string, error) {
	return "test-credential", nil
}

func TestClientReloadsDatabaseConfigurationWithoutRestart(t *testing.T) {
	provider := func(model string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/models" {
				http.NotFound(response, request)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"data": []map[string]string{{"id": model}}})
		}))
	}
	first := provider("model-a")
	defer first.Close()
	second := provider("model-b")
	defer second.Close()
	repository := &configRepositoryStub{config: core.ModelProviderConfig{Endpoint: first.URL, DefaultModel: "model-a", SecretRef: "ref-a", UpdatedAt: time.Now().UTC()}}
	client := New(repository, credentialStoreStub{}, "org-1")

	models, err := client.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("first models=%v err=%v", models, err)
	}
	repository.config = core.ModelProviderConfig{Endpoint: second.URL, DefaultModel: "model-b", SecretRef: "ref-b", UpdatedAt: time.Now().UTC().Add(time.Second)}
	models, err = client.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "model-b" {
		t.Fatalf("reloaded models=%v err=%v", models, err)
	}
}

type registryRepositoryStub struct {
	providers map[string]core.ProviderInstance
	fallback  core.ModelProviderConfig
}

func (s *registryRepositoryStub) GetModelProviderConfig(context.Context, string) (core.ModelProviderConfig, error) {
	return s.fallback, nil
}

func (s *registryRepositoryStub) GetProviderInstance(_ context.Context, _, providerID string) (core.ProviderInstance, error) {
	provider, ok := s.providers[providerID]
	if !ok {
		return core.ProviderInstance{}, sql.ErrNoRows
	}
	return provider, nil
}

func TestClientFallsBackToDefaultWhenRequestedProviderWasDeleted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			http.NotFound(response, request)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "model-a" {
			t.Fatalf("model=%v; expected surviving provider default", body["model"])
		}
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id":      "request-fallback",
			"choices": []map[string]any{{"message": map[string]any{"content": "fallback"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
		})
	}))
	defer upstream.Close()
	now := time.Now().UTC()
	repository := &registryRepositoryStub{providers: map[string]core.ProviderInstance{
		"provider-a": {ProviderID: "provider-a", ProtocolFamily: core.ProviderProtocolOpenAICompatible, Endpoint: upstream.URL, CredentialRef: "ref-a", DefaultModelID: "model-a", Status: core.ProviderStatusActive, UpdatedAt: now},
	}}
	client := New(repository, credentialStoreStub{}, "org-1")
	result, err := client.Complete(context.Background(), okruntime.ModelRequest{
		SystemPrompt: "system", UserPrompt: "user",
		RequestedModel: core.ModelRef{ProviderID: "deleted-provider", ModelID: "deleted-model"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestedModel.ProviderID != "deleted-provider" || result.RequestedModel.ModelID != "deleted-model" ||
		result.EffectiveModel.ProviderID != "provider-a" || result.EffectiveModel.ModelID != "model-a" || result.RoutingReason != "fallback" {
		t.Fatalf("result=%+v", result)
	}
}

func (s *registryRepositoryStub) GetDefaultProviderInstance(context.Context, string) (core.ProviderInstance, error) {
	return s.providers["provider-a"], nil
}

func TestClientRoutesRequestedProviderInstanceAndPreservesEffectiveModel(t *testing.T) {
	provider := func(label string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/chat/completions" {
				http.NotFound(response, request)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"id":      "request-" + label,
				"choices": []map[string]any{{"message": map[string]any{"content": label}, "finish_reason": "stop"}},
				"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1},
			})
		}))
	}
	first := provider("first")
	defer first.Close()
	second := provider("second")
	defer second.Close()
	now := time.Now().UTC()
	repository := &registryRepositoryStub{providers: map[string]core.ProviderInstance{
		"provider-a": {ProviderID: "provider-a", ProtocolFamily: core.ProviderProtocolOpenAICompatible, Endpoint: first.URL, CredentialRef: "ref-a", DefaultModelID: "model-a", Status: core.ProviderStatusActive, UpdatedAt: now},
		"provider-b": {ProviderID: "provider-b", ProtocolFamily: core.ProviderProtocolOpenAICompatible, Endpoint: second.URL, CredentialRef: "ref-b", DefaultModelID: "model-b", Status: core.ProviderStatusActive, UpdatedAt: now},
	}}
	client := New(repository, credentialStoreStub{}, "org-1")
	result, err := client.Complete(context.Background(), okruntime.ModelRequest{
		SystemPrompt: "system", UserPrompt: "user",
		RequestedModel: core.ModelRef{ProviderID: "provider-b", ModelID: "model-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "second" || result.RequestedModel.ProviderID != "provider-b" ||
		result.EffectiveModel.ProviderID != "provider-b" || result.EffectiveModel.ModelID != "model-b" {
		t.Fatalf("result=%+v", result)
	}
}
