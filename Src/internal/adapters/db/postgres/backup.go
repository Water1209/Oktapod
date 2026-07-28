package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
)

// Backup creates a logical PostgreSQL snapshot plus immutable workspace/blob
// copies. pg_dump obtains a database-consistent snapshot; content-addressed
// blobs are immutable, so files created concurrently cannot corrupt referenced
// data. Credentials are passed to the child process through PGPASSWORD and
// never appear in command arguments or error output.
func (s *Store) Backup(ctx context.Context, targetDir string) (sqlite.BackupManifest, error) {
	return s.BackupWithDSN(ctx, targetDir, s.dsn)
}

// BackupWithDSN allows the application role and the backup role to be
// separated. When PostgreSQL FORCE ROW LEVEL SECURITY is enabled, the backup
// DSN should use a dedicated BYPASSRLS role with no application write access.
func (s *Store) BackupWithDSN(ctx context.Context, targetDir, backupDSN string) (sqlite.BackupManifest, error) {
	target, err := filepath.Abs(targetDir)
	if err != nil {
		return sqlite.BackupManifest{}, err
	}
	blobRoot := filepath.Join(filepath.Dir(s.workspaceRoot), "blobs")
	if err := prepareBackupTarget(target, s.workspaceRoot, blobRoot); err != nil {
		return sqlite.BackupManifest{}, err
	}
	databasePath := filepath.Join(target, "database.dump")
	if strings.TrimSpace(backupDSN) == "" {
		return sqlite.BackupManifest{}, fmt.Errorf("postgres backup DSN is required")
	}
	if err := runPostgresDump(ctx, backupDSN, databasePath); err != nil {
		return sqlite.BackupManifest{}, err
	}
	if err := copyTreeSafe(s.workspaceRoot, filepath.Join(target, "workspaces")); err != nil {
		return sqlite.BackupManifest{}, fmt.Errorf("copy workspaces: %w", err)
	}
	if err := copyTreeSafe(blobRoot, filepath.Join(target, "blobs")); err != nil {
		return sqlite.BackupManifest{}, fmt.Errorf("copy blobs: %w", err)
	}
	manifest := sqlite.BackupManifest{FormatVersion: 1, CreatedAt: time.Now().UTC(), DatabaseType: "postgresql"}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_versions`).Scan(&manifest.SchemaVersion); err != nil {
		return sqlite.BackupManifest{}, fmt.Errorf("read postgres schema version: %w", err)
	}
	manifest.Database, err = fileEntrySafe(databasePath, "database.dump")
	if err != nil {
		return sqlite.BackupManifest{}, err
	}
	manifest.Workspaces, err = treeEntriesSafe(filepath.Join(target, "workspaces"), "workspaces")
	if err != nil {
		return sqlite.BackupManifest{}, err
	}
	manifest.Blobs, err = treeEntriesSafe(filepath.Join(target, "blobs"), "blobs")
	if err != nil {
		return sqlite.BackupManifest{}, err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return sqlite.BackupManifest{}, err
	}
	if err := os.WriteFile(filepath.Join(target, "manifest.json"), append(data, '\n'), 0o600); err != nil {
		return sqlite.BackupManifest{}, err
	}
	return manifest, nil
}

// Restore verifies the manifest before loading a logical dump into an empty
// target database. It refuses to overwrite a non-empty workspace or database
// schema; operators must explicitly provision an isolated restore target.
func Restore(ctx context.Context, backupDir, dsn, workspaceRoot string) (sqlite.BackupManifest, error) {
	backup, err := filepath.Abs(backupDir)
	if err != nil {
		return sqlite.BackupManifest{}, err
	}
	data, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil {
		return sqlite.BackupManifest{}, fmt.Errorf("read backup manifest: %w", err)
	}
	var manifest sqlite.BackupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return sqlite.BackupManifest{}, fmt.Errorf("decode backup manifest: %w", err)
	}
	if manifest.FormatVersion != 1 || manifest.DatabaseType != "postgresql" {
		return sqlite.BackupManifest{}, fmt.Errorf("backup is not a PostgreSQL snapshot")
	}
	if manifest.SchemaVersion <= 0 || manifest.SchemaVersion > sqlite.CurrentSchemaVersion {
		return sqlite.BackupManifest{}, fmt.Errorf("backup schema %d is not supported by this binary (current=%d)", manifest.SchemaVersion, sqlite.CurrentSchemaVersion)
	}
	if err := verifyEntrySafe(filepath.Join(backup, manifest.Database.Path), manifest.Database); err != nil {
		return sqlite.BackupManifest{}, err
	}
	for _, entry := range append(append([]sqlite.BackupEntry{}, manifest.Workspaces...), manifest.Blobs...) {
		if err := verifyEntrySafe(filepath.Join(backup, entry.Path), entry); err != nil {
			return sqlite.BackupManifest{}, err
		}
	}
	if err := ensureEmptyPostgresDatabase(ctx, dsn); err != nil {
		return sqlite.BackupManifest{}, err
	}
	if err := runPostgresRestore(ctx, dsn, filepath.Join(backup, manifest.Database.Path)); err != nil {
		return sqlite.BackupManifest{}, err
	}
	blobRoot := filepath.Join(filepath.Dir(workspaceRoot), "blobs")
	if err := prepareRestoreRoot(workspaceRoot, blobRoot); err != nil {
		return sqlite.BackupManifest{}, err
	}
	if err := copyTreeSafe(filepath.Join(backup, "workspaces"), workspaceRoot); err != nil {
		return sqlite.BackupManifest{}, fmt.Errorf("restore workspaces: %w", err)
	}
	if err := copyTreeSafe(filepath.Join(backup, "blobs"), blobRoot); err != nil {
		return sqlite.BackupManifest{}, fmt.Errorf("restore blobs: %w", err)
	}
	return manifest, nil
}

func runPostgresDump(ctx context.Context, dsn, output string) error {
	args, env, err := postgresCLIArgs(dsn)
	if err != nil {
		return err
	}
	args = append([]string{"--format=custom", "--no-owner", "--no-privileges", "--file=" + output}, args...)
	return runPostgresCLI(ctx, "pg_dump", args, env)
}

func runPostgresRestore(ctx context.Context, dsn, input string) error {
	args, env, err := postgresCLIArgs(dsn)
	if err != nil {
		return err
	}
	args = append([]string{"--format=custom", "--no-owner", "--no-privileges", "--exit-on-error"}, args...)
	args = append(args, input)
	return runPostgresCLI(ctx, "pg_restore", args, env)
}

func postgresCLIArgs(dsn string) ([]string, []string, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("parse postgres DSN for backup: %w", err)
	}
	args := []string{}
	if config.Host != "" {
		args = append(args, "--host="+config.Host)
	}
	if config.Port != 0 {
		args = append(args, fmt.Sprintf("--port=%d", config.Port))
	}
	if config.User != "" {
		args = append(args, "--username="+config.User)
	}
	if config.Database != "" {
		args = append(args, "--dbname="+config.Database)
	}
	env := os.Environ()
	if config.Password != "" {
		env = append(env, "PGPASSWORD="+config.Password)
	}
	if strings.Contains(strings.ToLower(dsn), "sslmode=disable") {
		env = append(env, "PGSSLMODE=disable")
	}
	return args, env, nil
}

func runPostgresCLI(ctx context.Context, executable string, args, env []string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = env
	command.Stdout = io.Discard
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if len(message) > 1000 {
			message = message[len(message)-1000:]
		}
		if message != "" {
			return fmt.Errorf("%s failed: %s", executable, message)
		}
		return fmt.Errorf("%s failed: %w", executable, err)
	}
	return nil
}

func ensureEmptyPostgresDatabase(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect restore target: %w", err)
	}
	defer conn.Close(ctx)
	var tableCount int
	if err := conn.QueryRow(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name <> 'spatial_ref_sys'`).Scan(&tableCount); err != nil {
		return fmt.Errorf("inspect restore target: %w", err)
	}
	if tableCount != 0 {
		return fmt.Errorf("restore target database is not empty (%d public tables); use an isolated database", tableCount)
	}
	return nil
}

