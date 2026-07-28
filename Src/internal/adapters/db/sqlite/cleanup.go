package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type CleanupReport struct {
	StartedAt          time.Time `json:"started_at"`
	DeletedTempFiles   int       `json:"deleted_temp_files"`
	DeletedOrphanBlobs int       `json:"deleted_orphan_blobs"`
	SkippedProtected   int       `json:"skipped_protected"`
	SkippedLegalHold   int       `json:"skipped_legal_hold"`
}

// Cleanup only touches names created by Oktapod itself and unreferenced blobs.
// User files, active workspace directories and any blob with a second reference
// are never candidates for deletion.
func (s *Store) Cleanup(ctx context.Context, scope core.Scope, now time.Time) (CleanupReport, error) {
	report := CleanupReport{StartedAt: now.UTC()}
	blocked, err := s.cleanupBlockedByRuntime(ctx)
	if err != nil {
		return report, err
	}
	if blocked {
		report.SkippedProtected++
		return report, nil
	}
	cutoff := now.Add(-24 * time.Hour)
	for _, root := range []string{s.workspaceRoot, s.blobRoot} {
		if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if s.pathHasLegalHold(path) {
				report.SkippedLegalHold++
				return nil
			}
			name := info.Name()
			if !strings.HasPrefix(name, ".oktapod-write-") && !strings.HasPrefix(name, ".upload-") {
				return nil
			}
			if info.ModTime().After(cutoff) {
				report.SkippedProtected++
				return nil
			}
			if err := os.Remove(path); err != nil {
				return err
			}
			report.DeletedTempFiles++
			return nil
		}); err != nil {
			return report, fmt.Errorf("cleanup temporary files: %w", err)
		}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT b.content_hash, b.storage_key
		FROM blobs b
		LEFT JOIN artifacts a ON a.content_hash = b.content_hash
		LEFT JOIN assets x ON x.content_hash = b.content_hash
		WHERE a.content_hash IS NULL AND x.content_hash IS NULL AND b.created_at < ?`, formatTime(cutoff))
	if err != nil {
		return report, err
	}
	type orphan struct{ hash, key string }
	orphans := []orphan{}
	for rows.Next() {
		var item orphan
		if err := rows.Scan(&item.hash, &item.key); err != nil {
			rows.Close()
			return report, err
		}
		orphans = append(orphans, item)
	}
	if err := rows.Close(); err != nil {
		return report, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range orphans {
		path, err := safeJoin(s.blobRoot, item.key)
		if err != nil {
			return report, err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return report, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE content_hash = ? AND NOT EXISTS (SELECT 1 FROM artifacts WHERE content_hash = ?) AND NOT EXISTS (SELECT 1 FROM assets WHERE content_hash = ?)`, item.hash, item.hash, item.hash); err != nil {
			return report, err
		}
		report.DeletedOrphanBlobs++
	}
	if len(orphans) > 0 {
		auditID, err := newID()
		if err != nil {
			return report, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, '', ?, 'cleanup.completed', 'cleanup', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, scope.UserID, auditID, fmt.Sprintf(`{"deleted_orphan_blobs":%d}`, report.DeletedOrphanBlobs), auditID, formatTime(now)); err != nil {
			return report, err
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) cleanupBlockedByRuntime(ctx context.Context) (bool, error) {
	var activeRuns, pendingApprovals int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE status NOT IN ('succeeded', 'failed', 'canceled')`).Scan(&activeRuns); err != nil {
		return false, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM approval_requests WHERE status = 'pending'`).Scan(&pendingApprovals); err != nil {
		return false, err
	}
	return activeRuns > 0 || pendingApprovals > 0, nil
}

// pathHasLegalHold supports an operator-controlled, local fail-closed fence.
// A marker at <workspace>/<project>/.oktapod-legal-hold protects that project;
// a marker at the Blob root protects all blobs. The marker itself is never a
// cleanup candidate because its name does not match a temporary-file prefix.
func (s *Store) pathHasLegalHold(path string) bool {
	if _, err := os.Stat(filepath.Join(s.blobRoot, ".oktapod-legal-hold")); err == nil {
		if rel, relErr := filepath.Rel(s.blobRoot, path); relErr == nil && rel != "." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	rel, err := filepath.Rel(s.workspaceRoot, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 0 || parts[0] == "" {
		return false
	}
	_, err = os.Stat(filepath.Join(s.workspaceRoot, parts[0], ".oktapod-legal-hold"))
	return err == nil
}

func (s *Store) CountBlobReferences(ctx context.Context, contentHash string) (int, error) {
	var artifacts, assets int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE content_hash = ?`, contentHash).Scan(&artifacts); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assets WHERE content_hash = ?`, contentHash).Scan(&assets); err != nil {
		return 0, err
	}
	return artifacts + assets, nil
}
