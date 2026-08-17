package sqlite

// The project archive is deliberately a logical export rather than a copy of
// the whole SQLite file. It keeps Personal -> Team migration scoped to one
// project, rewrites ownership/scope IDs, omits idempotency records and never
// carries model credentials. The archive is deterministic enough to inspect
// and verify before import, while the database remains the source of truth.

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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const projectArchiveVersion = 1

type ProjectArchive struct {
	FormatVersion   int                         `json:"format_version"`
	ExportedAt      string                      `json:"exported_at"`
	SourceProjectID string                      `json:"source_project_id"`
	SourceOrgID     string                      `json:"source_organization_id"`
	Tables          map[string][]map[string]any `json:"tables"`
	Files           []ArchiveFile               `json:"files"`
	Blobs           []ArchiveBlob               `json:"blobs"`
	SkippedSecrets  []string                    `json:"skipped_secrets,omitempty"`
}

type ArchiveFile struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type ArchiveBlob struct {
	ContentHash string `json:"content_hash"`
	SizeBytes   int64  `json:"size_bytes"`
	MediaType   string `json:"media_type"`
}

type migrationTable struct {
	Name  string
	Query string
}

var projectArchiveTables = []migrationTable{
	{Name: "projects", Query: `SELECT * FROM projects WHERE id = ?`},
	{Name: "project_runtime_settings", Query: `SELECT * FROM project_runtime_settings WHERE project_id = ?`},
	{Name: "workspaces", Query: `SELECT * FROM workspaces WHERE project_id = ?`},
	{Name: "project_memberships", Query: `SELECT * FROM project_memberships WHERE project_id = ?`},
	{Name: "team_definitions", Query: `SELECT * FROM team_definitions WHERE project_id = ?`},
	{Name: "conversations", Query: `SELECT * FROM conversations WHERE project_id = ?`},
	{Name: "conversation_model_calls", Query: `SELECT * FROM conversation_model_calls WHERE project_id = ?`},
	{Name: "messages", Query: `SELECT * FROM messages WHERE project_id = ?`},
	{Name: "tasks", Query: `SELECT * FROM tasks WHERE project_id = ?`},
	{Name: "runs", Query: `SELECT * FROM runs WHERE project_id = ?`},
	{Name: "steps", Query: `SELECT * FROM steps WHERE project_id = ?`},
	{Name: "run_event_streams", Query: `SELECT s.* FROM run_event_streams s JOIN runs r ON r.id = s.run_id WHERE r.project_id = ?`},
	{Name: "run_events", Query: `SELECT * FROM run_events WHERE project_id = ?`},
	{Name: "model_calls", Query: `SELECT * FROM model_calls WHERE project_id = ?`},
	{Name: "usage_ledger", Query: `SELECT * FROM usage_ledger WHERE project_id = ?`},
	{Name: "outbox_events", Query: `SELECT * FROM outbox_events WHERE project_id = ?`},
	{Name: "step_attempts", Query: `SELECT * FROM step_attempts WHERE project_id = ?`},
	{Name: "checkpoints", Query: `SELECT * FROM checkpoints WHERE project_id = ?`},
	{Name: "accepted_outcomes", Query: `SELECT * FROM accepted_outcomes WHERE project_id = ?`},
	{Name: "artifacts", Query: `SELECT * FROM artifacts WHERE project_id = ?`},
	{Name: "tool_calls", Query: `SELECT * FROM tool_calls WHERE project_id = ?`},
	{Name: "approval_requests", Query: `SELECT * FROM approval_requests WHERE project_id = ?`},
	{Name: "human_action_requests", Query: `SELECT * FROM human_action_requests WHERE project_id = ?`},
	{Name: "conversation_approval_rules", Query: `SELECT * FROM conversation_approval_rules WHERE project_id = ?`},
	{Name: "project_approval_rules", Query: `SELECT * FROM project_approval_rules WHERE project_id = ?`},
	{Name: "side_effect_receipts", Query: `SELECT * FROM side_effect_receipts WHERE project_id = ?`},
	{Name: "validation_results", Query: `SELECT * FROM validation_results WHERE project_id = ?`},
	{Name: "plan_items", Query: `SELECT * FROM plan_items WHERE project_id = ?`},
	{Name: "run_plan_revisions", Query: `SELECT * FROM run_plan_revisions WHERE project_id = ?`},
	{Name: "run_plan_items", Query: `SELECT * FROM run_plan_items WHERE project_id = ?`},
	{Name: "assets", Query: `SELECT * FROM assets WHERE project_id = ?`},
	{Name: "schedules", Query: `SELECT * FROM schedules WHERE project_id = ?`},
	{Name: "schedule_fires", Query: `SELECT * FROM schedule_fires WHERE project_id = ?`},
	{Name: "capabilities", Query: `SELECT * FROM capabilities WHERE project_id = ? AND scope_type = 'project'`},
	{Name: "capability_reviews", Query: `SELECT * FROM capability_reviews WHERE project_id = ?`},
	{Name: "capability_install_jobs", Query: `SELECT * FROM capability_install_jobs WHERE project_id = ? AND scope_type = 'project'`},
	{Name: "memories", Query: `SELECT * FROM memories WHERE project_id = ?`},
	{Name: "run_budgets", Query: `SELECT b.* FROM run_budgets b JOIN runs r ON r.id = b.run_id WHERE r.project_id = ?`},
	{Name: "agent_definitions", Query: `SELECT * FROM agent_definitions WHERE project_id = ?`},
	{Name: "agent_team_members", Query: `SELECT * FROM agent_team_members WHERE project_id = ?`},
	{Name: "agent_team_streams", Query: `SELECT s.* FROM agent_team_streams s JOIN runs r ON r.id = s.run_id WHERE r.project_id = ?`},
	{Name: "agent_team_messages", Query: `SELECT * FROM agent_team_messages WHERE project_id = ?`},
	{Name: "advisor_reports", Query: `SELECT * FROM advisor_reports WHERE project_id = ?`},
	{Name: "audit_events", Query: `SELECT * FROM audit_events WHERE project_id = ?`},
}

