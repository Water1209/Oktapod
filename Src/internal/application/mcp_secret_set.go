package application

import (
	"encoding/json"
	"fmt"
	"strings"
)

const mcpSecretSetSchemaV1 = "oktapod.mcp-secrets/v1"

type mcpSecretSet struct {
	Schema string            `json:"schema"`
	Values map[string]string `json:"values"`
}

// encodeMCPSecretSet produces one opaque SecretStore payload. The payload is
// never persisted with the capability; names in MCPDefinition v1 select the
// values that are injected into approved env/header targets at runtime.
func encodeMCPSecretSet(values map[string]string) (string, error) {
	if len(values) == 0 || len(values) > 16 {
		return "", fmt.Errorf("must contain between 1 and 16 named Secret values")
	}
	normalized := make(map[string]string, len(values))
	for name, value := range values {
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if name == "" || len(name) > 120 || value == "" || len(value) > 8192 {
			return "", fmt.Errorf("contains an invalid Secret name or value")
		}
		normalized[name] = value
	}
	encoded, err := json.Marshal(mcpSecretSet{Schema: mcpSecretSetSchemaV1, Values: normalized})
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
