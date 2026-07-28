package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/db/migrations/sqlite"
)

// BackupEntry is an immutable manifest entry. Hashes make a restore auditable
// and let operators detect a partial copy before replacing live data.
type BackupEntry struct {
	Path        string `json:"path"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentHash string `json:"content_hash"`
}

type BackupManifest struct {
	FormatVersion int           `json:"format_version"`
	CreatedAt     time.Time     `json:"created_at"`
	SchemaVersion int           `json:"schema_version"`
	DatabaseType  string        `json:"database_type,omitempty"`
	Database      BackupEntry   `json:"database"`
	Workspaces    []BackupEntry `json:"workspaces"`
	Blobs         []BackupEntry `json:"blobs"`
}

// UpgradePreflight is a read-only compatibility check intended to run before
// an operator takes a backup or upgrades a Personal installation. It never
// applies migrations and it fails safe when there are in-flight runs or
// pending approvals that require a stable runtime.
type UpgradePreflight struct {
	DatabasePath          string   `json:"database_path"`
	StoredSchemaVersion   int      `json:"stored_schema_version"`
	BinarySchemaVersion   int      `json:"binary_schema_version"`
	PendingMigrationCount int      `json:"pending_migration_count"`
	ActiveRunCount        int      `json:"active_run_count"`
	PendingApprovalCount  int      `json:"pending_approval_count"`
	Safe                  bool     `json:"safe"`
	Reasons               []string `json:"reasons,omitempty"`
}

// CurrentSchemaVersion is the highest migration embedded in this binary.
// Restore accepts older snapshots (the next Store.Open applies migrations) but
// refuses snapshots produced by a newer binary to avoid silent data loss.
var CurrentSchemaVersion = embeddedSchemaVersion()

func embeddedSchemaVersion() int {
	entries, err := sqlitemigrations.FS.ReadDir(".")
	if err != nil {
		panic(fmt.Sprintf("read embedded migrations: %v", err))
	}
	latest := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		prefix := strings.SplitN(entry.Name(), "_", 2)[0]
		version, parseErr := strconv.Atoi(prefix)
		if parseErr != nil || version <= 0 {
			panic(fmt.Sprintf("invalid embedded migration filename %q", entry.Name()))
		}
		if version > latest {
			latest = version
		}
	}
	return latest
}

// PreflightSQLite opens the database read-only, so the check cannot silently
// migrate or mutate the installation it is evaluating.
func PreflightSQLite(ctx context.Context, databasePath string) (UpgradePreflight, error) {
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return UpgradePreflight{}, err
	}
	report := UpgradePreflight{DatabasePath: absolute, BinarySchemaVersion: CurrentSchemaVersion, Safe: true, Reasons: []string{}}
	if _, err := os.Stat(absolute); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			report.Safe = true
			report.Reasons = []string{"database does not exist; first start will create and migrate it"}
			return report, nil
		}
		return report, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(absolute)+"?mode=ro")
	if err != nil {
		return report, fmt.Errorf("open sqlite read-only: %w", err)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return report, fmt.Errorf("ping sqlite read-only: %w", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&report.StoredSchemaVersion); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return report, fmt.Errorf("read schema version: %w", err)
		}
		report.StoredSchemaVersion = 0
	}
	if report.StoredSchemaVersion > CurrentSchemaVersion {
		report.Safe = false
		report.Reasons = append(report.Reasons, "database schema is newer than this binary")
	} else if report.StoredSchemaVersion < CurrentSchemaVersion {
		report.PendingMigrationCount = CurrentSchemaVersion - report.StoredSchemaVersion
		report.Reasons = append(report.Reasons, "pending migrations will be applied on the next start")
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE status NOT IN ('succeeded', 'failed', 'canceled')`).Scan(&report.ActiveRunCount)
	if report.ActiveRunCount > 0 {
		report.Safe = false
		report.Reasons = append(report.Reasons, "active or resumable runs exist")
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_requests WHERE status = 'pending'`).Scan(&report.PendingApprovalCount)
	if report.PendingApprovalCount > 0 {
		report.Safe = false
		report.Reasons = append(report.Reasons, "pending approvals exist")
	}
	return report, nil
}

func (s *Store) Backup(ctx context.Context, targetDir string) (BackupManifest, error) {
	target, err := filepath.Abs(targetDir)
	if err != nil {
		return BackupManifest{}, err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return BackupManifest{}, fmt.Errorf("create backup directory: %w", err)
	}
	if err := ensureOutside(target, s.workspaceRoot, s.blobRoot); err != nil {
		return BackupManifest{}, err
	}
	if entries, err := os.ReadDir(target); err != nil {
		return BackupManifest{}, err
	} else if len(entries) != 0 {
		return BackupManifest{}, fmt.Errorf("backup target must be empty: %s", target)
	}

	databasePath := filepath.Join(target, "database.sqlite")
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, databasePath); err != nil {
		return BackupManifest{}, fmt.Errorf("snapshot sqlite database: %w", err)
	}
	workspaceTarget := filepath.Join(target, "workspaces")
	blobTarget := filepath.Join(target, "blobs")
	if err := copyTree(s.workspaceRoot, workspaceTarget); err != nil {
		return BackupManifest{}, fmt.Errorf("copy workspaces: %w", err)
	}
	if err := copyTree(s.blobRoot, blobTarget); err != nil {
		return BackupManifest{}, fmt.Errorf("copy blobs: %w", err)
	}
	manifest := BackupManifest{FormatVersion: 1, CreatedAt: time.Now().UTC(), DatabaseType: "sqlite"}
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&manifest.SchemaVersion)
	manifest.Database, err = fileEntry(databasePath, "database.sqlite")
	if err != nil {
		return BackupManifest{}, err
	}
	manifest.Workspaces, err = treeEntries(workspaceTarget, "workspaces")
	if err != nil {
		return BackupManifest{}, err
	}
	manifest.Blobs, err = treeEntries(blobTarget, "blobs")
	if err != nil {
		return BackupManifest{}, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return BackupManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), append(data, '\n'), 0o600); err != nil {
		return BackupManifest{}, err
	}
	return manifest, nil
}

func Restore(ctx context.Context, backupDir, databasePath, workspaceRoot string) (BackupManifest, error) {
	backup, err := filepath.Abs(backupDir)
	if err != nil {
		return BackupManifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil {
		return BackupManifest{}, fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return BackupManifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if manifest.FormatVersion != 1 {
		return BackupManifest{}, fmt.Errorf("unsupported backup format %d", manifest.FormatVersion)
	}
	if manifest.DatabaseType != "" && manifest.DatabaseType != "sqlite" {
		return BackupManifest{}, fmt.Errorf("backup database type %q cannot be restored by sqlite", manifest.DatabaseType)
	}
	if manifest.SchemaVersion <= 0 || manifest.SchemaVersion > CurrentSchemaVersion {
		return BackupManifest{}, fmt.Errorf("backup schema %d is not supported by this binary (current=%d)", manifest.SchemaVersion, CurrentSchemaVersion)
	}
	if err := verifyEntry(filepath.Join(backup, manifest.Database.Path), manifest.Database); err != nil {
		return BackupManifest{}, err
	}
	for _, entry := range append(append([]BackupEntry{}, manifest.Workspaces...), manifest.Blobs...) {
		if err := verifyEntry(filepath.Join(backup, entry.Path), entry); err != nil {
			return BackupManifest{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		return BackupManifest{}, err
	}
	if _, err := os.Stat(databasePath); err == nil {
		return BackupManifest{}, fmt.Errorf("refusing to overwrite existing database: %s", databasePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return BackupManifest{}, err
	}
	if err := copyFile(filepath.Join(backup, manifest.Database.Path), databasePath, 0o600); err != nil {
		return BackupManifest{}, err
	}
	if err := copyTree(filepath.Join(backup, "workspaces"), workspaceRoot); err != nil {
		return BackupManifest{}, fmt.Errorf("restore workspaces: %w", err)
	}
	if err := copyTree(filepath.Join(backup, "blobs"), filepath.Join(filepath.Dir(workspaceRoot), "blobs")); err != nil {
		return BackupManifest{}, fmt.Errorf("restore blobs: %w", err)
	}
	return manifest, nil
}

func ensureOutside(target string, roots ...string) error {
	for _, root := range roots {
		abs, _ := filepath.Abs(root)
		rel, err := filepath.Rel(abs, target)
		if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			return fmt.Errorf("backup target must not be inside live data root: %s", root)
		}
	}
	return nil
}

func copyTree(source, target string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		destination := filepath.Join(target, rel)
		if info.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		return copyFile(path, destination, 0o600)
	})
}

func copyFile(source, target string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileEntry(path, relative string) (BackupEntry, error) {
	if _, err := os.Stat(path); err != nil {
		return BackupEntry{}, err
	}
	hash, size, err := hashFile(path)
	if err != nil {
		return BackupEntry{}, err
	}
	return BackupEntry{Path: filepath.ToSlash(relative), SizeBytes: size, ContentHash: hash}, nil
}

func treeEntries(root, prefix string) ([]BackupEntry, error) {
	entries := []BackupEntry{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry, err := fileEntry(path, filepath.ToSlash(filepath.Join(prefix, rel)))
		if err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, err
}

func hashFile(path string) (string, int64, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, in)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func verifyEntry(path string, expected BackupEntry) error {
	hash, size, err := hashFile(path)
	if err != nil {
		return fmt.Errorf("verify backup entry %s: %w", expected.Path, err)
	}
	if hash != expected.ContentHash || size != expected.SizeBytes {
		return fmt.Errorf("backup integrity mismatch for %s", expected.Path)
	}
	return nil
}
