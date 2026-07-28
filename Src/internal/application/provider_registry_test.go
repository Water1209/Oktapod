package application_test

import (
	"context"
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