func prepareBackupTarget(target string, roots ...string) error {
	if err := os.MkdirAll(target, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	for _, root := range roots {
		if err := ensureOutsideSafe(target, root); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("backup target must be empty: %s", target)
	}
	return nil
}

func prepareRestoreRoot(roots ...string) error {
	for _, root := range roots {
		if info, err := os.Stat(root); err == nil {
			if !info.IsDir() {
				return fmt.Errorf("restore root is not a directory: %s", root)
			}
			entries, readErr := os.ReadDir(root)
			if readErr != nil {
				return readErr
			}
			if len(entries) != 0 {
				return fmt.Errorf("restore root must be empty: %s", root)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func ensureOutsideSafe(target string, root string) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, target)
	if err == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
		return fmt.Errorf("backup target must not be inside live data root: %s", root)
	}
	return nil
}

func copyTreeSafe(source, target string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(target, 0o700)
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sourcePath := filepath.Join(source, entry.Name())
		targetPath := filepath.Join(target, entry.Name())
		if entry.IsDir() {
			if err := copyTreeSafe(sourcePath, targetPath); err != nil {
				return err
			}
			continue
		}
		if err := copyFileSafe(sourcePath, targetPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFileSafe(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileEntrySafe(path, relative string) (sqlite.BackupEntry, error) {
	hash, size, err := hashFileSafe(path)
	if err != nil {
		return sqlite.BackupEntry{}, err
	}
	return sqlite.BackupEntry{Path: filepath.ToSlash(relative), SizeBytes: size, ContentHash: hash}, nil
}

func treeEntriesSafe(root, prefix string) ([]sqlite.BackupEntry, error) {
	entries := []sqlite.BackupEntry{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entry, err := fileEntrySafe(path, filepath.ToSlash(filepath.Join(prefix, rel)))
		if err != nil {
			return err
		}
		entries = append(entries, entry)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, err
}

func hashFileSafe(path string) (string, int64, error) {
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

func verifyEntrySafe(path string, expected sqlite.BackupEntry) error {
	hash, size, err := hashFileSafe(path)
	if err != nil {
		return fmt.Errorf("verify backup entry %s: %w", expected.Path, err)
	}
	if hash != expected.ContentHash || size != expected.SizeBytes {
		return fmt.Errorf("backup integrity mismatch for %s", expected.Path)
	}
	return nil
}
