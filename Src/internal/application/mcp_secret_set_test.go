package application

import (
	"encoding/json"
	"testing"
)

func TestEncodeMCPSecretSetKeepsNamedValuesInsideOpaqueEnvelope(t *testing.T) {
	encoded, err := encodeMCPSecretSet(map[string]string{"SEARCH_TOKEN": "first-value", "EXTRACT_TOKEN": "second-value"})
	if err != nil {
		t.Fatal(err)
	}
	var envelope mcpSecretSet
	if err := json.Unmarshal([]byte(encoded), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Schema != mcpSecretSetSchemaV1 || len(envelope.Values) != 2 || envelope.Values["EXTRACT_TOKEN"] != "second-value" {
		t.Fatalf("envelope=%#v", envelope)
	}
}
