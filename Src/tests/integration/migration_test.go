package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestProjectArchiveMigratesPersonalProjectIntoTeamScope(t *testing.T) {
	ctx := context.Background()
	source, sourceDB := newRepository(t)
	defer source.Close()
	bootstrapRepository(t, source)
	sourceService := application.NewService(source, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Source User"}, core.Organization{ID: testOrgID, Name: "Source Org"}, func() string { return "" })
	project, err := sourceService.CreateProject(ctx, core.CreateProjectInput{Name: "迁移项目", Description: "source", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "")
	if err != nil {
		t.Fatal(err)
	}
	workspaceFile := filepath.Join(filepath.Dir(sourceDB), "workspaces", project.ID, "notes", "readme.md")
	if err := os.MkdirAll(filepath.Dir(workspaceFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workspaceFile, []byte("portable project evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(filepath.Dir(sourceDB), "workspaces", project.ID, ".env")
	if err := os.WriteFile(secretFile, []byte("SHOULD_NOT_MOVE=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sourceService.CreateCapability(ctx, project.ID, core.CreateCapabilityInput{
		Kind: core.CapabilityKindMCP, Name: "portable-mcp", Version: "1.0.0", ScopeType: core.CapabilityScopeProject,
		ManifestJSON:  "{\"schema_version\":\"oktapod.mcp/v1\",\"source\":{\"type\":\"manual\"},\"transport\":{\"type\":\"streamable_http\",\"url\":\"https://mcp.example.test\"},\"auth\":{\"type\":\"bearer\"},\"secret_bindings\":[{\"name\":\"credential\",\"target\":\"header:Authorization\",\"required\":true}]}",
		CredentialRef: "mcp-capability/source/secret-must-not-export",
	}, "migration-mcp"); err != nil {
		t.Fatal(err)
	}

	archiveDir := filepath.Join(t.TempDir(), "project-archive")
	archive, err := source.ExportProject(ctx, core.Scope{OrganizationID: testOrgID, UserID: testUserID}, project.ID, archiveDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Files) != 1 || len(archive.SkippedSecrets) != 1 {
		t.Fatalf("archive files=%#v skipped=%#v", archive.Files, archive.SkippedSecrets)
	}
	if rows := archive.Tables["capabilities"]; len(rows) != 1 || rows[0]["credential_ref"] != "" || rows[0]["status"] != core.CapabilityStatusDisabled {
		t.Fatalf("exported capability must require a new Secret: %#v", rows)
	}

	targetRoot := t.TempDir()
	target, err := sqlite.Open(ctx, filepath.Join(targetRoot, "oktapod.db"), filepath.Join(targetRoot, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetUser := "00000000-0000-7000-8000-000000000111"
	targetOrg := "00000000-0000-7000-8000-000000000112"
	if err := target.Bootstrap(ctx, core.Actor{ID: targetUser, DisplayName: "Team Owner"}, core.Organization{ID: targetOrg, Name: "Target Team"}); err != nil {
		t.Fatal(err)
	}
	imported, importedArchive, err := target.ImportProject(ctx, core.Scope{OrganizationID: targetOrg, UserID: targetUser}, archiveDir)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Kind != core.ProjectKindTeam || imported.ID == project.ID || imported.OrganizationID != targetOrg || imported.OwnerUserID != targetUser {
		t.Fatalf("imported project=%#v", imported)
	}
	if importedArchive.SourceProjectID != project.ID {
		t.Fatalf("archive source=%q", importedArchive.SourceProjectID)
	}
	content, err := os.ReadFile(filepath.Join(targetRoot, "workspaces", imported.ID, "notes", "readme.md"))
	if err != nil || string(content) != "portable project evidence" {
		t.Fatalf("imported workspace content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(targetRoot, "workspaces", imported.ID, ".env")); !os.IsNotExist(err) {
		t.Fatalf("secret file should not be imported, err=%v", err)
	}
	capabilities, err := target.ListCapabilities(ctx, core.Scope{OrganizationID: targetOrg, UserID: targetUser}, imported.ID, 10)
	if err != nil || len(capabilities) != 1 || capabilities[0].CredentialRef != "" || capabilities[0].Status != core.CapabilityStatusDisabled {
		t.Fatalf("imported MCP must be disconnected and require a new Secret: items=%#v err=%v", capabilities, err)
	}
}
