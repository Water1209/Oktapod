package application_test

import (
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestMCPRequiredSecretWithoutCredentialRefIsMissing(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithProviderRegistry(&memoryProviderSecrets{values: map[string]string{}}, nil)
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "MCP Secret State", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.CreateCapability(t.Context(), project.ID, core.CreateCapabilityInput{
		Kind: core.CapabilityKindMCP, Name: "missing-secret", Version: "1.0.0", ScopeType: core.CapabilityScopeProject,
		ManifestJSON: `{"schema_version":"oktapod.mcp/v1","source":{"type":"manual"},"transport":{"type":"streamable_http","url":"https://mcp.example.test"},"auth":{"type":"bearer"},"secret_bindings":[{"name":"credential","target":"header:Authorization","required":true}]}`,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if capability.CredentialStatus != core.CredentialMissing || capability.CredentialExists {
		t.Fatalf("credential status=%q exists=%v", capability.CredentialStatus, capability.CredentialExists)
	}
}
