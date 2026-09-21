package application_test

import (
	"context"
	"errors"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type recordingRuntimeDependencyRepository struct {
	application.Repository
	connectCalls int
	auditActions []string
}

func (r *recordingRuntimeDependencyRepository) ConnectMCP(ctx context.Context, scope core.Scope, capabilityID, credential string, updater func(context.Context, string) error, correlationID string) (core.MCPHealth, error) {
	r.connectCalls++
	return core.MCPHealth{Status: core.MCPStatusConnected, LastError: "Executable doesn't exist at chrome-for-testing"}, nil
}

func (r *recordingRuntimeDependencyRepository) AuditCapability(ctx context.Context, scope core.Scope, capabilityID, action string, detail map[string]any, correlationID string) error {
	r.auditActions = append(r.auditActions, action)
	return nil
}

type recordingRuntimeDependencyProvider struct {
	detectCalls   int
	installCalls  int
	installedItem application.RuntimeDependency
}

func (p *recordingRuntimeDependencyProvider) Detect(_ context.Context, request application.RuntimeDependencyRequest) ([]application.RuntimeDependency, error) {
	p.detectCalls++
	return []application.RuntimeDependency{{
		ID: "runtime", Kind: "binary", Provider: "generic", Name: "runtime",
		Version: "1.2.3", DisplayName: "通用运行时", Description: "Provider 自描述的运行时依赖", Installable: true,
	}}, nil
}

func (p *recordingRuntimeDependencyProvider) Supports(dependency application.RuntimeDependency) bool {
	return dependency.Provider == "generic" && dependency.ID == "runtime"
}

func (p *recordingRuntimeDependencyProvider) Install(_ context.Context, request application.RuntimeDependencyInstallRequest) error {
	p.installCalls++
	p.installedItem = request.Dependency
	return nil
}

func TestInstallRuntimeDependencyRequiresConfirmation(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	if _, err := service.InstallRuntimeDependency(t.Context(), "id", "generic", "runtime", "test", false); err == nil {
		t.Fatal("install was allowed without explicit user confirmation")
	}
}

func TestInstallRuntimeDependencyUsesProviderThenAuditsAndReconnects(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	repository := &recordingRuntimeDependencyRepository{Repository: store}
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" }).
		WithRuntimeDependencyProvider(&recordingRuntimeDependencyProvider{})

	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Runtime dependency", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.CreateCapability(t.Context(), project.ID, core.CreateCapabilityInput{
		Kind: core.CapabilityKindMCP, Name: "generic-runtime", Version: "1.0.0", ScopeType: core.CapabilityScopeProject,
		ManifestJSON: `{"schema_version":"oktapod.mcp/v1","source":{"type":"manual"},"transport":{"type":"streamable_http","url":"https://mcp.example.test"}}`,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewCapability(t.Context(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "test"}, "review", ""); err != nil {
		t.Fatal(err)
	}

	items, err := service.ListRuntimeDependencies(t.Context(), capability.ID)
	if err != nil || len(items) != 1 || !items[0].Installable {
		t.Fatalf("dependencies=%v err=%v", items, err)
	}
	updated, err := service.InstallRuntimeDependency(t.Context(), capability.ID, items[0].Provider, items[0].ID, "install", true)
	if err != nil {
		t.Fatal(err)
	}
	provider := service.RuntimeDependencyProviders()[0].(*recordingRuntimeDependencyProvider)
	if provider.installCalls != 1 || provider.installedItem.Provider != "generic" {
		t.Fatalf("provider install calls=%d dependency=%+v", provider.installCalls, provider.installedItem)
	}
	if repository.connectCalls != 1 || len(repository.auditActions) != 1 || repository.auditActions[0] != "capability.runtime_dependency_installed" {
		t.Fatalf("connect calls=%d audits=%v", repository.connectCalls, repository.auditActions)
	}
	if updated.ID != capability.ID {
		t.Fatalf("updated capability=%+v", updated)
	}
}

func TestGetMCPHealthDistinguishesUnconnectedMCPFromMissingCapability(t *testing.T) {
	store, actor, org := openProviderTestStore(t)
	defer store.Close()
	service := application.NewService(store, core.ProfilePersonal, "https://provider.invalid/v1", actor, org, func() string { return "" })
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Unconnected MCP", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	capability, err := service.CreateCapability(t.Context(), project.ID, core.CreateCapabilityInput{
		Kind: core.CapabilityKindMCP, Name: "unconnected", Version: "1.0.0", ScopeType: core.CapabilityScopeProject,
		ManifestJSON: `{"schema_version":"oktapod.mcp/v1","source":{"type":"manual"},"transport":{"type":"streamable_http","url":"https://mcp.example.test"}}`,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReviewCapability(t.Context(), capability.ID, core.ReviewCapabilityInput{Decision: core.CapabilityReviewApproved, Note: "test"}, "review", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetMCPHealth(t.Context(), capability.ID); !errors.Is(err, core.ErrMCPNotConnected) {
		t.Fatalf("health error=%v want ErrMCPNotConnected", err)
	}
}