// ExportProject writes a self-contained, inspectable project archive. Secret-
// looking workspace files are intentionally skipped and listed in the
// manifest; importing never silently copies them.
func (s *Store) ExportProject(ctx context.Context, scope core.Scope, projectID, outputDir string) (ProjectArchive, error) {
	if strings.TrimSpace(outputDir) == "" {
		return ProjectArchive{}, fmt.Errorf("migration export output is required")
	}
	if _, err := s.GetProject(ctx, scope, projectID); err != nil {
		return ProjectArchive{}, fmt.Errorf("authorize project export: %w", err)
	}
	if err := prepareEmptyDir(outputDir); err != nil {
		return ProjectArchive{}, err
	}

	archive := ProjectArchive{
		FormatVersion:   projectArchiveVersion,
		ExportedAt:      formatTime(time.Now()),
		SourceProjectID: projectID,
		SourceOrgID:     scope.OrganizationID,
		Tables:          make(map[string][]map[string]any),
	}
	for _, table := range projectArchiveTables {
		rows, err := queryArchiveRows(ctx, s.db, table.Query, projectID)
		if err != nil {
			return ProjectArchive{}, fmt.Errorf("export %s: %w", table.Name, err)
		}
		if table.Name == "capabilities" {
			for _, row := range rows {
				row["credential_ref"] = ""
				if migrationStringValue(row["kind"]) == core.CapabilityKindMCP {
					row["status"] = core.CapabilityStatusDisabled
				}
			}
		}
		archive.Tables[table.Name] = rows
	}
	users, err := queryArchiveRows(ctx, s.db, `
		SELECT DISTINCT u.* FROM users u
		WHERE u.id = (SELECT owner_user_id FROM projects WHERE id = ?)
		   OR u.id IN (SELECT user_id FROM project_memberships WHERE project_id = ?)
	`, projectID, projectID)
	if err != nil {
		return ProjectArchive{}, fmt.Errorf("export users: %w", err)
	}
	archive.Tables["users"] = users
	memberships, err := queryArchiveRows(ctx, s.db, `
		SELECT m.* FROM memberships m
		WHERE m.organization_id = (SELECT organization_id FROM projects WHERE id = ?)
		  AND m.user_id IN (SELECT user_id FROM project_memberships WHERE project_id = ?)
	`, projectID, projectID)
	if err != nil {
		return ProjectArchive{}, fmt.Errorf("export memberships: %w", err)
	}
	archive.Tables["memberships"] = memberships

	workspacePath, err := safeJoin(s.workspaceRoot, projectID)
	if err != nil {
		return ProjectArchive{}, err
	}
	if err := filepath.Walk(workspacePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(workspacePath, path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if isRuntimeCachePath(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if isSecretMigrationPath(rel) {
			archive.SkippedSecrets = append(archive.SkippedSecrets, filepath.ToSlash(rel))
			return nil
		}
		destination := filepath.Join(outputDir, "workspace", rel)
		if err := copyVerifiedFile(path, destination); err != nil {
			return err
		}
		sum, size, err := migrationHashFile(path)
		if err != nil {
			return err
		}
		archive.Files = append(archive.Files, ArchiveFile{Path: filepath.ToSlash(filepath.Join("workspace", rel)), SHA256: sum, SizeBytes: size})
		return nil
	}); err != nil {
		return ProjectArchive{}, fmt.Errorf("export workspace: %w", err)
	}

	// Only artifacts and file assets reference the blob store. Other tables
	// (capabilities, checkpoints, agent messages) also expose a content_hash,
	// but those values are integrity digests rather than blob foreign keys.
	blobHashes := make(map[string]struct{})
	for _, tableName := range []string{"artifacts", "assets"} {
		rows := archive.Tables[tableName]
		for _, row := range rows {
			if value, ok := row["content_hash"].(string); ok && strings.HasPrefix(value, "sha256:") {
				blobHashes[value] = struct{}{}
			}
		}
	}
	for hash := range blobHashes {
		storageKey := strings.TrimPrefix(hash, "sha256:")
		blobPath, err := safeJoin(s.blobRoot, filepath.Join("sha256", storageKey[:2], storageKey))
		if err != nil {
			return ProjectArchive{}, err
		}
		if _, err := os.Stat(blobPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return ProjectArchive{}, fmt.Errorf("blob %s is missing", hash)
			}
			return ProjectArchive{}, err
		}
		destination := filepath.Join(outputDir, "blobs", "sha256", storageKey[:2], storageKey)
		if err := copyVerifiedFile(blobPath, destination); err != nil {
			return ProjectArchive{}, err
		}
		sum, size, err := migrationHashFile(blobPath)
		if err != nil {
			return ProjectArchive{}, err
		}
		archive.Blobs = append(archive.Blobs, ArchiveBlob{ContentHash: hash, SizeBytes: size})
		if sum != storageKey {
			return ProjectArchive{}, fmt.Errorf("blob %s hash mismatch", hash)
		}
	}
	sort.Slice(archive.Files, func(i, j int) bool { return archive.Files[i].Path < archive.Files[j].Path })
	sort.Slice(archive.Blobs, func(i, j int) bool { return archive.Blobs[i].ContentHash < archive.Blobs[j].ContentHash })
	sort.Strings(archive.SkippedSecrets)
	manifest, err := json.MarshalIndent(archive, "", "  ")
	if err != nil {
		return ProjectArchive{}, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "manifest.json"), manifest, 0o600); err != nil {
		return ProjectArchive{}, err
	}
	return archive, nil
}

