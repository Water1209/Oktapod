package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestResolveModelPricingUsesVersionedDatabaseSnapshot(t *testing.T) {
	ctx := context.Background()
	store, organizationID := openPricingStore(t, ctx)
	now := time.Now().UTC().Add(-time.Minute)
	items, err := store.UpsertModelPricingVersions(ctx, organizationID, []core.ModelPricingVersion{{
		ProviderID: "provider-a", ModelID: "model-a", Currency: "CNY",
		InputPer1K: "0.001", OutputPer1K: "0.002", EffectiveAt: now, Version: 1,
	}})
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	snapshot, cost, currency := resolveModelPricing(ctx, store.db, organizationID, "provider-a", "model-a", core.ModelUsage{InputTokens: 1000, OutputTokens: 500}, time.Now().UTC())
	if cost != "0.00200000" || currency != "CNY" {
		t.Fatalf("cost=%s currency=%s", cost, currency)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(snapshot), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "priced" || payload["currency"] != "CNY" || payload["version"] != float64(1) || payload["provider_id"] != "provider-a" {
		t.Fatalf("snapshot=%s", snapshot)
	}
}

func TestResolveModelPricingMarksUnpricedCalls(t *testing.T) {
	ctx := context.Background()
	store, organizationID := openPricingStore(t, ctx)
	snapshot, cost, currency := resolveModelPricing(ctx, store.db, organizationID, "provider-a", "model-a", core.ModelUsage{InputTokens: 1, OutputTokens: 1}, time.Now().UTC())
	if cost != "0" || currency != "USD" || !strings.Contains(snapshot, `"status":"unpriced"`) {
		t.Fatalf("snapshot=%s cost=%s currency=%s", snapshot, cost, currency)
	}
}

func openPricingStore(t *testing.T, ctx context.Context) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	organizationID := "00000000-0000-7000-8000-000000000002"
	if err := store.Bootstrap(ctx, core.Actor{ID: "00000000-0000-7000-8000-000000000001", DisplayName: "User"}, core.Organization{ID: organizationID, Name: "Org"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProviderInstance(ctx, organizationID, core.UpsertProviderInstanceInput{ProviderID: "provider-a", DisplayName: "Provider A", ProtocolFamily: core.ProviderProtocolOpenAICompatible, Endpoint: "https://example.test/v1", CredentialRef: "model-provider/org/ref", DefaultModelID: "model-a", Status: core.ProviderStatusActive, IsDefault: true}); err != nil {
		t.Fatal(err)
	}
	return store, organizationID
}
