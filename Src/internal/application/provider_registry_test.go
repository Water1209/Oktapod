package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type memoryProviderSecrets struct {
	mu     sync.Mutex
	values map[string]string
}

func (s *memoryProviderSecrets) Get(_ context.Context, ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[ref]
	if !ok {
		return "", errors.New("secret not found")
	}
	return value, nil
}

func (s *memoryProviderSecrets) Set(_ context.Context, ref, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[ref] = value
	return nil
}

func (s *memoryProviderSecrets) Delete(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, ref)
	return nil
}

type failingProviderRepository struct {
	application.Repository
}

func (f failingProviderRepository) UpsertProviderInstance(context.Context, string, core.UpsertProviderInstanceInput) (core.ProviderInstance, error) {
	return core.ProviderInstance{}, errors.New("injected provider database failure")
}

type failingCatalog struct {
	err error
}

type staticCatalog struct {
	models []core.ModelDescriptor
}

func (s staticCatalog) ListModels(context.Context) ([]core.ModelDescriptor, error) {
	return s.models, nil
}

func (f failingCatalog) ListModels(context.Context) ([]core.ModelDescriptor, error) {
	return nil, f.err
}

func TestSaveProviderRestoresCredentialWhenDatabaseWriteFails(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	secrets := &memoryProviderSecrets{values: map[string]string{}}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithProviderRegistry(secrets, nil)
	old := "old-secret"
	created, err := service.SaveProvider(t.Context(), "", application.SaveProviderInput{
		DisplayName: "Primary", ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint: "https://one.example.test/v1", DefaultModelID: "model-a", Credential: &old,
	})
	if err != nil {
		t.Fatal(err)
	}
	failing := application.NewService(failingProviderRepository{Repository: store}, core.ProfilePersonal, created.Endpoint, actor, org, func() string { return "" }).
		WithProviderRegistry(secrets, nil)
	replacement := "new-secret"
	_, err = failing.SaveProvider(t.Context(), created.ProviderID, application.SaveProviderInput{
		DisplayName: created.DisplayName, ProtocolFamily: created.ProtocolFamily,
		Endpoint: created.Endpoint, DefaultModelID: created.DefaultModelID,
		IsDefault: true, Credential: &replacement,
	})
	if err == nil {
		t.Fatal("expected injected database failure")
	}
	persisted, getErr := secrets.Get(t.Context(), storeProviderRef(t, store, org.ID, created.ProviderID))
	if getErr != nil || persisted != old {
		t.Fatalf("credential=%q err=%v; expected rollback to old secret", persisted, getErr)
	}
}