// ImportProject imports a project into the caller's organization. The source
// project and all dependent IDs are remapped; source users are recreated and
// the caller becomes the sole owner. Other members retain their role when
// their identity can be safely recreated. No SecretRef value is resolved.
func (s *Store) ImportProject(ctx context.Context, scope core.Scope, inputDir string) (core.Project, ProjectArchive, error) {
	manifestPath := filepath.Join(inputDir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return core.Project{}, ProjectArchive{}, fmt.Errorf("read migration manifest: %w", err)
	}
	var archive ProjectArchive
	if err := json.Unmarshal(data, &archive); err != nil {
		return core.Project{}, ProjectArchive{}, fmt.Errorf("parse migration manifest: %w", err)
	}
	if archive.FormatVersion != projectArchiveVersion || archive.SourceProjectID == "" {
		return core.Project{}, ProjectArchive{}, fmt.Errorf("unsupported migration archive format %d", archive.FormatVersion)
	}
	if err := verifyArchiveFiles(inputDir, archive); err != nil {
		return core.Project{}, ProjectArchive{}, err
	}
	projectRows := archive.Tables["projects"]
	if len(projectRows) != 1 {
		return core.Project{}, ProjectArchive{}, fmt.Errorf("migration archive must contain exactly one project")
	}
	if err := s.ensureImportIDsFree(ctx, archive); err != nil {
		return core.Project{}, ProjectArchive{}, err
	}

	projectID, err := newID()
	if err != nil {
		return core.Project{}, ProjectArchive{}, err
	}
	workspacePath, err := safeJoin(s.workspaceRoot, projectID)
	if err != nil {
		return core.Project{}, ProjectArchive{}, err
	}
	temporaryWorkspace := workspacePath + ".oktapod-migrate-" + uuid.NewString()
	if err := copyArchiveTree(filepath.Join(inputDir, "workspace"), temporaryWorkspace); err != nil {
		return core.Project{}, ProjectArchive{}, fmt.Errorf("stage migration workspace: %w", err)
	}
	defer os.RemoveAll(temporaryWorkspace)

	userMap := map[string]string{}
	ownerSourceID, _ := projectRows[0]["owner_user_id"].(string)
	userMap[ownerSourceID] = scope.UserID
	for _, row := range archive.Tables["users"] {
		oldID, _ := row["id"].(string)
		if oldID == "" || oldID == ownerSourceID {
			continue
		}
		newUser, err := newID()
		if err != nil {
			return core.Project{}, ProjectArchive{}, err
		}
		userMap[oldID] = newUser
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Project{}, ProjectArchive{}, err
	}
	rollback := func(e error) (core.Project, ProjectArchive, error) {
		_ = tx.Rollback()
		return core.Project{}, ProjectArchive{}, e
	}
	now := formatTime(time.Now())
	for _, row := range archive.Tables["users"] {
		oldID, _ := row["id"].(string)
		newIDValue := userMap[oldID]
		if newIDValue == "" || newIDValue == scope.UserID {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users(id, display_name, email, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, newIDValue, migrationStringValue(row["display_name"]), migrationStringValue(row["email"]), migrationStringValue(row["created_at"]), migrationStringValue(row["updated_at"])); err != nil {
			return rollback(fmt.Errorf("import user %s: %w", oldID, err))
		}
	}
	// The project is deliberately created as Team even when exported from a
	// Personal profile. The immutable workspace key follows the new ID.
	project := projectRows[0]
	project["id"] = projectID
	project["organization_id"] = scope.OrganizationID
	project["owner_user_id"] = scope.UserID
	project["kind"] = core.ProjectKindTeam
	project["updated_at"] = now
	if err := insertArchiveRow(ctx, tx, "projects", project, nil, nil, nil, projectID); err != nil {
		return rollback(fmt.Errorf("import project: %w", err))
	}
	workspaceRows := archive.Tables["workspaces"]
	if len(workspaceRows) > 0 {
		workspace := workspaceRows[0]
		workspace["project_id"] = projectID
		workspace["workspace_key"] = projectID
		workspace["relative_path"] = projectID
		if err := insertArchiveRow(ctx, tx, "workspaces", workspace, nil, nil, nil, projectID); err != nil {
			return rollback(fmt.Errorf("import workspace: %w", err))
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO memberships(organization_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'owner', 'active', ?, ?)`, scope.OrganizationID, scope.UserID, now, now); err != nil {
		return rollback(fmt.Errorf("import owner membership: %w", err))
	}
	// Import source membership rows before project memberships so all foreign
	// keys exist. Any source owner is constrained to the target caller.
	for _, row := range archive.Tables["memberships"] {
		oldUser := migrationStringValue(row["user_id"])
		mapped := userMap[oldUser]
		if mapped == "" {
			continue
		}
		role := migrationStringValue(row["role"])
		if mapped == scope.UserID {
			role = core.ProjectRoleOwner
		}
		if role != core.ProjectRoleOwner && role != core.ProjectRoleMember && role != core.ProjectRoleApprover {
			role = core.ProjectRoleMember
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO memberships(organization_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`, scope.OrganizationID, mapped, role, migrationStringValue(row["status"]), migrationStringValue(row["created_at"]), migrationStringValue(row["updated_at"])); err != nil {
			return rollback(fmt.Errorf("import membership: %w", err))
		}
	}

	// Import dependent rows in migration/foreign-key order. Existing IDs are
	// rejected up front, so INSERT OR IGNORE cannot hide data loss.
	for _, table := range projectArchiveTables {
		if table.Name == "projects" || table.Name == "workspaces" || table.Name == "project_memberships" || table.Name == "audit_events" {
			continue
		}
		for _, source := range archive.Tables[table.Name] {
			row := cloneRow(source)
			for key := range row {
				switch key {
				case "organization_id":
					row[key] = scope.OrganizationID
				case "project_id":
					row[key] = projectID
				case "owner_user_id", "user_id", "actor_user_id", "accepted_by", "requested_by", "decided_by":
					if mapped := userMap[migrationStringValue(row[key])]; mapped != "" {
						row[key] = mapped
					}
				case "scope_id":
					if migrationStringValue(row["scope_type"]) == core.CapabilityScopeProject {
						row[key] = projectID
					}
				}
			}
			if err := insertArchiveRow(ctx, tx, table.Name, row, nil, nil, nil, projectID); err != nil {
				return rollback(fmt.Errorf("import %s: %w", table.Name, err))
			}
		}
	}
	for _, source := range archive.Tables["project_memberships"] {
		row := cloneRow(source)
		row["project_id"] = projectID
		if mapped := userMap[migrationStringValue(row["user_id"])]; mapped != "" {
			row["user_id"] = mapped
		}
		if migrationStringValue(row["user_id"]) == scope.UserID {
			row["role"] = core.ProjectRoleOwner
		}
		if err := insertArchiveRow(ctx, tx, "project_memberships", row, nil, nil, nil, projectID); err != nil {
			return rollback(fmt.Errorf("import project membership: %w", err))
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'project.migrated', 'project', ?, 'succeeded', ?, ?, ?)`, uuid.NewString(), scope.OrganizationID, projectID, scope.UserID, projectID, `{"source_project_id":"`+archive.SourceProjectID+`","secrets_skipped":`+fmt.Sprintf("%d", len(archive.SkippedSecrets))+`}`, uuid.NewString(), now); err != nil {
		return rollback(fmt.Errorf("import migration audit: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return core.Project{}, ProjectArchive{}, fmt.Errorf("commit migration: %w", err)
	}
	if err := os.Rename(temporaryWorkspace, workspacePath); err != nil {
		return core.Project{}, ProjectArchive{}, fmt.Errorf("activate migrated workspace: %w", err)
	}
	for _, blob := range archive.Blobs {
		storageKey := strings.TrimPrefix(blob.ContentHash, "sha256:")
		destination, err := safeJoin(s.blobRoot, filepath.Join("sha256", storageKey[:2], storageKey))
		if err != nil {
			return core.Project{}, ProjectArchive{}, err
		}
		if _, err := os.Stat(destination); err == nil {
			continue
		}
		source := filepath.Join(inputDir, "blobs", "sha256", storageKey[:2], storageKey)
		if err := copyVerifiedFile(source, destination); err != nil {
			return core.Project{}, ProjectArchive{}, err
		}
	}
	imported, err := s.GetProject(ctx, scope, projectID)
	if err != nil {
		return core.Project{}, ProjectArchive{}, err
	}
	return imported, archive, nil
}

func (s *Store) ensureImportIDsFree(ctx context.Context, archive ProjectArchive) error {
	for _, table := range projectArchiveTables {
		for _, row := range archive.Tables[table.Name] {
			id, ok := row["id"].(string)
			if !ok || id == "" {
				continue
			}
			var exists int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table.Name+` WHERE id = ?`, id).Scan(&exists); err != nil {
				return fmt.Errorf("check import conflict %s/%s: %w", table.Name, id, err)
			}
			if exists > 0 {
				return fmt.Errorf("migration id collision in %s: %s", table.Name, id)
			}
		}
	}
	return nil
}

func insertArchiveRow(ctx context.Context, tx *sql.Tx, table string, row map[string]any, _ map[string]string, _ map[string]string, _ map[string]string, _ string) error {
	columns := make([]string, 0, len(row))
	for key := range row {
		columns = append(columns, key)
	}
	sort.Strings(columns)
	placeholders := make([]string, len(columns))
	args := make([]any, len(columns))
	for i, column := range columns {
		placeholders[i] = "?"
		args[i] = normalizeSQLiteValue(row[column])
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO `+table+` (`+strings.Join(columns, ", ")+") VALUES ("+strings.Join(placeholders, ", ")+")", args...)
	return err
}

func queryArchiveRows(ctx context.Context, db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = normalizeSQLiteValue(values[i])
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func normalizeSQLiteValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	default:
		return typed
	}
}

