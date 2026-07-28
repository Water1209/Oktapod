package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestProviderRegistrySupportsMultipleInstancesAndCollidingModelIDs(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000001", DisplayName: "Owner"}
	org := core.Organization{ID: "00000000-0000-7000-8000-000000000002", Name: "Personal"}
	if err := store.Bootstrap(ctx, actor, org); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"provider-a", "provider-b"} {
		_, err = store.UpsertProviderInstance(ctx, org.ID, core.UpsertProviderInstanceInput{
			ProviderID: id, DisplayName: "Provider " + id,
			ProtocolFamily: core.ProviderProtocolOpenAICompatible,
			Endpoint:       "https://" + id + ".example.test/v1",
			CredentialRef:  "model-provider/test/" + id,
			DefaultModelID: "shared-model", Status: core.ProviderStatusActive,
			IsDefault: index == 0,
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := store.ReplaceModelCatalog(ctx, org.ID, id, []core.ModelCatalogEntry{{
			ProviderID: id, ModelID: "shared-model", CapabilitiesJSON: "{}",
			FetchedAt: now, ExpiresAt: now.Add(time.Hour), CatalogVersion: id + "-v1",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	providers, err := store.ListProviderInstances(ctx, org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 2 || !providers[0].IsDefault {
		t.Fatalf("providers=%+v", providers)
	}
	for _, id := range []string{"provider-a", "provider-b"} {
		models, err := store.ListModelCatalogEntries(ctx, org.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 1 || models[0].ProviderID != id || models[0].ModelID != "shared-model" {
			t.Fatalf("%s models=%+v", id, models)
		}
	}
}

func TestProviderRegistryNeverStoresPlaintextCredential(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var forbidden int
	err = store.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM pragma_table_info('provider_instances')
WHERE lower(name) IN ('api_key', 'credential', 'secret_value')`).Scan(&forbidden)
	if err != nil {
		t.Fatal(err)
	}
	if forbidden != 0 {
		t.Fatal("provider registry must store only credential references")
	}
}