func TestListProviderModelsReturnsExpiredCatalogWhileOffline(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	secrets := &memoryProviderSecrets{values: map[string]string{}}
	offlineSecret := "credential-value-that-must-not-leak"
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithProviderRegistry(secrets, func(core.ProviderInstance, string) application.ModelCatalog {
			return failingCatalog{err: errors.New("upstream included " + offlineSecret)}
		})
	provider, err := service.SaveProvider(t.Context(), "", application.SaveProviderInput{
		DisplayName: "Offline", ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint: "https://offline.example.test/v1", DefaultModelID: "cached-model", Credential: &offlineSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.ReplaceModelCatalog(t.Context(), org.ID, provider.ProviderID, []core.ModelCatalogEntry{{
		ProviderID: provider.ProviderID, ModelID: "cached-model",
		CapabilitiesJSON: "{}", FetchedAt: now.Add(-time.Hour),
		ExpiresAt: now.Add(-time.Minute), CatalogVersion: "cached-v1",
	}}); err != nil {
		t.Fatal(err)
	}
	models, err := service.ListProviderModels(t.Context(), provider.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || !models[0].Stale || models[0].ModelID != "cached-model" {
		t.Fatalf("models=%+v", models)
	}
	result, err := service.TestProvider(t.Context(), provider.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if result.RedactedError == "" || strings.Contains(result.RedactedError, offlineSecret) {
		t.Fatalf("provider error was not safely redacted: %q", result.RedactedError)
	}
}

func TestProviderCatalogPreservesUnknownToolCallingCapability(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	secrets := &memoryProviderSecrets{values: map[string]string{}}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithProviderRegistry(secrets, func(core.ProviderInstance, string) application.ModelCatalog {
			return staticCatalog{models: []core.ModelDescriptor{
				{ID: "unknown-tool-support"},
				{ID: "known-no-tools", ToolCallingKnown: true, ToolCalling: false},
			}}
		})
	credential := "provider-secret"
	provider, err := service.SaveProvider(t.Context(), "", application.SaveProviderInput{
		DisplayName: "Catalog", ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint: "https://catalog.example.test/v1", DefaultModelID: "unknown-tool-support", Credential: &credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	models, err := service.RefreshProviderModels(t.Context(), provider.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models=%+v", models)
	}
	knownByID := map[string]bool{}
	for _, model := range models {
		var capabilities map[string]bool
		if err := json.Unmarshal([]byte(model.CapabilitiesJSON), &capabilities); err != nil {
			t.Fatal(err)
		}
		knownByID[model.ModelID] = capabilities["tool_calling_known"]
	}
	if knownByID["unknown-tool-support"] || !knownByID["known-no-tools"] {
		t.Fatalf("tool calling knowledge was not preserved: %+v", knownByID)
	}
}

func TestEnabledProviderModelsAreTheProjectSelectionBoundary(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	secrets := &memoryProviderSecrets{values: map[string]string{}}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithProviderRegistry(secrets, func(core.ProviderInstance, string) application.ModelCatalog {
			return staticCatalog{models: []core.ModelDescriptor{{ID: "enabled-model"}, {ID: "catalog-only-model"}}}
		})
	credential := "provider-secret"
	provider, err := service.SaveProvider(t.Context(), "", application.SaveProviderInput{
		DisplayName: "Boundary", ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint: "https://boundary.example.test/v1", DefaultModelID: "enabled-model", Credential: &credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.RefreshProviderModels(t.Context(), provider.ProviderID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveProviderModels(t.Context(), provider.ProviderID, application.SaveProviderModelsInput{ModelIDs: []string{"enabled-model"}, DefaultModelID: "enabled-model", Activate: true}); err != nil {
		t.Fatal(err)
	}
	_, err = service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Blocked", Kind: core.ProjectKindPersonal, Scene: core.SceneWork, ModelProviderID: provider.ProviderID, DefaultModel: "catalog-only-model"}, "blocked-project")
	var validation *core.ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected disabled model validation, got %v", err)
	}
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Allowed", Kind: core.ProjectKindPersonal, Scene: core.SceneWork, ModelProviderID: provider.ProviderID, DefaultModel: "enabled-model"}, "allowed-project")
	if err != nil {
		t.Fatal(err)
	}
	if project.ModelProviderID != provider.ProviderID || project.DefaultModel != "enabled-model" {
		t.Fatalf("project model boundary not persisted: %+v", project)
	}
	inherited, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Inherited", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "inherited-project")
	if err != nil {
		t.Fatal(err)
	}
	if inherited.ModelProviderID != provider.ProviderID || inherited.DefaultModel != "" {
		t.Fatalf("project did not inherit the active default provider: %+v", inherited)
	}
}

func TestProviderChangeAppliesToNextRunWithoutMutatingExistingSnapshot(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	secrets := &memoryProviderSecrets{values: map[string]string{}}
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithProviderRegistry(secrets, nil)
	credential := "provider-secret"
	provider, err := service.SaveProvider(t.Context(), "", application.SaveProviderInput{
		DisplayName: "Primary", ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint: "https://primary.example.test/v1", DefaultModelID: "model-a", Credential: &credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.ReplaceModelCatalog(t.Context(), org.ID, provider.ProviderID, []core.ModelCatalogEntry{{
		ProviderID: provider.ProviderID, ModelID: "model-a", CapabilitiesJSON: "{}",
		FetchedAt: now, ExpiresAt: now.Add(time.Hour), CatalogVersion: "catalog-a",
	}}); err != nil {
		t.Fatal(err)
	}
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Project", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{
		Goal: "first", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate,
		ProviderID: provider.ProviderID, Thinking: "high",
	}, "correlation-first", "")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.SaveProvider(t.Context(), provider.ProviderID, application.SaveProviderInput{
		DisplayName: provider.DisplayName, ProtocolFamily: provider.ProtocolFamily,
		Endpoint: provider.Endpoint, DefaultModelID: "model-b", IsDefault: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateTask(t.Context(), project.ID, core.CreateTaskInput{
		Goal: "second", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate,
		ProviderID: updated.ProviderID,
	}, "correlation-second", "")
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := store.GetRunContext(t.Context(), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	secondRun, err := store.GetRunContext(t.Context(), second.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if firstRun.EffectiveModel.ModelID != "model-a" || firstRun.Thinking != "high" || firstRun.ModelCatalogVersion != "catalog-a" {
		t.Fatalf("first run snapshot=%+v", firstRun)
	}
	if secondRun.EffectiveModel.ModelID != "model-b" || secondRun.Thinking != "auto" {
		t.Fatalf("second run snapshot=%+v", secondRun)
	}
}

func openProviderTestStore(t *testing.T) (*sqlite.Store, core.Actor, core.Organization) {
	t.Helper()
	root := t.TempDir()
	store, err := sqlite.Open(t.Context(), filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000001", DisplayName: "Owner"}
	org := core.Organization{ID: "00000000-0000-7000-8000-000000000002", Name: "Personal"}
	if err := store.Bootstrap(t.Context(), actor, org); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, actor, org
}

func storeProviderRef(t *testing.T, store *sqlite.Store, orgID, providerID string) string {
	t.Helper()
	provider, err := store.GetProviderInstance(t.Context(), orgID, providerID)
	if err != nil {
		t.Fatal(err)
	}
	return provider.CredentialRef
}
