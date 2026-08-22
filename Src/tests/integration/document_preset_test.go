package integration_test

import (
	"context"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type healthyDocumentRuntime struct{}

func (healthyDocumentRuntime) Health(context.Context) application.DocumentRuntimeHealth {
	return application.DocumentRuntimeHealth{Available: true, Version: "1.0.0-test", Protocol: "oktapod.document/v1", Renderer: "test"}
}

func (healthyDocumentRuntime) Execute(_ context.Context, _ core.RunContext, call core.ToolCall) core.ToolExecution {
	return core.ToolExecution{Status: core.StatusSucceeded, Output: map[string]any{"tool_name": call.Name}}
}

func TestPresetDocumentResourcesRememberDeletionAndProtectDependencies(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).WithDocumentRuntime(healthyDocumentRuntime{})
	project, err := service.CreateProject(t.Context(), core.CreateProjectInput{Name: "Document presets", Kind: core.ProjectKindPersonal, Scene: core.SceneWork}, "")
	if err != nil {
		t.Fatal(err)
	}

	items, err := service.ListCapabilities(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	presets := presetCapabilityMap(items)
	if len(presets) != 5 {
		t.Fatalf("preset count=%d want=5 items=%#v", len(presets), items)
	}
	for key, item := range presets {
		if item.Origin != core.CapabilityOriginPreset || item.ManagedVersion == "" || item.Availability != core.ResourceAvailabilityAvailable {
			t.Fatalf("preset %s is not available: %#v", key, item)
		}
	}

	presentation := presets["presentations"]
	if _, err := service.SetCapabilityStatus(t.Context(), presentation.ID, core.CapabilityStatusDisabled, "disable-presentation-preset"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ListCapabilities(t.Context(), project.ID); err != nil {
		t.Fatal(err)
	}
	disabled, err := repository.GetCapability(t.Context(), core.Scope{OrganizationID: testOrgID, UserID: testUserID}, presentation.ID)
	if err != nil || disabled.Status != core.CapabilityStatusDisabled {
		t.Fatalf("preset disable state was not preserved: %#v err=%v", disabled, err)
	}

	pdf := presets["pdf"]
	if err := service.DeleteCapability(t.Context(), pdf.ID, "delete-pdf-preset"); err != nil {
		t.Fatal(err)
	}
	restarted := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" }).WithDocumentRuntime(healthyDocumentRuntime{})
	afterRestart, err := restarted.ListCapabilities(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, restoredSilently := presetCapabilityMap(afterRestart)["pdf"]; restoredSilently {
		t.Fatal("deleted preset was silently restored by a later resource refresh")
	}
	catalog, err := restarted.ListResourceCatalog(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state := catalogInstallState(catalog, "pdf"); state != "available" {
		t.Fatalf("deleted preset catalog state=%q want available", state)
	}
	restored, err := restarted.InstallPresetResource(t.Context(), project.ID, "pdf")
	if err != nil || restored.PresetKey != "pdf" {
		t.Fatalf("manual preset restore=%#v err=%v", restored, err)
	}

	current, err := restarted.ListCapabilities(t.Context(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCapability := presetCapabilityMap(current)["document-runtime"]
	if err := restarted.DeleteCapability(t.Context(), runtimeCapability.ID, "delete-runtime-with-dependencies"); err == nil {
		t.Fatal("runtime deletion was not blocked while dependent skills remained installed")
	}
}

func customCapabilities(items []core.Capability) []core.Capability {
	result := make([]core.Capability, 0, len(items))
	for _, item := range items {
		if item.Origin == core.CapabilityOriginCustom {
			result = append(result, item)
		}
	}
	return result
}

func presetCapabilityMap(items []core.Capability) map[string]core.Capability {
	result := make(map[string]core.Capability)
	for _, item := range items {
		if item.PresetKey != "" {
			result[item.PresetKey] = item
		}
	}
	return result
}

func catalogInstallState(items []core.PresetResourceCatalogItem, key string) string {
	for _, item := range items {
		if item.PresetKey == key {
			return item.InstallState
		}
	}
	return ""
}
