package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestCleanupRemovesOnlyOwnedExpiredTemporaryFiles(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	workspaceRoot := filepath.Join(filepath.Dir(databasePath), "workspaces")
	temporary := filepath.Join(workspaceRoot, ".oktapod-write-expired")
	userFile := filepath.Join(workspaceRoot, "keep.txt")
	if err := os.WriteFile(temporary, []byte("temp"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userFile, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(temporary, old, old); err != nil {
		t.Fatal(err)
	}
	report, err := repository.Cleanup(context.Background(), core.Scope{OrganizationID: testOrgID, UserID: testUserID}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if report.DeletedTempFiles != 1 {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary file still exists: %v", err)
	}
	if _, err := os.Stat(userFile); err != nil {
		t.Fatalf("user file was removed: %v", err)
	}
}

func TestCleanupRespectsProjectLegalHold(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	workspaceRoot := filepath.Join(filepath.Dir(databasePath), "workspaces")
	projectRoot := filepath.Join(workspaceRoot, "00000000-0000-7000-8000-000000000099")
	if err := os.MkdirAll(projectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(projectRoot, ".oktapod-write-expired")
	if err := os.WriteFile(filepath.Join(projectRoot, ".oktapod-legal-hold"), []byte("hold"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protected, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(protected, old, old); err != nil {
		t.Fatal(err)
	}
	report, err := repository.Cleanup(context.Background(), core.Scope{OrganizationID: testOrgID, UserID: testUserID}, time.Now())
	if err != nil || report.DeletedTempFiles != 0 || report.SkippedLegalHold == 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := os.Stat(protected); err != nil {
		t.Fatalf("held file was removed: %v", err)
	}
}

func TestCleanupFailsClosedWhenRunIsActive(t *testing.T) {
	t.Parallel()
	repository, databasePath := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://example.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	project, err := service.CreateProject(context.Background(), core.CreateProjectInput{Name: "Cleanup fence", Kind: core.ProjectKindPersonal, Scene: core.SceneCode}, "cleanup-fence-project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateTask(context.Background(), project.ID, core.CreateTaskInput{Goal: "active run blocks cleanup", Mode: core.ModeSingleAgent, Visibility: core.VisibilityPrivate}, "cleanup-fence-task", "cleanup-fence-task"); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(filepath.Dir(databasePath), "workspaces", ".oktapod-write-expired")
	if err := os.WriteFile(temporary, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(temporary, old, old); err != nil {
		t.Fatal(err)
	}
	report, err := repository.Cleanup(context.Background(), core.Scope{OrganizationID: testOrgID, UserID: testUserID}, time.Now())
	if err != nil || report.DeletedTempFiles != 0 || report.SkippedProtected == 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := os.Stat(temporary); err != nil {
		t.Fatalf("active-run protected file was removed: %v", err)
	}
}
