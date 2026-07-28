package integration_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestCapabilityGlobalAndProjectScopesAreResolvedPerProject(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	first, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Capability scope one", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Capability scope two", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}

	global, err := service.CreateCapability(context.Background(), first.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "global-skill", ManifestJSON: `{"description":"global","instructions":"global"}`}, "global-skill")
	if err != nil {
		t.Fatal(err)
	}
	projectOnly, err := service.CreateCapability(context.Background(), first.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "project-skill", ManifestJSON: `{"description":"project","instructions":"project"}`, ScopeType: core.CapabilityScopeProject}, "project-skill")
	if err != nil {
		t.Fatal(err)
	}
	if global.ScopeType != core.CapabilityScopeGlobal || projectOnly.ScopeType != core.CapabilityScopeProject {
		t.Fatalf("global=%#v project=%#v", global, projectOnly)
	}
	duplicate, err := service.CreateCapability(context.Background(), second.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "global-skill", Version: "0.1.0", ManifestJSON: `{"description":"global","instructions":"global"}`}, "global-skill-duplicate")
	if err != nil || duplicate.ID != global.ID {
		t.Fatalf("same content must be idempotent: duplicate=%#v err=%v", duplicate, err)
	}
	upgrade, err := service.CreateCapability(context.Background(), second.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "global-skill", Version: "0.2.0", ManifestJSON: `{"description":"global v2","instructions":"global v2"}`}, "global-skill-upgrade")
	if err != nil || upgrade.ID == global.ID || upgrade.Version != "0.2.0" {
		t.Fatalf("new version must coexist: upgrade=%#v err=%v", upgrade, err)
	}
	firstItems, err := service.ListCapabilities(context.Background(), first.ID)
	if err != nil || len(firstItems) != 3 {
		t.Fatalf("first items=%#v err=%v", firstItems, err)
	}
	secondItems, err := service.ListCapabilities(context.Background(), second.ID)
	if err != nil || len(secondItems) != 2 {
		t.Fatalf("second items=%#v err=%v", secondItems, err)
	}
	if _, err := service.CreateCapability(context.Background(), second.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "global-skill", Version: "0.1.0", ManifestJSON: `{"description":"tampered","instructions":"tampered"}`}, "global-skill-conflict"); err == nil {
		t.Fatal("same global name and version with a different digest must conflict")
	}
}

func TestCapabilityZipBundleIsStoredAndExecutableContentIsQuarantined(t *testing.T) {
	t.Parallel()
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Capability bundle", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	bundle := integrationCapabilityZip(t, map[string]string{
		"bundle/SKILL.md":         "---\nname: bundled-skill\ndescription: Bundled skill.\n---\nUse the workflow.\n",
		"bundle/scripts/check.py": "print('safe test fixture')\n",
	})
	capability, err := service.CreateCapability(context.Background(), project.ID, core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: "bundle", ManifestJSON: `{}`, SourceFilename: "bundle.zip", BundleBase64: bundle}, "bundle-install")
	if err != nil {
		t.Fatal(err)
	}
	if capability.Name != "bundled-skill" || capability.Status != core.CapabilityStatusQuarantined || capability.RiskLevel != "review" || capability.InstallJobID == "" || capability.BundleHash != capability.ContentHash {
		t.Fatalf("capability=%#v", capability)
	}
}

func integrationCapabilityZip(t *testing.T, files map[string]string) string {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}
