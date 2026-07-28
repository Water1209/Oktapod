package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestSQLiteBackupManifestVerifiesAndRestores(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	workspaceRoot := filepath.Join(root, "workspaces")
	store, err := sqlite.Open(context.Background(), databasePath, workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Bootstrap(context.Background(), core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "evidence.txt"), []byte("immutable evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backup")
	manifest, err := store.Backup(context.Background(), backupDir)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if manifest.Database.ContentHash == "" || len(manifest.Workspaces) != 1 {
		t.Fatalf("manifest=%#v", manifest)
	}
	restoreRoot := filepath.Join(root, "restored")
	if _, err := sqlite.Restore(context.Background(), backupDir, filepath.Join(restoreRoot, "oktapod.db"), filepath.Join(restoreRoot, "workspaces")); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restored, err := sqlite.Open(context.Background(), filepath.Join(restoreRoot, "oktapod.db"), filepath.Join(restoreRoot, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(restoreRoot, "workspaces", "evidence.txt"))
	if err != nil || string(content) != "immutable evidence" {
		t.Fatalf("restored content=%q err=%v", content, err)
	}
}

func TestSQLiteUpgradePreflightIsReadOnlyAndStopsActiveRuns(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	store, err := sqlite.Open(context.Background(), databasePath, filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	bootstrapRepository(t, store)
	service := application.NewService(store, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Preflight", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "preflight-project")
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "must remain visible to preflight", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "preflight-task", "preflight-task"); err != nil {
		store.Close()
		t.Fatal(err)
	}
	report, err := sqlite.PreflightSQLite(context.Background(), databasePath)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if report.Safe || report.ActiveRunCount == 0 || report.StoredSchemaVersion != sqlite.CurrentSchemaVersion {
		t.Fatalf("preflight=%#v", report)
	}
	store.Close()

	cleanRoot := t.TempDir()
	cleanPath := filepath.Join(cleanRoot, "new.db")
	cleanStore, err := sqlite.Open(context.Background(), cleanPath, filepath.Join(cleanRoot, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	cleanStore.Close()
	cleanReport, err := sqlite.PreflightSQLite(context.Background(), cleanPath)
	if err != nil || !cleanReport.Safe || cleanReport.ActiveRunCount != 0 {
		t.Fatalf("clean preflight=%#v err=%v", cleanReport, err)
	}
}

func TestRestoreRejectsSnapshotFromNewerSchema(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := sqlite.Open(context.Background(), filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Bootstrap(context.Background(), core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backup")
	if _, err := store.Backup(context.Background(), backupDir); err != nil {
		store.Close()
		t.Fatal(err)
	}
	store.Close()
	manifestPath := filepath.Join(backupDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["schema_version"] = float64(sqlite.CurrentSchemaVersion + 1)
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.Restore(context.Background(), backupDir, filepath.Join(root, "new", "oktapod.db"), filepath.Join(root, "new", "workspaces")); err == nil {
		t.Fatal("restore accepted a newer schema snapshot")
	}
}