func cloneRow(source map[string]any) map[string]any {
	copy := make(map[string]any, len(source))
	for key, value := range source {
		copy[key] = value
	}
	return copy
}

func migrationStringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func prepareEmptyDir(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(path, 0o700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("migration output is not a directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return fmt.Errorf("migration output directory must be empty: %s", path)
	}
	return nil
}

func copyVerifiedFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(destination)
		return err
	}
	return out.Close()
}

func migrationHashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func verifyArchiveFiles(inputDir string, archive ProjectArchive) error {
	for _, file := range archive.Files {
		path, err := safeJoin(inputDir, file.Path)
		if err != nil {
			return err
		}
		sum, size, err := migrationHashFile(path)
		if err != nil || sum != file.SHA256 || size != file.SizeBytes {
			return fmt.Errorf("migration file integrity check failed: %s", file.Path)
		}
	}
	for _, blob := range archive.Blobs {
		storageKey := strings.TrimPrefix(blob.ContentHash, "sha256:")
		if len(storageKey) < 2 {
			return fmt.Errorf("invalid blob hash %q", blob.ContentHash)
		}
		path, err := safeJoin(inputDir, filepath.Join("blobs", "sha256", storageKey[:2], storageKey))
		if err != nil {
			return err
		}
		sum, size, err := migrationHashFile(path)
		if err != nil || sum != storageKey || size != blob.SizeBytes {
			return fmt.Errorf("migration blob integrity check failed: %s", blob.ContentHash)
		}
	}
	return nil
}

func copyArchiveTree(source, destination string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(destination, 0o700)
	} else if err != nil {
		return err
	}
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyVerifiedFile(path, target)
	})
}

func isSecretMigrationPath(path string) bool {
	clean := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(clean))
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.Contains(base, "secret") || strings.Contains(base, "credential") || strings.Contains(base, "token") {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return strings.Contains(clean, "/.git/")
}
