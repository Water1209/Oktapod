package core

import (
	"strings"
	"testing"
)

func TestParseMCPDefinitionNormalizesLegacyManifest(t *testing.T) {
	definition, err := ParseMCPDefinition(`{"transport":"stdio","command":"npx","args":["-y","example@1.2.3"],"auth_env":"EXAMPLE_API_KEY","permissions":["network"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if definition.SchemaVersion != MCPDefinitionSchemaV1 || definition.Transport.Type != "stdio" || definition.Auth.Type != "environment" || len(definition.SecretBindings) != 1 {
		t.Fatalf("definition=%#v", definition)
	}
	encoded, err := definition.JSON()
	if err != nil || !strings.Contains(encoded, `"schema_version":"oktapod.mcp/v1"`) {
		t.Fatalf("encoded=%s err=%v", encoded, err)
	}
}

func TestParseMCPDefinitionAcceptsRemoteAuthModes(t *testing.T) {
	for _, auth := range []string{"none", "bearer", "oauth", "custom_header"} {
		header := ""
		if auth == "custom_header" {
			header = `,"header":"X-API-Key"`
		}
		raw := `{"schema_version":"oktapod.mcp/v1","source":{"type":"manual"},"transport":{"type":"streamable_http","url":"https://example.com/mcp"},"auth":{"type":"` + auth + `"` + header + `}}`
		definition, err := ParseMCPDefinition(raw)
		if err != nil {
			t.Fatalf("auth=%s err=%v", auth, err)
		}
		expectedBindings := 0
		if auth == "bearer" || auth == "custom_header" {
			expectedBindings = 1
		}
		if len(definition.SecretBindings) != expectedBindings {
			t.Fatalf("auth=%s bindings=%#v", auth, definition.SecretBindings)
		}
	}
}

func TestParseMCPDefinitionRejectsEmbeddedSecrets(t *testing.T) {
	for _, raw := range []string{
		`{"transport":"streamable_http","url":"https://example.com/mcp?api_key=secret"}`,
		`{"transport":"stdio","command":"npx","api_key":"secret"}`,
		`{"transport":"streamable_http","url":"https://example.com/mcp","headers":{"Authorization":"Bearer secret"}}`,
	} {
		if _, err := ParseMCPDefinition(raw); err == nil {
			t.Fatalf("embedded secret accepted: %s", raw)
		}
	}
}

func TestParseMCPDefinitionRejectsUnsafeOrDuplicateSecretTargets(t *testing.T) {
	for name, manifest := range map[string]string{
		"reserved-header": `{"transport":{"type":"streamable_http","url":"https://example.com/mcp"},"auth":{"type":"bearer"},"secret_bindings":[{"name":"token","target":"header:Host","required":true}]}`,
		"invalid-env":     `{"transport":{"type":"stdio","command":"server"},"auth":{"type":"environment"},"secret_bindings":[{"name":"token","target":"env:BAD-NAME","required":true}]}`,
		"duplicate":       `{"transport":{"type":"stdio","command":"server"},"auth":{"type":"environment"},"secret_bindings":[{"name":"one","target":"env:TOKEN","required":true},{"name":"two","target":"env:TOKEN","required":true}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMCPDefinition(manifest); err == nil {
				t.Fatal("unsafe Secret binding was accepted")
			}
		})
	}
}
