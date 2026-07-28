package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestEstimateModelCostUsesConfiguredSnapshot(t *testing.T) {
	t.Setenv("OKTAPOD_MODEL_PRICING_JSON", `{"model-a":{"input_per_1k":0.001,"output_per_1k":0.002}}`)
	if got := estimateModelCost("model-a", core.ModelUsage{InputTokens: 1000, OutputTokens: 500}); got != "0.00200000" {
		t.Fatalf("cost=%s", got)
	}
}

func TestResolveModelPricingPersistsImmutableSnapshotMetadata(t *testing.T) {
	t.Setenv("OKTAPOD_MODEL_PRICING_JSON", `{"model-a":{"input_per_1k":0.001,"output_per_1k":0.002}}`)
	snapshot, cost := resolveModelPricing("model-a", core.ModelUsage{InputTokens: 1000, OutputTokens: 500})
	if cost != "0.00200000" {
		t.Fatalf("cost=%s", cost)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(snapshot), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "priced" || payload["currency"] != "USD" || payload["version"] == "" {
		t.Fatalf("snapshot=%s", snapshot)
	}
}

func TestResolveModelPricingMarksUnpricedCalls(t *testing.T) {
	t.Setenv("OKTAPOD_MODEL_PRICING_JSON", "")
	snapshot, cost := resolveModelPricing("model-a", core.ModelUsage{InputTokens: 1, OutputTokens: 1})
	if cost != "0" || !strings.Contains(snapshot, `"status":"unpriced"`) {
		t.Fatalf("snapshot=%s cost=%s", snapshot, cost)
	}
}
