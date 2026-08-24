package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestResourceInstallDraftResponseKeepsRequiredCollectionsAsArrays(t *testing.T) {
	now := time.Now().UTC()
	response, err := resourceInstallDraftResponse(core.ResourceInstallDraft{
		ID: uuid.NewString(), Kind: core.CapabilityKindMCP, SourceInput: "{}", Status: "validated",
		Warnings: []string{}, RequiredSecrets: []core.MCPSecretBinding{}, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"warnings":[]`) || !strings.Contains(text, `"required_secrets":[]`) {
		t.Fatalf("required collection encoded as null: %s", text)
	}
}
