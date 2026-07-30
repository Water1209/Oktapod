package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"

	"github.com/oktapod/oktapod/Src/db/migrations/sqlite"
	localblob "github.com/oktapod/oktapod/Src/internal/adapters/blob/localfs"
	sqlitegen "github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite/sqlc"
	mcpadapter "github.com/oktapod/oktapod/Src/internal/adapters/mcp"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type Store struct {
	db               *sql.DB
	queries          *sqlitegen.Queries
	workspaceRoot    string
	blobRoot         string
	blobStore        application.BlobStore
	executionPolicy  application.ExecutionPolicy
	schedulerOwnerID string
	idempotencyMu    sync.Mutex
	identityMu       sync.Mutex
}

func Open(ctx context.Context, databasePath, workspaceRoot string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		return nil, fmt.Errorf("create data root: %w", err)
	}
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	blobRoot := filepath.Join(filepath.Dir(workspaceRoot), "blobs")
	if err := os.MkdirAll(blobRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create blob root: %w", err)
	}

	dsn := "file:" + filepath.ToSlash(databasePath) + "?_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	store, err := NewExternalStore(db, workspaceRoot)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// NewExternalStore wires the repository to an already-open database/sql
// connection. The PostgreSQL profile uses this constructor after adapting
// placeholder syntax and applying its own migrations; Personal Open keeps the
// SQLite-specific WAL/migration path above.
func NewExternalStore(db *sql.DB, workspaceRoot string) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("database connection is required")
	}
	absoluteWorkspace, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(absoluteWorkspace, 0o700); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	blobRoot := filepath.Join(filepath.Dir(absoluteWorkspace), "blobs")
	if err := os.MkdirAll(blobRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create blob root: %w", err)
	}
	ownerID, err := newID()
	if err != nil {
		return nil, err
	}
	blobStore, err := localblob.New(blobRoot)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, queries: sqlitegen.New(db), workspaceRoot: absoluteWorkspace, blobRoot: blobRoot, blobStore: blobStore, executionPolicy: application.StaticExecutionPolicy{}, schedulerOwnerID: ownerID}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) GetActor(ctx context.Context, scope core.Scope) (core.Actor, error) {
	var actor core.Actor
	if err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.display_name
		FROM users u JOIN memberships m ON m.user_id = u.id
		WHERE u.id = ? AND m.organization_id = ? AND m.status = 'active'
	`, scope.UserID, scope.OrganizationID).Scan(&actor.ID, &actor.Username, &actor.DisplayName); err != nil {
		return core.Actor{}, err
	}
	return actor, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_versions (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("initialize migration ledger: %w", err)
	}
	entries, err := fs.ReadDir(sqlitemigrations.FS, ".")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		prefix := strings.SplitN(name, "_", 2)[0]
		version, parseErr := strconv.ParseInt(prefix, 10, 64)
		if parseErr != nil || version <= 0 {
			return fmt.Errorf("invalid migration filename %s", name)
		}
		var alreadyApplied int
		if queryErr := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_versions WHERE version = ?`, version).Scan(&alreadyApplied); queryErr != nil {
			return fmt.Errorf("read migration version %d: %w", version, queryErr)
		}
		if alreadyApplied > 0 {
			continue
		}
		body, readErr := fs.ReadFile(sqlitemigrations.FS, name)
		if readErr != nil {
			return fmt.Errorf("read migration %s: %w", name, readErr)
		}
		tx, beginErr := s.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return fmt.Errorf("begin migration %s: %w", name, beginErr)
		}
		if _, execErr := tx.ExecContext(ctx, string(body)); execErr != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, execErr)
		}
		if _, ledgerErr := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_versions(version, applied_at) VALUES (?, ?)`, version, formatTime(time.Now())); ledgerErr != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, ledgerErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("commit migration %s: %w", name, commitErr)
		}
	}
	return nil
}

func (s *Store) Bootstrap(ctx context.Context, actor core.Actor, organization core.Organization) error {
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if err := q.UpsertOrganization(ctx, sqlitegen.UpsertOrganizationParams{ID: organization.ID, Name: organization.Name, CreatedAt: now, UpdatedAt: now}); err != nil {
		return fmt.Errorf("bootstrap organization: %w", err)
	}
	if err := q.UpsertUser(ctx, sqlitegen.UpsertUserParams{ID: actor.ID, DisplayName: actor.DisplayName, CreatedAt: now, UpdatedAt: now}); err != nil {
		return fmt.Errorf("bootstrap user: %w", err)
	}
	if err := q.UpsertMembership(ctx, sqlitegen.UpsertMembershipParams{
		OrganizationID: organization.ID,
		UserID:         actor.ID,
		Role:           "owner",
		Status:         "active",
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		return fmt.Errorf("bootstrap membership: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_context_settings (organization_id, owner_user_id, auto_compact, trigger_tokens, retain_tokens, summary_max_tokens, updated_at) VALUES (?, ?, 1, 100000, 20000, 4096, ?)`, organization.ID, actor.ID, now); err != nil {
		return fmt.Errorf("bootstrap conversation context settings: %w", err)
	}
	return tx.Commit()
}

func (s *Store) CreateAuthSession(ctx context.Context, organizationID string, input core.CreateAuthSessionInput) (core.AuthSession, error) {
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(input.UserID) == "" {
		return core.AuthSession{}, fmt.Errorf("organization and user are required")
	}
	var active int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE organization_id = ? AND user_id = ? AND status = 'active'`, organizationID, input.UserID).Scan(&active); err != nil {
		return core.AuthSession{}, err
	}
	if active == 0 {
		return core.AuthSession{}, fmt.Errorf("user is not an active organization member")
	}
	var displayName string
	if err := s.db.QueryRowContext(ctx, `SELECT display_name FROM users WHERE id = ?`, input.UserID).Scan(&displayName); err != nil {
		return core.AuthSession{}, err
	}
	if input.ExpiresInSeconds <= 0 {
		input.ExpiresInSeconds = 8 * 60 * 60
	}
	if input.ExpiresInSeconds > 30*24*60*60 {
		input.ExpiresInSeconds = 30 * 24 * 60 * 60
	}
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return core.AuthSession{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(rawToken)
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	sessionID, err := newID()
	if err != nil {
		return core.AuthSession{}, err
	}
	now := time.Now()
	expires := now.Add(time.Duration(input.ExpiresInSeconds) * time.Second)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(id, organization_id, user_id, token_hash, expires_at, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, organizationID, input.UserID, tokenHash, formatTime(expires), formatTime(now), formatTime(now)); err != nil {
		return core.AuthSession{}, err
	}
	return core.AuthSession{ID: sessionID, OrganizationID: organizationID, UserID: input.UserID, DisplayName: displayName, Token: token, ExpiresAt: expires, CreatedAt: now, LastSeenAt: now}, nil
}

func (s *Store) ResolveAuthSession(ctx context.Context, token string) (core.AuthSession, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return core.AuthSession{}, sql.ErrNoRows
	}
	sum := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(sum[:])
	now := formatTime(time.Now())
	var session core.AuthSession
	var expiresAt, createdAt, lastSeenAt string
	if err := s.db.QueryRowContext(ctx, `
		SELECT s.id, s.organization_id, s.user_id, u.display_name, s.expires_at, s.created_at, s.last_seen_at
		FROM auth_sessions s
		JOIN users u ON u.id = s.user_id
		JOIN memberships m ON m.organization_id = s.organization_id AND m.user_id = s.user_id AND m.status = 'active'
		WHERE s.token_hash = ? AND s.revoked_at = '' AND s.expires_at > ?
	`, tokenHash, now).Scan(&session.ID, &session.OrganizationID, &session.UserID, &session.DisplayName, &expiresAt, &createdAt, &lastSeenAt); err != nil {
		return core.AuthSession{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET last_seen_at = ? WHERE id = ?`, now, session.ID); err != nil {
		return core.AuthSession{}, err
	}
	var err error
	session.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return core.AuthSession{}, err
	}
	session.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return core.AuthSession{}, err
	}
	session.LastSeenAt, err = parseTime(lastSeenAt)
	if err != nil {
		return core.AuthSession{}, err
	}
	return session, nil
}

func (s *Store) RevokeAuthSession(ctx context.Context, sessionID, organizationID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = ? WHERE id = ? AND organization_id = ? AND revoked_at = ''`, formatTime(time.Now()), sessionID, organizationID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateProject(ctx context.Context, scope core.Scope, input core.CreateProjectInput, idempotency core.Idempotency) (core.Project, error) {
	const route = "POST /projects"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Project
		found, replayErr := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if replayErr != nil {
			return core.Project{}, replayErr
		}
		if found {
			return replay, nil
		}
	}
	projectID, err := newID()
	if err != nil {
		return core.Project{}, err
	}
	relativePath := filepath.Join(projectID)
	workspacePath, err := safeJoin(s.workspaceRoot, relativePath)
	if err != nil {
		return core.Project{}, err
	}
	if err := os.MkdirAll(workspacePath, 0o700); err != nil {
		return core.Project{}, fmt.Errorf("create workspace: %w", err)
	}

	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Project{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.Project
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil {
			_ = os.Remove(workspacePath)
			return core.Project{}, replayErr
		}
		if found {
			_ = os.Remove(workspacePath)
			return replay, nil
		}
	}
	row, err := q.CreateProject(ctx, sqlitegen.CreateProjectParams{
		ID:             projectID,
		OrganizationID: scope.OrganizationID,
		OwnerUserID:    scope.UserID,
		Name:           input.Name,
		Description:    input.Description,
		Kind:           input.Kind,
		Scene:          input.Scene,
		DefaultModel:   input.DefaultModel,
		FallbackModel:  input.FallbackModel,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		return core.Project{}, fmt.Errorf("create project: %w", err)
	}
	if err := q.CreateWorkspace(ctx, sqlitegen.CreateWorkspaceParams{ProjectID: projectID, WorkspaceKey: projectID, RelativePath: relativePath, CreatedAt: now}); err != nil {
		return core.Project{}, fmt.Errorf("create workspace record: %w", err)
	}
	if err := q.UpsertProjectMembership(ctx, sqlitegen.UpsertProjectMembershipParams{
		ProjectID: projectID,
		UserID:    scope.UserID,
		Role:      "owner",
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return core.Project{}, fmt.Errorf("create project membership: %w", err)
	}
	project, err := projectFromRow(row)
	if err != nil {
		_ = os.Remove(workspacePath)
		return core.Project{}, err
	}
	project, err = projectWithRuntimeSettings(ctx, tx, project)
	if err != nil {
		_ = os.Remove(workspacePath)
		return core.Project{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, project); err != nil {
		_ = os.Remove(workspacePath)
		return core.Project{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = os.Remove(workspacePath)
		return core.Project{}, err
	}
	return project, nil
}

func (s *Store) GetProject(ctx context.Context, scope core.Scope, projectID string) (core.Project, error) {
	row, err := s.queries.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID})
	if err != nil {
		return core.Project{}, err
	}
	project, err := projectFromRow(row)
	if err != nil {
		return core.Project{}, err
	}
	return projectWithRuntimeSettings(ctx, s.db, project)
}

func (s *Store) UpdateProjectSettings(ctx context.Context, scope core.Scope, projectID string, input core.UpdateProjectSettingsInput, idempotency core.Idempotency) (core.Project, error) {
	route := "PATCH /projects/" + projectID
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Project
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Project{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.Project
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	var row sqlitegen.Project
	if err := tx.QueryRowContext(ctx, `
		SELECT p.id, p.organization_id, p.owner_user_id, p.name, p.description, p.kind, p.scene, p.status,
		       p.created_at, p.updated_at, p.default_model, p.fallback_model
		FROM projects p
		JOIN project_memberships pm ON pm.project_id = p.id AND pm.user_id = ? AND pm.status = 'active'
		WHERE p.id = ? AND p.organization_id = ? AND p.status = 'active' AND pm.role IN ('owner', 'approver')`,
		scope.UserID, projectID, scope.OrganizationID,
	).Scan(&row.ID, &row.OrganizationID, &row.OwnerUserID, &row.Name, &row.Description, &row.Kind, &row.Scene, &row.Status, &row.CreatedAt, &row.UpdatedAt, &row.DefaultModel, &row.FallbackModel); err != nil {
		return core.Project{}, err
	}
	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE projects SET default_model = ?, fallback_model = ?, updated_at = ? WHERE id = ? AND organization_id = ?`, *input.DefaultModel, *input.FallbackModel, now, projectID, scope.OrganizationID); err != nil {
		return core.Project{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO project_runtime_settings (project_id, max_model_calls, max_tokens, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			max_model_calls = excluded.max_model_calls,
			max_tokens = excluded.max_tokens,
			updated_at = excluded.updated_at`,
		projectID, *input.MaxModelCalls, *input.MaxTokens, now, now); err != nil {
		return core.Project{}, err
	}
	row.DefaultModel, row.FallbackModel, row.UpdatedAt = *input.DefaultModel, *input.FallbackModel, now
	project, err := projectFromRow(row)
	if err != nil {
		return core.Project{}, err
	}
	auditID, err := newID()
	if err != nil {
		return core.Project{}, err
	}
	project.MaxModelCalls, project.MaxTokens = *input.MaxModelCalls, *input.MaxTokens
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'project.settings_updated', 'project', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, projectID, scope.UserID, projectID, fmt.Sprintf(`{"default_model":%q,"fallback_model":%q,"max_model_calls":%d,"max_tokens":%d}`, project.DefaultModel, project.FallbackModel, project.MaxModelCalls, project.MaxTokens), auditID, now); err != nil {
		return core.Project{}, err
	}
	if idempotency.Key != "" {
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, project); err != nil {
			return core.Project{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return core.Project{}, err
	}
	return project, nil
}

func (s *Store) ListProjects(ctx context.Context, scope core.Scope, limit int) ([]core.Project, error) {
	rows, err := s.queries.ListProjectsForUser(ctx, sqlitegen.ListProjectsForUserParams{OrganizationID: scope.OrganizationID, UserID: scope.UserID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	projects := make([]core.Project, 0, len(rows))
	for _, row := range rows {
		project, convertErr := projectFromRow(row)
		if convertErr != nil {
			return nil, convertErr
		}
		project, convertErr = projectWithRuntimeSettings(ctx, s.db, project)
		if convertErr != nil {
			return nil, convertErr
		}
		projects = append(projects, project)
	}
	return projects, nil
}

func (s *Store) AddProjectMember(ctx context.Context, scope core.Scope, projectID string, input core.AddProjectMemberInput, correlationID string, idempotency core.Idempotency) (core.ProjectMember, error) {
	route := "POST /projects/" + projectID + "/members"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.ProjectMember
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ProjectMember{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.ProjectMember
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	project, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID})
	if err != nil {
		return core.ProjectMember{}, err
	}
	if project.Kind != core.ProjectKindTeam {
		return core.ProjectMember{}, &core.ValidationError{Field: "project_id", Message: "members can only be managed on a team project"}
	}
	requester, err := q.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return core.ProjectMember{}, err
	}
	if requester.Role != core.ProjectRoleOwner {
		return core.ProjectMember{}, sql.ErrNoRows
	}
	now := formatTime(time.Now())
	target, err := q.FindUserByEmail(ctx, input.Email)
	if errors.Is(err, sql.ErrNoRows) {
		userID, idErr := newID()
		if idErr != nil {
			return core.ProjectMember{}, idErr
		}
		target, err = q.CreateInvitedUser(ctx, sqlitegen.CreateInvitedUserParams{ID: userID, DisplayName: input.DisplayName, Email: input.Email, CreatedAt: now, UpdatedAt: now})
	}
	if err != nil {
		return core.ProjectMember{}, err
	}
	current, currentErr := q.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: target.ID, OrganizationID: scope.OrganizationID})
	if currentErr == nil && current.Role == core.ProjectRoleOwner && input.Role != core.ProjectRoleOwner {
		owners, countErr := q.CountActiveProjectOwners(ctx, projectID)
		if countErr != nil {
			return core.ProjectMember{}, countErr
		}
		if owners <= 1 {
			return core.ProjectMember{}, &core.ResourceConflictError{Resource: "project membership", Reason: "the last owner cannot be demoted"}
		}
	} else if currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows) {
		return core.ProjectMember{}, currentErr
	}
	if err := q.ActivateOrganizationMembership(ctx, sqlitegen.ActivateOrganizationMembershipParams{OrganizationID: scope.OrganizationID, UserID: target.ID, CreatedAt: now, UpdatedAt: now}); err != nil {
		return core.ProjectMember{}, err
	}
	if err := q.UpsertProjectMembership(ctx, sqlitegen.UpsertProjectMembershipParams{ProjectID: projectID, UserID: target.ID, Role: input.Role, Status: "active", CreatedAt: now, UpdatedAt: now}); err != nil {
		return core.ProjectMember{}, err
	}
	row, err := q.GetProjectMemberForUser(ctx, sqlitegen.GetProjectMemberForUserParams{ProjectID: projectID, UserID: target.ID, OrganizationID: scope.OrganizationID, UserID_2: scope.UserID})
	if err != nil {
		return core.ProjectMember{}, err
	}
	member, err := projectMemberFromFields(row.UserID, row.DisplayName, row.Email, row.Role, row.Status, row.CreatedAt, row.UpdatedAt)
	if err != nil {
		return core.ProjectMember{}, err
	}
	detail, _ := json.Marshal(map[string]any{"role": member.Role})
	auditID, err := newID()
	if err != nil {
		return core.ProjectMember{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: auditID, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "project.member_upserted", TargetType: "user", TargetID: member.UserID, Outcome: "succeeded", DetailJson: string(detail), CorrelationID: correlationID, OccurredAt: now}); err != nil {
		return core.ProjectMember{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, member); err != nil {
		return core.ProjectMember{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ProjectMember{}, err
	}
	return member, nil
}

func (s *Store) ListProjectMembers(ctx context.Context, scope core.Scope, projectID string) ([]core.ProjectMember, error) {
	rows, err := s.queries.ListProjectMembersForUser(ctx, sqlitegen.ListProjectMembersForUserParams{ProjectID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID})
	if err != nil {
		return nil, err
	}
	members := make([]core.ProjectMember, 0, len(rows))
	for _, row := range rows {
		member, convertErr := projectMemberFromFields(row.UserID, row.DisplayName, row.Email, row.Role, row.Status, row.CreatedAt, row.UpdatedAt)
		if convertErr != nil {
			return nil, convertErr
		}
		members = append(members, member)
	}
	return members, nil
}

func (s *Store) RemoveProjectMember(ctx context.Context, scope core.Scope, projectID, userID, correlationID string, idempotency core.Idempotency) error {
	route := "DELETE /projects/" + projectID + "/members/" + userID
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay struct{ Removed bool }
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay struct{ Removed bool }
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replayErr
		}
	}
	requester, err := q.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil || requester.Role != core.ProjectRoleOwner {
		return sql.ErrNoRows
	}
	target, err := q.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: userID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return err
	}
	if target.Role == core.ProjectRoleOwner {
		owners, countErr := q.CountActiveProjectOwners(ctx, projectID)
		if countErr != nil {
			return countErr
		}
		if owners <= 1 {
			return &core.ResourceConflictError{Resource: "project membership", Reason: "the last owner cannot be removed"}
		}
	}
	removed, err := q.RemoveProjectMemberAsOwner(ctx, sqlitegen.RemoveProjectMemberAsOwnerParams{ProjectID: projectID, UserID: userID, UserID_2: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return err
	}
	if removed != 1 {
		return sql.ErrNoRows
	}
	now := formatTime(time.Now())
	auditID, err := newID()
	if err != nil {
		return err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: auditID, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "project.member_removed", TargetType: "user", TargetID: userID, Outcome: "succeeded", DetailJson: "{}", CorrelationID: correlationID, OccurredAt: now}); err != nil {
		return err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, 204, struct{ Removed bool }{true}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListConversations(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Conversation, error) {
	rows, err := s.queries.ListConversationsForUser(ctx, sqlitegen.ListConversationsForUserParams{
		OrganizationID: scope.OrganizationID,
		ProjectID:      projectID,
		UserID:         scope.UserID,
		OwnerUserID:    scope.UserID,
		Limit:          int64(limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]core.Conversation, 0, len(rows))
	for _, row := range rows {
		createdAt, parseErr := parseTime(row.CreatedAt)
		if parseErr != nil {
			return nil, parseErr
		}
		updatedAt, parseErr := parseTime(row.UpdatedAt)
		if parseErr != nil {
			return nil, parseErr
		}
		var activeLeafEntryID, parentSessionID, forkedFromEntryID string
		if err := s.db.QueryRowContext(ctx, `
			SELECT COALESCE(active_leaf_entry_id, ''), COALESCE(parent_session_id, ''),
			       COALESCE(forked_from_entry_id, '')
			FROM conversations WHERE id = ?`, row.ID,
		).Scan(&activeLeafEntryID, &parentSessionID, &forkedFromEntryID); err != nil {
			return nil, err
		}
		items = append(items, core.Conversation{
			ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID,
			OwnerUserID: row.OwnerUserID, Title: row.Title, Visibility: row.Visibility,
			MessageCount: row.NextMessageSeq, LastMessage: row.LastMessage,
			ActiveLeafEntryID: activeLeafEntryID, ParentSessionID: parentSessionID,
			ForkedFromEntryID: forkedFromEntryID,
			CreatedAt:         createdAt, UpdatedAt: updatedAt,
		})
	}
	return items, nil
}

func (s *Store) GetConversation(ctx context.Context, scope core.Scope, conversationID string) (core.Conversation, error) {
	var row struct {
		ID, OrganizationID, ProjectID, OwnerUserID, Title, Visibility, CreatedAt, UpdatedAt, LastMessage string
		ActiveLeafEntryID, ParentSessionID, ForkedFromEntryID                                            string
		NextMessageSeq                                                                                   int64
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT c.id, c.organization_id, c.project_id, c.owner_user_id, c.title, c.visibility,
		       c.next_message_seq, c.created_at, c.updated_at,
		       COALESCE((SELECT m.content FROM messages m WHERE m.conversation_id = c.id ORDER BY m.server_seq DESC LIMIT 1), ''),
		       COALESCE(c.active_leaf_entry_id, ''), COALESCE(c.parent_session_id, ''),
		       COALESCE(c.forked_from_entry_id, '')
		FROM conversations c
		JOIN memberships m ON m.organization_id = c.organization_id
		JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = m.user_id
		WHERE c.id = ? AND c.organization_id = ? AND m.user_id = ? AND m.status = 'active'
		  AND pm.status = 'active' AND (c.visibility = 'project' OR c.owner_user_id = ?)
	`, conversationID, scope.OrganizationID, scope.UserID, scope.UserID).Scan(
		&row.ID, &row.OrganizationID, &row.ProjectID, &row.OwnerUserID, &row.Title, &row.Visibility,
		&row.NextMessageSeq, &row.CreatedAt, &row.UpdatedAt, &row.LastMessage,
		&row.ActiveLeafEntryID, &row.ParentSessionID, &row.ForkedFromEntryID,
	)
	if err != nil {
		return core.Conversation{}, err
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.Conversation{}, err
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return core.Conversation{}, err
	}
	return core.Conversation{ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, OwnerUserID: row.OwnerUserID, Title: row.Title, Visibility: row.Visibility, MessageCount: row.NextMessageSeq, LastMessage: row.LastMessage, ActiveLeafEntryID: row.ActiveLeafEntryID, ParentSessionID: row.ParentSessionID, ForkedFromEntryID: row.ForkedFromEntryID, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func (s *Store) DeleteConversation(ctx context.Context, scope core.Scope, conversationID, correlationID string) error {
	var err error
	for attempt := 0; attempt < 6; attempt++ {
		err = s.deleteConversationTx(ctx, scope, conversationID, correlationID)
		if err == nil || !sqliteBusyError(err) {
			return err
		}
		if waitErr := waitSQLiteRetry(ctx, attempt); waitErr != nil {
			return waitErr
		}
	}
	return err
}

func (s *Store) RenameConversation(ctx context.Context, scope core.Scope, conversationID, title string) (core.Conversation, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE conversations SET title = ?, updated_at = ?
		WHERE id = ? AND organization_id = ? AND owner_user_id = ?`,
		title, formatTime(time.Now()), conversationID, scope.OrganizationID, scope.UserID)
	if err != nil {
		return core.Conversation{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return core.Conversation{}, err
	}
	if affected == 0 {
		return core.Conversation{}, sql.ErrNoRows
	}
	return s.GetConversation(ctx, scope, conversationID)
}

func (s *Store) deleteConversationTx(ctx context.Context, scope core.Scope, conversationID, correlationID string) error {
	conversation, err := s.GetConversation(ctx, scope, conversationID)
	if err != nil {
		return err
	}
	if conversation.OwnerUserID != scope.UserID {
		return &core.ForbiddenError{Message: "only the conversation owner can delete this thread"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var active int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tasks
		WHERE conversation_id = ? AND status IN ('queued','running','waiting_approval','waiting_input','paused')`,
		conversationID,
	).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return &core.ResourceConflictError{Resource: "conversation", Reason: "stop the active task before deleting this thread"}
	}
	// Forked threads remain usable. They lose only the deleted ancestry pointer;
	// their copied messages and entries are independent.
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations
		SET parent_session_id = NULL, forked_from_entry_id = NULL
		WHERE parent_session_id = ?`, conversationID); err != nil {
		return fmt.Errorf("detach conversation children: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE schedule_fires SET task_id = NULL
		WHERE task_id IN (SELECT id FROM tasks WHERE conversation_id = ?)`, conversationID); err != nil {
		return fmt.Errorf("detach scheduled fires: %w", err)
	}
	// The original usage ledger intentionally keeps strict foreign keys without
	// ON DELETE CASCADE. A thread owns this execution history, so remove the
	// ledger and model-call rows before deleting its tasks.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM usage_ledger
		WHERE task_id IN (SELECT id FROM tasks WHERE conversation_id = ?)`, conversationID); err != nil {
		return fmt.Errorf("delete conversation usage ledger: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM model_calls
		WHERE task_id IN (SELECT id FROM tasks WHERE conversation_id = ?)`, conversationID); err != nil {
		return fmt.Errorf("delete conversation model calls: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE conversation_id = ?`, conversationID); err != nil {
		return fmt.Errorf("delete conversation tasks: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		DELETE FROM conversations
		WHERE id = ? AND organization_id = ? AND owner_user_id = ?`,
		conversationID, scope.OrganizationID, scope.UserID,
	)
	if err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if removed != 1 {
		return sql.ErrNoRows
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (
			id, organization_id, project_id, actor_user_id, action,
			target_type, target_id, outcome, detail_json, correlation_id, occurred_at
		) VALUES (?, ?, ?, ?, 'conversation.deleted', 'conversation', ?, 'succeeded', '{}', ?, ?)`,
		auditID, scope.OrganizationID, conversation.ProjectID, scope.UserID,
		conversationID, correlationID, formatTime(time.Now()),
	); err != nil {
		return fmt.Errorf("write conversation deletion audit: %w", err)
	}
	return tx.Commit()
}

func (s *Store) ListMessages(ctx context.Context, scope core.Scope, conversationID string, after int64, limit int) ([]core.Message, error) {
	rows, err := s.queries.ListMessagesForUser(ctx, sqlitegen.ListMessagesForUserParams{
		ConversationID: conversationID,
		OrganizationID: scope.OrganizationID,
		UserID:         scope.UserID,
		OwnerUserID:    scope.UserID,
		ServerSeq:      after,
		Limit:          int64(limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]core.Message, 0, len(rows))
	for _, row := range rows {
		createdAt, parseErr := parseTime(row.CreatedAt)
		if parseErr != nil {
			return nil, parseErr
		}
		items = append(items, core.Message{
			ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID,
			ConversationID: row.ConversationID, OwnerUserID: row.OwnerUserID, TaskID: row.TaskID,
			ServerSeq: row.ServerSeq, Role: row.Role, Visibility: row.Visibility,
			Content: row.Content, CreatedAt: createdAt,
		})
	}
	return items, nil
}

func (s *Store) ListSessionEntries(ctx context.Context, scope core.Scope, conversationID string, afterEntrySeq int64, limit int) (core.SessionTreePage, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var activeLeaf string
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(c.active_leaf_entry_id, '')
		FROM conversations c
		JOIN project_memberships pm ON pm.project_id = c.project_id
		 AND pm.user_id = ? AND pm.status = 'active'
		WHERE c.id = ? AND c.organization_id = ?
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)`,
		scope.UserID, conversationID, scope.OrganizationID, scope.UserID,
	).Scan(&activeLeaf); err != nil {
		return core.SessionTreePage{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT se.id, se.organization_id, se.project_id, se.conversation_id,
		       COALESCE(se.parent_entry_id, ''), se.seq, se.entry_seq, se.kind, se.ref_id,
		       se.label, se.summary, se.namespace,
		       CASE WHEN se.kind = 'message' THEN json_object(
		         'role', COALESCE(m.role, ''),
		         'content', CASE WHEN length(COALESCE(m.content, '')) > 4000
		                         THEN substr(m.content, 1, 4000) || '…'
		                         ELSE COALESCE(m.content, '') END,
		         'truncated', length(COALESCE(m.content, '')) > 4000,
		         'task_id', COALESCE(m.task_id, '')
		       ) ELSE se.payload_json END,
		       se.schema_version, se.context_injection, se.created_at
		FROM session_entries se
		LEFT JOIN messages m ON m.id = se.ref_id
		 AND m.conversation_id = se.conversation_id
		 AND m.organization_id = se.organization_id
		WHERE se.conversation_id = ? AND se.organization_id = ? AND se.entry_seq > ?
		ORDER BY se.entry_seq LIMIT ?`,
		conversationID, scope.OrganizationID, afterEntrySeq, limit,
	)
	if err != nil {
		return core.SessionTreePage{}, err
	}
	defer rows.Close()
	entries := make([]core.SessionEntry, 0, limit)
	for rows.Next() {
		entry, scanErr := scanSessionEntry(rows)
		if scanErr != nil {
			return core.SessionTreePage{}, scanErr
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return core.SessionTreePage{}, err
	}
	next := int64(0)
	if len(entries) == limit {
		next = entries[len(entries)-1].EntrySeq
	}
	return core.SessionTreePage{Entries: entries, ActiveLeafEntryID: activeLeaf, NextEntrySeq: next}, nil
}

type sessionEntryRowScanner interface {
	Scan(dest ...any) error
}

func scanSessionEntry(scanner sessionEntryRowScanner) (core.SessionEntry, error) {
	var entry core.SessionEntry
	var createdAt string
	if err := scanner.Scan(
		&entry.ID, &entry.OrganizationID, &entry.ProjectID, &entry.ConversationID,
		&entry.ParentEntryID, &entry.Seq, &entry.EntrySeq, &entry.Kind, &entry.RefID,
		&entry.Label, &entry.Summary, &entry.Namespace, &entry.PayloadJSON,
		&entry.SchemaVersion, &entry.ContextInjection, &createdAt,
	); err != nil {
		return core.SessionEntry{}, err
	}
	var err error
	entry.CreatedAt, err = parseTime(createdAt)
	return entry, err
}

func (s *Store) GetSessionEntryPath(ctx context.Context, scope core.Scope, entryID string) ([]core.SessionEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE ancestors(
			id, organization_id, project_id, conversation_id, parent_entry_id,
			seq, entry_seq, kind, ref_id, label, summary, namespace,
			payload_json, schema_version, context_injection, created_at,
			visited, cycle, depth
		) AS (
			SELECT se.id, se.organization_id, se.project_id, se.conversation_id,
			       se.parent_entry_id, se.seq, se.entry_seq, se.kind, se.ref_id,
			       se.label, se.summary, se.namespace, se.payload_json,
			       se.schema_version, se.context_injection, se.created_at,
			       ',' || se.id || ',', 0, 0
			FROM session_entries se
			JOIN conversations c ON c.id = se.conversation_id
			JOIN project_memberships pm ON pm.project_id = c.project_id
			 AND pm.user_id = ? AND pm.status = 'active'
			WHERE se.id = ? AND se.organization_id = ?
			  AND (c.visibility = 'project' OR c.owner_user_id = ?)
			UNION ALL
			SELECT parent.id, parent.organization_id, parent.project_id,
			       parent.conversation_id, parent.parent_entry_id, parent.seq,
			       parent.entry_seq, parent.kind, parent.ref_id, parent.label,
			       parent.summary, parent.namespace, parent.payload_json,
			       parent.schema_version, parent.context_injection,
			       parent.created_at, child.visited || parent.id || ',',
			       CASE WHEN child.visited LIKE '%,' || parent.id || ',%' THEN 1 ELSE 0 END,
			       child.depth + 1
			FROM session_entries parent
			JOIN ancestors child ON parent.id = child.parent_entry_id
			WHERE child.cycle = 0 AND child.depth < 100000
		)
		SELECT a.id, a.organization_id, a.project_id, a.conversation_id,
		       COALESCE(a.parent_entry_id, ''), a.seq, a.entry_seq, a.kind, a.ref_id,
		       a.label, a.summary, a.namespace,
		       CASE WHEN a.kind = 'message' THEN json_object(
		         'role', COALESCE(m.role, ''),
		         'content', CASE WHEN length(COALESCE(m.content, '')) > 4000
		                         THEN substr(m.content, 1, 4000) || '…'
		                         ELSE COALESCE(m.content, '') END,
		         'truncated', length(COALESCE(m.content, '')) > 4000,
		         'task_id', COALESCE(m.task_id, '')
		       ) ELSE a.payload_json END,
		       a.schema_version, a.context_injection, a.created_at, a.cycle, a.depth
		FROM ancestors a
		LEFT JOIN messages m ON m.id = a.ref_id
		 AND m.conversation_id = a.conversation_id
		 AND m.organization_id = a.organization_id
		ORDER BY a.depth DESC`,
		scope.UserID, entryID, scope.OrganizationID, scope.UserID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	path := make([]core.SessionEntry, 0, 32)
	cycleDetected := false
	for rows.Next() {
		var entry core.SessionEntry
		var createdAt string
		var cycle int
		var depth int64
		if err := rows.Scan(
			&entry.ID, &entry.OrganizationID, &entry.ProjectID, &entry.ConversationID,
			&entry.ParentEntryID, &entry.Seq, &entry.EntrySeq, &entry.Kind, &entry.RefID,
			&entry.Label, &entry.Summary, &entry.Namespace, &entry.PayloadJSON,
			&entry.SchemaVersion, &entry.ContextInjection, &createdAt, &cycle, &depth,
		); err != nil {
			return nil, err
		}
		entry.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		cycleDetected = cycleDetected || cycle == 1
		path = append(path, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(path) == 0 {
		return nil, sql.ErrNoRows
	}
	if cycleDetected {
		return nil, fmt.Errorf("SESSION_TREE_CYCLE: entry %s contains a parent cycle", entryID)
	}
	if path[0].ParentEntryID != "" {
		return nil, fmt.Errorf("SESSION_TREE_ORPHAN: ancestor %s references missing parent %s", path[0].ID, path[0].ParentEntryID)
	}
	return path, nil
}

func (s *Store) ListSessionPathMessages(ctx context.Context, scope core.Scope, conversationID, leafEntryID string, afterServerSeq int64, limit int) ([]core.Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	path, err := s.GetSessionEntryPath(ctx, scope, leafEntryID)
	if err != nil {
		return nil, err
	}
	if len(path) == 0 || path[len(path)-1].ConversationID != conversationID {
		return nil, &core.ForbiddenError{Message: "leaf entry is not in this thread"}
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE path(id, parent_entry_id, depth) AS (
			SELECT id, parent_entry_id, 0
			FROM session_entries WHERE id = ? AND conversation_id = ?
			UNION ALL
			SELECT parent.id, parent.parent_entry_id, child.depth + 1
			FROM session_entries parent
			JOIN path child ON parent.id = child.parent_entry_id
			WHERE child.depth < 100000
		)
		SELECT m.id, m.organization_id, m.project_id, m.conversation_id,
		       m.owner_user_id, m.task_id, m.server_seq, m.role,
		       m.visibility, m.content, m.created_at
		FROM path
		JOIN session_entries se ON se.id = path.id AND se.kind = 'message'
		JOIN messages m ON m.id = se.ref_id
		WHERE m.server_seq > ?
		ORDER BY path.depth DESC
		LIMIT ?`,
		leafEntryID, conversationID, afterServerSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]core.Message, 0, limit)
	for rows.Next() {
		var message core.Message
		var createdAt string
		if err := rows.Scan(
			&message.ID,
			&message.OrganizationID,
			&message.ProjectID,
			&message.ConversationID,
			&message.OwnerUserID,
			&message.TaskID,
			&message.ServerSeq,
			&message.Role,
			&message.Visibility,
			&message.Content,
			&createdAt,
		); err != nil {
			return nil, err
		}
		message.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func ensureSessionTreeIdle(ctx context.Context, tx *sql.Tx, conversationID string) error {
	var active int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE t.conversation_id = ?
		  AND r.status IN ('queued', 'running', 'waiting_approval', 'waiting_input', 'paused')`,
		conversationID,
	).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return &core.StateConflictError{Status: "run_active", Command: "session_tree_mutation"}
	}
	return nil
}

func (s *Store) NavigateSession(ctx context.Context, scope core.Scope, conversationID string, input core.NavigateSessionInput) (core.NavigateSessionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.NavigateSessionResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var activeLeaf string
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(c.active_leaf_entry_id, '')
		FROM conversations c
		JOIN project_memberships pm ON pm.project_id = c.project_id
		 AND pm.user_id = ? AND pm.status = 'active'
		WHERE c.id = ? AND c.organization_id = ?
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)`,
		scope.UserID, conversationID, scope.OrganizationID, scope.UserID,
	).Scan(&activeLeaf); err != nil {
		return core.NavigateSessionResult{}, err
	}
	if err := ensureSessionTreeIdle(ctx, tx, conversationID); err != nil {
		return core.NavigateSessionResult{}, err
	}
	if input.ExpectedLeafEntryID != activeLeaf {
		return core.NavigateSessionResult{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "navigate"}
	}
	row := tx.QueryRowContext(ctx, `
		SELECT id, organization_id, project_id, conversation_id,
		       COALESCE(parent_entry_id, ''), seq, entry_seq, kind, ref_id,
		       label, summary, namespace, payload_json, schema_version,
		       context_injection, created_at
		FROM session_entries WHERE id = ? AND conversation_id = ?`,
		input.EntryID, conversationID,
	)
	entry, err := scanSessionEntry(row)
	if err != nil {
		return core.NavigateSessionResult{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE conversations SET active_leaf_entry_id = ?, updated_at = ?
		WHERE id = ? AND COALESCE(active_leaf_entry_id, '') = ?`,
		entry.ID, formatTime(time.Now()), conversationID, input.ExpectedLeafEntryID,
	)
	if err != nil {
		return core.NavigateSessionResult{}, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return core.NavigateSessionResult{}, changedErr
		}
		return core.NavigateSessionResult{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "navigate"}
	}
	composer := ""
	if entry.Kind == core.SessionEntryMessage {
		var role, content string
		if err := tx.QueryRowContext(ctx, `SELECT role, content FROM messages WHERE id = ? AND conversation_id = ?`, entry.RefID, conversationID).Scan(&role, &content); err != nil {
			return core.NavigateSessionResult{}, err
		}
		if role == "user" {
			composer = content
		}
	}
	if err := insertSessionTreeEvent(ctx, tx, scope, entry.ProjectID, conversationID, entry.ID, "navigate", map[string]any{"from_entry_id": activeLeaf, "to_entry_id": entry.ID}); err != nil {
		return core.NavigateSessionResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.NavigateSessionResult{}, err
	}
	conversation, err := s.GetConversation(ctx, scope, conversationID)
	if err != nil {
		return core.NavigateSessionResult{}, err
	}
	return core.NavigateSessionResult{Conversation: conversation, Entry: entry, ComposerText: composer}, nil
}

func (s *Store) ForkSession(ctx context.Context, scope core.Scope, conversationID string, input core.ForkSessionInput) (core.Conversation, error) {
	source, err := s.GetConversation(ctx, scope, conversationID)
	if err != nil {
		return core.Conversation{}, err
	}
	targetEntryID := strings.TrimSpace(input.EntryID)
	if targetEntryID == "" && input.Clone {
		targetEntryID = source.ActiveLeafEntryID
	}
	if targetEntryID == "" {
		return core.Conversation{}, &core.ValidationError{Field: "entry_id", Message: "is required"}
	}
	path, err := s.GetSessionEntryPath(ctx, scope, targetEntryID)
	if err != nil {
		return core.Conversation{}, err
	}
	if path[len(path)-1].ConversationID != conversationID {
		return core.Conversation{}, &core.ForbiddenError{Message: "entry is not in the source thread"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Conversation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := ensureSessionTreeIdle(ctx, tx, conversationID); err != nil {
		return core.Conversation{}, err
	}
	newConversationID, err := newID()
	if err != nil {
		return core.Conversation{}, err
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		prefix := "Fork: "
		if input.Clone {
			prefix = "Clone: "
		}
		title = prefix + source.Title
	}
	now := formatTime(time.Now())
	q := s.queries.WithTx(tx)
	if _, err := q.CreateConversation(ctx, sqlitegen.CreateConversationParams{
		ID:             newConversationID,
		OrganizationID: source.OrganizationID,
		ProjectID:      source.ProjectID,
		OwnerUserID:    scope.UserID,
		Title:          title,
		Visibility:     source.Visibility,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		return core.Conversation{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversations SET parent_session_id = ?, forked_from_entry_id = ?
		WHERE id = ?`, conversationID, targetEntryID, newConversationID,
	); err != nil {
		return core.Conversation{}, err
	}
	for _, sourceEntry := range path {
		if sourceEntry.Kind == core.SessionEntryMessage {
			var role, visibility, content string
			if err := tx.QueryRowContext(ctx, `SELECT role, visibility, content FROM messages WHERE id = ?`, sourceEntry.RefID).Scan(&role, &visibility, &content); err != nil {
				return core.Conversation{}, err
			}
			if _, _, err := s.appendSessionMessageTx(ctx, tx, q, sessionMessageInput{
				OrganizationID: source.OrganizationID,
				ProjectID:      source.ProjectID,
				ConversationID: newConversationID,
				OwnerUserID:    scope.UserID,
				Role:           role,
				Visibility:     visibility,
				Content:        content,
				Now:            now,
			}); err != nil {
				return core.Conversation{}, err
			}
			continue
		}
		kind := sourceEntry.Kind
		refID := ""
		summary := sourceEntry.Summary
		namespace := sourceEntry.Namespace
		if kind == core.SessionEntryCompaction {
			kind = core.SessionEntryBranchSummary
			namespace = ""
			if err := tx.QueryRowContext(ctx, `SELECT summary FROM conversation_compactions WHERE id = ?`, sourceEntry.RefID).Scan(&summary); err != nil {
				return core.Conversation{}, err
			}
		}
		if _, err := s.appendSessionEntryTx(ctx, tx, sessionEntryInput{
			OrganizationID:   source.OrganizationID,
			ProjectID:        source.ProjectID,
			ConversationID:   newConversationID,
			Kind:             kind,
			RefID:            refID,
			Label:            sourceEntry.Label,
			Summary:          summary,
			Namespace:        namespace,
			PayloadJSON:      sourceEntry.PayloadJSON,
			ContextInjection: sourceEntry.ContextInjection,
			Now:              now,
		}); err != nil {
			return core.Conversation{}, err
		}
	}
	operation := "fork"
	if input.Clone {
		operation = "clone"
	}
	if err := insertSessionTreeEvent(ctx, tx, scope, source.ProjectID, conversationID, targetEntryID, operation, map[string]any{"new_conversation_id": newConversationID}); err != nil {
		return core.Conversation{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Conversation{}, err
	}
	return s.GetConversation(ctx, scope, newConversationID)
}

func (s *Store) LabelSessionEntry(ctx context.Context, scope core.Scope, conversationID, entryID string, input core.LabelSessionEntryInput) (core.SessionEntry, error) {
	label := strings.TrimSpace(input.Label)
	if label == "" || len([]rune(label)) > 120 {
		return core.SessionEntry{}, &core.ValidationError{Field: "label", Message: "must contain 1 to 120 characters"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.SessionEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var organizationID, projectID, activeLeaf string
	if err := tx.QueryRowContext(ctx, `
		SELECT c.organization_id, c.project_id, COALESCE(c.active_leaf_entry_id, '')
		FROM conversations c
		JOIN project_memberships pm ON pm.project_id = c.project_id
		 AND pm.user_id = ? AND pm.status = 'active'
		WHERE c.id = ? AND c.organization_id = ?
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)`,
		scope.UserID, conversationID, scope.OrganizationID, scope.UserID,
	).Scan(&organizationID, &projectID, &activeLeaf); err != nil {
		return core.SessionEntry{}, err
	}
	if err := ensureSessionTreeIdle(ctx, tx, conversationID); err != nil {
		return core.SessionEntry{}, err
	}
	if activeLeaf != input.ExpectedLeafEntryID {
		return core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "label"}
	}
	result, err := tx.ExecContext(ctx, `UPDATE session_entries SET label = ? WHERE id = ? AND conversation_id = ?`, label, entryID, conversationID)
	if err != nil {
		return core.SessionEntry{}, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return core.SessionEntry{}, changedErr
		}
		return core.SessionEntry{}, sql.ErrNoRows
	}
	payload, _ := json.Marshal(map[string]any{"target_entry_id": entryID, "label": label})
	auditEntry, err := s.appendSessionEntryTx(ctx, tx, sessionEntryInput{
		OrganizationID:      organizationID,
		ProjectID:           projectID,
		ConversationID:      conversationID,
		Kind:                core.SessionEntryCustom,
		Namespace:           "system/label",
		PayloadJSON:         string(payload),
		ContextInjection:    "exclude",
		ExpectedLeafEntryID: input.ExpectedLeafEntryID,
		Now:                 formatTime(time.Now()),
	})
	if err != nil {
		return core.SessionEntry{}, err
	}
	if err := insertSessionTreeEvent(ctx, tx, scope, projectID, conversationID, entryID, "label", map[string]any{"label": label, "audit_entry_id": auditEntry.ID}); err != nil {
		return core.SessionEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.SessionEntry{}, err
	}
	return auditEntry, nil
}

func (s *Store) AppendBranchSummary(ctx context.Context, scope core.Scope, conversationID string, input core.BranchSummaryInput) (core.SessionEntry, error) {
	summary := strings.TrimSpace(input.Summary)
	if summary == "" || len(summary) > 64*1024 {
		return core.SessionEntry{}, &core.ValidationError{Field: "summary", Message: "must contain 1 to 65536 bytes"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.SessionEntry{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var organizationID, projectID, activeLeaf string
	if err := tx.QueryRowContext(ctx, `
		SELECT c.organization_id, c.project_id, COALESCE(c.active_leaf_entry_id, '')
		FROM conversations c
		JOIN project_memberships pm ON pm.project_id = c.project_id
		 AND pm.user_id = ? AND pm.status = 'active'
		WHERE c.id = ? AND c.organization_id = ?
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)`,
		scope.UserID, conversationID, scope.OrganizationID, scope.UserID,
	).Scan(&organizationID, &projectID, &activeLeaf); err != nil {
		return core.SessionEntry{}, err
	}
	if err := ensureSessionTreeIdle(ctx, tx, conversationID); err != nil {
		return core.SessionEntry{}, err
	}
	if activeLeaf != input.ExpectedLeafEntryID {
		return core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "branch_summary"}
	}
	entry, err := s.appendSessionEntryTx(ctx, tx, sessionEntryInput{
		OrganizationID:      organizationID,
		ProjectID:           projectID,
		ConversationID:      conversationID,
		Kind:                core.SessionEntryBranchSummary,
		Summary:             summary,
		ContextInjection:    "summary_only",
		ExpectedLeafEntryID: input.ExpectedLeafEntryID,
		Now:                 formatTime(time.Now()),
	})
	if err != nil {
		return core.SessionEntry{}, err
	}
	if err := insertSessionTreeEvent(ctx, tx, scope, projectID, conversationID, entry.ID, "branch_summary", map[string]any{"entry_id": entry.ID}); err != nil {
		return core.SessionEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.SessionEntry{}, err
	}
	return entry, nil
}

func insertSessionTreeEvent(ctx context.Context, tx *sql.Tx, scope core.Scope, projectID, conversationID, entryID, operation string, payload map[string]any) error {
	id, err := newID()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO session_tree_events (
			id, organization_id, project_id, conversation_id, entry_id,
			operation, actor_user_id, payload_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, scope.OrganizationID, projectID, conversationID, entryID,
		operation, scope.UserID, string(encoded), formatTime(time.Now()),
	)
	return err
}

func (s *Store) GetConversationContextSettings(ctx context.Context, scope core.Scope) (core.ConversationContextSettings, error) {
	var item core.ConversationContextSettings
	var autoCompact int64
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT c.organization_id, c.owner_user_id, c.auto_compact, c.trigger_tokens,
		       c.retain_tokens, c.summary_max_tokens, c.updated_at
		FROM conversation_context_settings c
		JOIN memberships m ON m.organization_id = c.organization_id AND m.user_id = c.owner_user_id
		WHERE c.organization_id = ? AND c.owner_user_id = ? AND m.status = 'active'
	`, scope.OrganizationID, scope.UserID).Scan(&item.OrganizationID, &item.OwnerUserID, &autoCompact, &item.TriggerTokens, &item.RetainTokens, &item.SummaryMaxTokens, &updatedAt)
	if err != nil {
		return core.ConversationContextSettings{}, err
	}
	item.AutoCompact = autoCompact == 1
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return core.ConversationContextSettings{}, err
	}
	return item, nil
}

func (s *Store) UpsertConversationContextSettings(ctx context.Context, scope core.Scope, input core.UpdateConversationContextSettingsInput) (core.ConversationContextSettings, error) {
	if input.TriggerTokens <= 0 || input.RetainTokens <= 0 || input.SummaryMaxTokens <= 0 {
		return core.ConversationContextSettings{}, &core.ValidationError{Field: "context_settings", Message: "token budgets must be positive"}
	}
	if input.TriggerTokens > 10_000_000 || input.RetainTokens > 1_000_000 || input.SummaryMaxTokens > 32_768 {
		return core.ConversationContextSettings{}, &core.ValidationError{Field: "context_settings", Message: "token budgets exceed supported limits"}
	}
	if input.RetainTokens >= input.TriggerTokens {
		return core.ConversationContextSettings{}, &core.ValidationError{Field: "retain_tokens", Message: "must be lower than trigger_tokens"}
	}
	if input.SummaryMaxTokens >= input.TriggerTokens-input.RetainTokens {
		return core.ConversationContextSettings{}, &core.ValidationError{Field: "summary_max_tokens", Message: "must leave room below the trigger after retained context"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ConversationContextSettings{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM memberships WHERE organization_id = ? AND user_id = ? AND status = 'active'`, scope.OrganizationID, scope.UserID).Scan(&active); err != nil {
		return core.ConversationContextSettings{}, err
	}
	if active != 1 {
		return core.ConversationContextSettings{}, sql.ErrNoRows
	}
	autoCompact := int64(0)
	if input.AutoCompact {
		autoCompact = 1
	}
	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_context_settings (organization_id, owner_user_id, auto_compact, trigger_tokens, retain_tokens, summary_max_tokens, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(organization_id, owner_user_id) DO UPDATE SET auto_compact = excluded.auto_compact, trigger_tokens = excluded.trigger_tokens, retain_tokens = excluded.retain_tokens, summary_max_tokens = excluded.summary_max_tokens, updated_at = excluded.updated_at`, scope.OrganizationID, scope.UserID, autoCompact, input.TriggerTokens, input.RetainTokens, input.SummaryMaxTokens, now); err != nil {
		return core.ConversationContextSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ConversationContextSettings{}, err
	}
	return s.GetConversationContextSettings(ctx, scope)
}

func (s *Store) GetLatestConversationCompaction(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationCompaction, error) {
	var item core.ConversationCompaction
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
		WITH RECURSIVE active_path(id, parent_entry_id, depth) AS (
			SELECT se.id, se.parent_entry_id, 0
			FROM conversations active_conversation
			JOIN session_entries se ON se.id = active_conversation.active_leaf_entry_id
			WHERE active_conversation.id = ?
			UNION ALL
			SELECT parent.id, parent.parent_entry_id, child.depth + 1
			FROM session_entries parent
			JOIN active_path child ON parent.id = child.parent_entry_id
			WHERE child.depth < 100000
		)
		SELECT cc.id, cc.organization_id, cc.project_id, cc.conversation_id, cc.owner_user_id,
		       cc.summary, cc.compacted_through_seq, cc.retained_from_seq,
		       cc.source_message_count, cc.source_tokens, cc.trigger,
		       cc.split_message_id, cc.split_prefix_runes,
		       COALESCE(se.id, ''), cc.created_at
		FROM conversation_compactions cc
		JOIN conversations c ON c.id = cc.conversation_id
		LEFT JOIN session_entries se ON se.conversation_id = cc.conversation_id
		 AND se.kind = 'compaction' AND se.ref_id = cc.id
		JOIN active_path path ON path.id = se.id
		JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = ? AND pm.status = 'active'
		WHERE cc.conversation_id = ? AND cc.organization_id = ?
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)
		ORDER BY cc.created_at DESC, cc.id DESC LIMIT 1
	`, conversationID, scope.UserID, conversationID, scope.OrganizationID, scope.UserID).Scan(
		&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.OwnerUserID,
		&item.Summary, &item.CompactedThroughSeq, &item.RetainedFromSeq,
		&item.SourceMessageCount, &item.SourceTokens, &item.Trigger,
		&item.SplitMessageID, &item.SplitPrefixRunes, &item.EntryID, &createdAt,
	)
	if err != nil {
		return core.ConversationCompaction{}, err
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return core.ConversationCompaction{}, err
	}
	return item, nil
}

func (s *Store) SaveConversationCompaction(ctx context.Context, scope core.Scope, item core.ConversationCompaction) (core.ConversationCompaction, error) {
	if strings.TrimSpace(item.Summary) == "" || item.CompactedThroughSeq < 0 || item.RetainedFromSeq <= item.CompactedThroughSeq || item.SourceMessageCount <= 0 || item.SourceTokens <= 0 {
		return core.ConversationCompaction{}, &core.ValidationError{Field: "compaction", Message: "contains invalid summary boundaries"}
	}
	if item.Trigger != "auto" && item.Trigger != "manual" {
		return core.ConversationCompaction{}, &core.ValidationError{Field: "trigger", Message: "must be auto or manual"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ConversationCompaction{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var projectID, ownerUserID string
	if err := tx.QueryRowContext(ctx, `SELECT c.project_id, c.owner_user_id FROM conversations c JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = ? AND pm.status = 'active' WHERE c.id = ? AND c.organization_id = ? AND (c.visibility = 'project' OR c.owner_user_id = ?)`, scope.UserID, item.ConversationID, scope.OrganizationID, scope.UserID).Scan(&projectID, &ownerUserID); err != nil {
		return core.ConversationCompaction{}, err
	}
	if item.ProjectID != "" && item.ProjectID != projectID {
		return core.ConversationCompaction{}, &core.ForbiddenError{Message: "conversation compaction project does not match"}
	}
	if err := ensureSessionTreeIdle(ctx, tx, item.ConversationID); err != nil {
		return core.ConversationCompaction{}, err
	}
	if strings.TrimSpace(item.ID) == "" {
		item.ID, err = newID()
		if err != nil {
			return core.ConversationCompaction{}, err
		}
	}
	item.OrganizationID = scope.OrganizationID
	item.ProjectID = projectID
	item.OwnerUserID = ownerUserID
	item.CreatedAt = time.Now()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO conversation_compactions (
			id, organization_id, project_id, conversation_id, owner_user_id,
			summary, compacted_through_seq, retained_from_seq,
			source_message_count, source_tokens, trigger,
			split_message_id, split_prefix_runes, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.ID, item.OrganizationID, item.ProjectID, item.ConversationID, item.OwnerUserID,
		item.Summary, item.CompactedThroughSeq, item.RetainedFromSeq,
		item.SourceMessageCount, item.SourceTokens, item.Trigger,
		item.SplitMessageID, item.SplitPrefixRunes, formatTime(item.CreatedAt),
	); err != nil {
		return core.ConversationCompaction{}, err
	}
	entry, err := s.appendSessionEntryTx(ctx, tx, sessionEntryInput{
		OrganizationID:      item.OrganizationID,
		ProjectID:           item.ProjectID,
		ConversationID:      item.ConversationID,
		Kind:                core.SessionEntryCompaction,
		RefID:               item.ID,
		PayloadJSON:         fmt.Sprintf(`{"compacted_through_seq":%d,"retained_from_seq":%d,"split_message_id":%q,"split_prefix_runes":%d}`, item.CompactedThroughSeq, item.RetainedFromSeq, item.SplitMessageID, item.SplitPrefixRunes),
		ContextInjection:    "summary_only",
		ExpectedLeafEntryID: item.ExpectedLeafEntryID,
		Now:                 formatTime(item.CreatedAt),
	})
	if err != nil {
		return core.ConversationCompaction{}, err
	}
	item.EntryID = entry.ID
	if err := tx.Commit(); err != nil {
		return core.ConversationCompaction{}, err
	}
	return item, nil
}

func (s *Store) AppendChatTurn(ctx context.Context, scope core.Scope, projectID string, input core.ProjectTurnInput, assistant string, calls []core.ConversationModelCall, idempotency core.Idempotency) (core.ProjectTurnResult, error) {
	route := "POST /projects/" + projectID + "/turns"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.ProjectTurnResult
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		if found {
			return replay, nil
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.ProjectTurnResult{}, err
	}
	conversationID := strings.TrimSpace(input.ConversationID)
	now := formatTime(time.Now())
	if conversationID == "" {
		conversationID, err = newID()
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		if _, err := q.CreateConversation(ctx, sqlitegen.CreateConversationParams{ID: conversationID, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, Title: titleFromMessage(input.Content), Visibility: input.Visibility, CreatedAt: now, UpdatedAt: now}); err != nil {
			return core.ProjectTurnResult{}, fmt.Errorf("create chat conversation: %w", err)
		}
	} else {
		var visibleProjectID, visibility string
		if err := tx.QueryRowContext(ctx, `
			SELECT c.project_id, c.visibility FROM conversations c
			JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = ? AND pm.status = 'active'
			WHERE c.id = ? AND c.organization_id = ? AND (c.visibility = 'project' OR c.owner_user_id = ?)
		`, scope.UserID, conversationID, scope.OrganizationID, scope.UserID).Scan(&visibleProjectID, &visibility); err != nil {
			return core.ProjectTurnResult{}, err
		}
		if visibleProjectID != projectID || visibility != input.Visibility {
			return core.ProjectTurnResult{}, &core.ForbiddenError{Message: "conversation scope does not match turn visibility"}
		}
	}
	created := make([]core.Message, 0, 2)
	for _, value := range []struct{ Role, Content string }{{"user", input.Content}, {"assistant", assistant}} {
		message, _, appendErr := s.appendSessionMessageTx(ctx, tx, q, sessionMessageInput{
			OrganizationID: scope.OrganizationID,
			ProjectID:      projectID,
			ConversationID: conversationID,
			TaskID:         input.TaskID,
			OwnerUserID:    scope.UserID,
			Role:           value.Role,
			Visibility:     input.Visibility,
			Content:        value.Content,
			Now:            now,
		})
		if appendErr != nil {
			return core.ProjectTurnResult{}, appendErr
		}
		created = append(created, message)
	}
	if err := recordConversationCalls(ctx, tx, scope, projectID, conversationID, calls, now); err != nil {
		return core.ProjectTurnResult{}, err
	}
	var title, createdAtText, updatedAtText, activeLeafEntryID, parentSessionID, forkedFromEntryID string
	var count int64
	if err := tx.QueryRowContext(ctx, `
		SELECT title, next_message_seq, created_at, updated_at,
		       COALESCE(active_leaf_entry_id, ''), COALESCE(parent_session_id, ''),
		       COALESCE(forked_from_entry_id, '')
		FROM conversations WHERE id = ?`, conversationID,
	).Scan(&title, &count, &createdAtText, &updatedAtText, &activeLeafEntryID, &parentSessionID, &forkedFromEntryID); err != nil {
		return core.ProjectTurnResult{}, err
	}
	createdAt, err := parseTime(createdAtText)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	updatedAt, err := parseTime(updatedAtText)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	result := core.ProjectTurnResult{Route: "chat", Conversation: core.Conversation{ID: conversationID, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, Title: title, Visibility: input.Visibility, MessageCount: count, LastMessage: assistant, ActiveLeafEntryID: activeLeafEntryID, ParentSessionID: parentSessionID, ForkedFromEntryID: forkedFromEntryID, CreatedAt: createdAt, UpdatedAt: updatedAt}, Messages: created}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, result); err != nil {
		return core.ProjectTurnResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ProjectTurnResult{}, err
	}
	return result, nil
}

func (s *Store) ImportTranscript(ctx context.Context, scope core.Scope, projectID string, input core.ImportTranscriptInput, idempotency core.Idempotency) (core.ImportTranscriptResult, error) {
	route := "POST /projects/" + projectID + "/transcript-import"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.ImportTranscriptResult
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.ImportTranscriptResult{}, err
		}
		if found {
			return replay, nil
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ImportTranscriptResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.ImportTranscriptResult{}, err
	}
	result := core.ImportTranscriptResult{ProjectID: projectID, ConversationIDs: make([]string, 0, len(input.Conversations))}
	for _, source := range input.Conversations {
		conversationID, err := newID()
		if err != nil {
			return core.ImportTranscriptResult{}, err
		}
		now := formatTime(time.Now().UTC())
		title := strings.TrimSpace(source.Title)
		if title == "" && len(source.Messages) > 0 {
			title = titleFromMessage(source.Messages[0].Content)
		}
		if title == "" {
			title = "Imported Thread"
		}
		if _, err := q.CreateConversation(ctx, sqlitegen.CreateConversationParams{
			ID: conversationID, OrganizationID: scope.OrganizationID, ProjectID: projectID,
			OwnerUserID: scope.UserID, Title: title, Visibility: core.VisibilityPrivate,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return core.ImportTranscriptResult{}, fmt.Errorf("create imported conversation: %w", err)
		}
		for _, sourceMessage := range source.Messages {
			messageTime := sourceMessage.CreatedAt.UTC()
			if messageTime.IsZero() {
				messageTime = time.Now().UTC()
			}
			if _, _, err := s.appendSessionMessageTx(ctx, tx, q, sessionMessageInput{
				OrganizationID: scope.OrganizationID, ProjectID: projectID,
				ConversationID: conversationID, OwnerUserID: scope.UserID,
				Role: sourceMessage.Role, Visibility: core.VisibilityPrivate,
				Content: sourceMessage.Content, Now: formatTime(messageTime),
			}); err != nil {
				return core.ImportTranscriptResult{}, err
			}
			result.MessageCount++
		}
		result.ConversationIDs = append(result.ConversationIDs, conversationID)
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, result); err != nil {
		return core.ImportTranscriptResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ImportTranscriptResult{}, err
	}
	return result, nil
}

func (s *Store) RecordConversationModelCalls(ctx context.Context, scope core.Scope, projectID, conversationID string, calls []core.ConversationModelCall) error {
	if len(calls) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var visibleProjectID string
	if err := tx.QueryRowContext(ctx, `SELECT c.project_id FROM conversations c JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = ? AND pm.status = 'active' WHERE c.id = ? AND c.organization_id = ? AND (c.visibility = 'project' OR c.owner_user_id = ?)`, scope.UserID, conversationID, scope.OrganizationID, scope.UserID).Scan(&visibleProjectID); err != nil {
		return err
	}
	if visibleProjectID != projectID {
		return &core.ForbiddenError{Message: "conversation is not in this project"}
	}
	if err := recordConversationCalls(ctx, tx, scope, projectID, conversationID, calls, formatTime(time.Now())); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LatestConversationContextUsage(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationContextUsage, error) {
	var item core.ConversationContextUsage
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT cmc.conversation_id, cmc.purpose, cmc.context_tokens,
		       cmc.input_tokens, cmc.output_tokens, cmc.cache_read_tokens,
		       cmc.usage_semantics_version, cmc.created_at
		FROM conversation_model_calls cmc
		JOIN conversations c ON c.id = cmc.conversation_id
		JOIN project_memberships pm ON pm.project_id = c.project_id
		 AND pm.user_id = ? AND pm.status = 'active'
		WHERE cmc.organization_id = ? AND cmc.conversation_id = ?
		  AND cmc.error_code = ''
		  AND cmc.purpose = 'answer'
		  AND cmc.usage_estimated = 0
		  AND (cmc.context_tokens > 0 OR cmc.input_tokens > 0)
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)
		ORDER BY cmc.created_at DESC, cmc.id DESC
		LIMIT 1`,
		scope.UserID, scope.OrganizationID, conversationID, scope.UserID,
	).Scan(
		&item.ConversationID,
		&item.Purpose,
		&item.ContextTokens,
		&item.InputTokens,
		&item.OutputTokens,
		&item.CacheReadTokens,
		&item.SemanticsVersion,
		&createdAt,
	)
	if err != nil {
		return core.ConversationContextUsage{}, err
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return core.ConversationContextUsage{}, err
	}
	return item, nil
}

func (s *Store) ConversationFileContext(ctx context.Context, scope core.Scope, conversationID string, through time.Time) (core.ConversationFileContext, error) {
	var visible int
	if err := s.db.QueryRowContext(ctx, `
		SELECT 1
		FROM conversations c
		JOIN project_memberships pm ON pm.project_id = c.project_id
		 AND pm.user_id = ? AND pm.status = 'active'
		WHERE c.id = ? AND c.organization_id = ?
		  AND (c.visibility = 'project' OR c.owner_user_id = ?)`,
		scope.UserID, conversationID, scope.OrganizationID, scope.UserID,
	).Scan(&visible); err != nil {
		return core.ConversationFileContext{}, err
	}
	throughText := formatTime(through.UTC())
	modifiedSet := map[string]struct{}{}
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.name
		FROM artifacts a
		JOIN tasks t ON t.id = a.task_id
		WHERE t.conversation_id = ? AND a.organization_id = ?
		  AND a.source_type = 'workspace_file' AND a.created_at <= ?
		ORDER BY a.name, a.id`, conversationID, scope.OrganizationID, throughText)
	if err != nil {
		return core.ConversationFileContext{}, err
	}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return core.ConversationFileContext{}, err
		}
		if path = strings.TrimSpace(filepath.ToSlash(path)); path != "" {
			modifiedSet[path] = struct{}{}
		}
	}
	if err := rows.Close(); err != nil {
		return core.ConversationFileContext{}, err
	}
	readSet := map[string]struct{}{}
	rows, err = s.db.QueryContext(ctx, `
		SELECT tc.arguments_json
		FROM tool_calls tc
		JOIN tasks t ON t.id = tc.task_id
		WHERE t.conversation_id = ? AND tc.organization_id = ?
		  AND tc.tool_name IN ('read_file', 'workspace_read_file')
		  AND tc.created_at <= ?
		ORDER BY tc.position, tc.id`, conversationID, scope.OrganizationID, throughText)
	if err != nil {
		return core.ConversationFileContext{}, err
	}
	for rows.Next() {
		var argumentsJSON string
		if err := rows.Scan(&argumentsJSON); err != nil {
			_ = rows.Close()
			return core.ConversationFileContext{}, err
		}
		var arguments map[string]any
		if json.Unmarshal([]byte(argumentsJSON), &arguments) != nil {
			continue
		}
		path, _ := arguments["path"].(string)
		if path = strings.TrimSpace(filepath.ToSlash(path)); path != "" {
			readSet[path] = struct{}{}
		}
	}
	if err := rows.Close(); err != nil {
		return core.ConversationFileContext{}, err
	}
	result := core.ConversationFileContext{
		ReadFiles:     make([]string, 0, len(readSet)),
		ModifiedFiles: make([]string, 0, len(modifiedSet)),
	}
	for path := range readSet {
		result.ReadFiles = append(result.ReadFiles, path)
	}
	for path := range modifiedSet {
		result.ModifiedFiles = append(result.ModifiedFiles, path)
	}
	sort.Strings(result.ReadFiles)
	sort.Strings(result.ModifiedFiles)
	return result, nil
}

func recordConversationCalls(ctx context.Context, tx *sql.Tx, scope core.Scope, projectID, conversationID string, calls []core.ConversationModelCall, now string) error {
	for _, call := range calls {
		id := strings.TrimSpace(call.Model.CallID)
		if id == "" {
			var err error
			id, err = newID()
			if err != nil {
				return err
			}
		}
		estimated := int64(0)
		if call.Model.Usage.Estimated {
			estimated = 1
		}
		cost := estimateModelCost(call.Model.Model, call.Model.Usage)
		requested, effective := persistedModelRefs(call.Model)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversation_model_calls (
			id, organization_id, project_id, conversation_id, owner_user_id, purpose,
			provider, endpoint, model, routing_reason, provider_request_id,
			input_tokens, output_tokens, cached_tokens, reasoning_tokens, usage_estimated,
			latency_ms, error_code, cost_decimal, currency, created_at,
			through_entry_id, requested_provider_id, requested_model_id,
			effective_provider_id, effective_model_id, stop_reason, context_tokens,
			cache_read_tokens, cache_write_tokens, usage_semantics_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'USD', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, scope.OrganizationID, projectID, conversationID, scope.UserID, call.Purpose,
			call.Model.Provider, call.Model.Endpoint, call.Model.Model, call.Model.RoutingReason, call.Model.ProviderRequestID,
			call.Model.Usage.InputTokens, call.Model.Usage.OutputTokens, call.Model.Usage.CachedTokens, call.Model.Usage.ReasoningTokens, estimated,
			call.Model.Latency.Milliseconds(), call.Model.ErrorCode, cost, now,
			call.ThroughEntryID, requested.ProviderID, requested.ModelID,
			effective.ProviderID, effective.ModelID, call.Model.StopReason, call.Model.Usage.ContextTokens,
			persistedCacheReadTokens(call.Model.Usage), call.Model.Usage.CacheWriteTokens, persistedUsageVersion(call.Model.Usage)); err != nil {
			return fmt.Errorf("record conversation model call: %w", err)
		}
	}
	return nil
}

func titleFromMessage(content string) string {
	title := strings.TrimSpace(strings.ReplaceAll(content, "\n", " "))
	if runes := []rune(title); len(runes) > 60 {
		title = string(runes[:60])
	}
	if title == "" {
		return "新会话"
	}
	return title
}

func (s *Store) ListProjectActivity(ctx context.Context, scope core.Scope, projectID, before string, limit int) ([]core.ProjectActivity, error) {
	rows, err := s.queries.ListProjectActivityForUser(ctx, sqlitegen.ListProjectActivityForUserParams{
		OrganizationID: scope.OrganizationID,
		ProjectID:      projectID,
		UserID:         scope.UserID,
		OwnerUserID:    scope.UserID,
		NULLIF:         before,
		Limit:          int64(limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]core.ProjectActivity, 0, len(rows))
	for _, row := range rows {
		occurredAt, parseErr := parseTime(row.OccurredAt)
		if parseErr != nil {
			return nil, parseErr
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(row.PayloadJson), &payload); err != nil {
			return nil, fmt.Errorf("decode project activity payload: %w", err)
		}
		items = append(items, core.ProjectActivity{
			ID: row.ID, ProjectID: row.ProjectID, RunID: row.RunID, TaskID: row.TaskID,
			TaskTitle: row.TaskTitle, TaskStatus: row.TaskStatus, Mode: row.Mode,
			EventType: row.EventType, Visibility: row.Visibility, Payload: payload, OccurredAt: occurredAt,
		})
	}
	return items, nil
}

func (s *Store) CreatePlanItem(ctx context.Context, scope core.Scope, projectID string, input core.CreatePlanItemInput, idempotency core.Idempotency) (core.PlanItem, error) {
	route := "POST /projects/" + projectID + "/plan-items"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.PlanItem
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.PlanItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.PlanItem
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.PlanItem{}, err
	}
	rank, err := q.NextPlanItemRank(ctx, sqlitegen.NextPlanItemRankParams{ProjectID: projectID, Status: core.PlanStatusTodo})
	if err != nil {
		return core.PlanItem{}, err
	}
	id, err := newID()
	if err != nil {
		return core.PlanItem{}, err
	}
	now := formatTime(time.Now())
	row, err := q.CreatePlanItem(ctx, sqlitegen.CreatePlanItemParams{
		ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID,
		Title: input.Title, Description: input.Description, Status: core.PlanStatusTodo,
		Source: core.PlanSourceManual, Visibility: input.Visibility, Rank: rank, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return core.PlanItem{}, err
	}
	item, err := planItemFromRow(row)
	if err != nil {
		return core.PlanItem{}, err
	}
	auditID, err := newID()
	if err != nil {
		return core.PlanItem{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{
		ID: auditID, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID,
		Action: "plan_item.created", TargetType: "plan_item", TargetID: item.ID, Outcome: "succeeded",
		DetailJson:    fmt.Sprintf(`{"status":%q,"visibility":%q,"source":%q}`, item.Status, item.Visibility, item.Source),
		CorrelationID: auditID, OccurredAt: now,
	}); err != nil {
		return core.PlanItem{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, item); err != nil {
		return core.PlanItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.PlanItem{}, err
	}
	return item, nil
}

func (s *Store) ListPlanItems(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.PlanItem, error) {
	rows, err := s.queries.ListPlanItemsForUser(ctx, sqlitegen.ListPlanItemsForUserParams{
		OrganizationID: scope.OrganizationID, ProjectID: projectID, UserID: scope.UserID,
		OwnerUserID: scope.UserID, Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	items := make([]core.PlanItem, 0, len(rows))
	for _, row := range rows {
		item, convertErr := planItemFromRow(row)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) MovePlanItem(ctx context.Context, scope core.Scope, planItemID string, input core.MovePlanItemInput, idempotency core.Idempotency) (core.PlanItem, error) {
	route := "POST /plan-items/" + planItemID + "/status"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.PlanItem
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.PlanItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.PlanItem
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	current, err := q.GetPlanItemForUser(ctx, sqlitegen.GetPlanItemForUserParams{ID: planItemID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.PlanItem{}, err
	}
	if current.Status == input.Status {
		item, err := planItemFromRow(current)
		if err != nil {
			return core.PlanItem{}, err
		}
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, 200, item); err != nil {
			return core.PlanItem{}, err
		}
		if err := tx.Commit(); err != nil {
			return core.PlanItem{}, err
		}
		return item, nil
	}
	rank, err := q.NextPlanItemRank(ctx, sqlitegen.NextPlanItemRankParams{ProjectID: current.ProjectID, Status: input.Status})
	if err != nil {
		return core.PlanItem{}, err
	}
	now := formatTime(time.Now())
	row, err := q.UpdatePlanItemStatusForUser(ctx, sqlitegen.UpdatePlanItemStatusForUserParams{
		Status: input.Status, Rank: rank, UpdatedAt: now, ID: planItemID,
		OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID,
	})
	if err != nil {
		return core.PlanItem{}, err
	}
	item, err := planItemFromRow(row)
	if err != nil {
		return core.PlanItem{}, err
	}
	auditID, err := newID()
	if err != nil {
		return core.PlanItem{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{
		ID: auditID, OrganizationID: scope.OrganizationID, ProjectID: item.ProjectID, ActorUserID: scope.UserID,
		Action: "plan_item.status_changed", TargetType: "plan_item", TargetID: item.ID, Outcome: "succeeded",
		DetailJson:    fmt.Sprintf(`{"from":%q,"to":%q,"rank":%d}`, current.Status, item.Status, item.Rank),
		CorrelationID: auditID, OccurredAt: now,
	}); err != nil {
		return core.PlanItem{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, 200, item); err != nil {
		return core.PlanItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.PlanItem{}, err
	}
	return item, nil
}

func (s *Store) CreateAssetFolder(ctx context.Context, scope core.Scope, projectID string, input core.CreateAssetFolderInput, idempotency core.Idempotency) (core.Asset, error) {
	route := "POST /projects/" + projectID + "/asset-folders"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Asset
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Asset{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.Asset
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.Asset{}, err
	}
	if err := validateAssetParent(ctx, q, scope, projectID, input.ParentID, input.Visibility); err != nil {
		return core.Asset{}, err
	}
	id, err := newID()
	if err != nil {
		return core.Asset{}, err
	}
	now := formatTime(time.Now())
	row, err := q.CreateAsset(ctx, sqlitegen.CreateAssetParams{
		ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID,
		ParentID: input.ParentID, Name: input.Name, Kind: core.AssetKindFolder,
		Visibility: input.Visibility, SourceType: core.AssetSourceUpload, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return core.Asset{}, mapAssetConflict(err, input.Name)
	}
	asset, err := assetFromRow(row)
	if err != nil {
		return core.Asset{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, asset); err != nil {
		return core.Asset{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Asset{}, err
	}
	return asset, nil
}

func (s *Store) GetAsset(ctx context.Context, scope core.Scope, assetID string) (core.Asset, error) {
	row, err := s.queries.GetAssetForUser(ctx, sqlitegen.GetAssetForUserParams{ID: assetID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Asset{}, err
	}
	return assetFromRow(row)
}

func (s *Store) UploadAsset(ctx context.Context, scope core.Scope, projectID string, input core.UploadAssetInput, content io.Reader, maxFileBytes, quotaBytes int64) (core.Asset, error) {
	if _, err := s.queries.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.Asset{}, err
	}
	if err := validateAssetParent(ctx, s.queries, scope, projectID, input.ParentID, input.Visibility); err != nil {
		return core.Asset{}, err
	}
	persisted, err := s.persistBlobReader(ctx, content, input.MediaType, maxFileBytes)
	if err != nil {
		return core.Asset{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Asset{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.Asset{}, err
	}
	if err := validateAssetParent(ctx, q, scope, projectID, input.ParentID, input.Visibility); err != nil {
		return core.Asset{}, err
	}
	used, err := q.SumProjectAssetBytes(ctx, sqlitegen.SumProjectAssetBytesParams{OrganizationID: scope.OrganizationID, ProjectID: projectID})
	if err != nil {
		return core.Asset{}, err
	}
	if used > quotaBytes-persisted.SizeBytes {
		return core.Asset{}, &core.CapacityExceededError{LimitBytes: quotaBytes}
	}
	now := formatTime(time.Now())
	if err := ensureBlob(ctx, tx, persisted, now); err != nil {
		return core.Asset{}, err
	}
	id, err := newID()
	if err != nil {
		return core.Asset{}, err
	}
	row, err := q.CreateAsset(ctx, sqlitegen.CreateAssetParams{
		ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID,
		ParentID: input.ParentID, Name: input.Name, Kind: core.AssetKindFile, MediaType: persisted.MediaType,
		ContentHash: persisted.ContentHash, SizeBytes: persisted.SizeBytes, Visibility: input.Visibility,
		SourceType: core.AssetSourceUpload, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return core.Asset{}, mapAssetConflict(err, input.Name)
	}
	asset, err := assetFromRow(row)
	if err != nil {
		return core.Asset{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Asset{}, err
	}
	return asset, nil
}

func (s *Store) ListAssets(ctx context.Context, scope core.Scope, projectID, parentID, query, kind string, limit int, quotaBytes int64) (core.AssetCollection, error) {
	base := struct {
		OrganizationID string
		ProjectID      string
		UserID         string
		OwnerUserID    string
		Limit          int64
	}{scope.OrganizationID, projectID, scope.UserID, scope.UserID, int64(limit)}
	var rows []sqlitegen.Asset
	var err error
	switch {
	case query != "" && kind != "":
		rows, err = s.queries.SearchAssetsByKindForUser(ctx, sqlitegen.SearchAssetsByKindForUserParams{OrganizationID: base.OrganizationID, ProjectID: base.ProjectID, LOWER: query, Kind: kind, UserID: base.UserID, OwnerUserID: base.OwnerUserID, Limit: base.Limit})
	case query != "":
		rows, err = s.queries.SearchAssetsForUser(ctx, sqlitegen.SearchAssetsForUserParams{OrganizationID: base.OrganizationID, ProjectID: base.ProjectID, LOWER: query, UserID: base.UserID, OwnerUserID: base.OwnerUserID, Limit: base.Limit})
	case kind != "":
		rows, err = s.queries.ListAssetsByParentAndKindForUser(ctx, sqlitegen.ListAssetsByParentAndKindForUserParams{OrganizationID: base.OrganizationID, ProjectID: base.ProjectID, ParentID: parentID, Kind: kind, UserID: base.UserID, OwnerUserID: base.OwnerUserID, Limit: base.Limit})
	default:
		rows, err = s.queries.ListAssetsByParentForUser(ctx, sqlitegen.ListAssetsByParentForUserParams{OrganizationID: base.OrganizationID, ProjectID: base.ProjectID, ParentID: parentID, UserID: base.UserID, OwnerUserID: base.OwnerUserID, Limit: base.Limit})
	}
	if err != nil {
		return core.AssetCollection{}, err
	}
	items := make([]core.Asset, 0, len(rows))
	for _, row := range rows {
		asset, convertErr := assetFromRow(row)
		if convertErr != nil {
			return core.AssetCollection{}, convertErr
		}
		items = append(items, asset)
	}
	visibleBytes, err := s.queries.SumVisibleAssetBytesForUser(ctx, sqlitegen.SumVisibleAssetBytesForUserParams{OrganizationID: scope.OrganizationID, ProjectID: projectID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.AssetCollection{}, err
	}
	return core.AssetCollection{Items: items, VisibleBytes: visibleBytes, QuotaBytes: quotaBytes}, nil
}

func (s *Store) OpenAssetContent(ctx context.Context, scope core.Scope, assetID string) (core.Asset, io.ReadCloser, error) {
	row, err := s.queries.GetAssetBlobForUser(ctx, sqlitegen.GetAssetBlobForUserParams{ID: assetID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Asset{}, nil, err
	}
	asset, err := assetFromFields(row.ID, row.OrganizationID, row.ProjectID, row.OwnerUserID, row.ParentID, row.Name, row.Kind, row.MediaType, row.ContentHash, row.SizeBytes, row.Visibility, row.SourceType, row.SourceArtifactID, row.CreatedAt, row.UpdatedAt)
	if err != nil {
		return core.Asset{}, nil, err
	}
	content, err := s.blobStore.Open(ctx, row.StorageKey, asset.SizeBytes)
	if err != nil {
		return core.Asset{}, nil, err
	}
	return asset, content, nil
}

func (s *Store) OpenArtifactContent(ctx context.Context, scope core.Scope, artifactID string) (core.Artifact, io.ReadCloser, error) {
	row, err := s.queries.GetArtifactBlobForUser(ctx, sqlitegen.GetArtifactBlobForUserParams{ID: artifactID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Artifact{}, nil, err
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.Artifact{}, nil, err
	}
	artifact := core.Artifact{ID: row.ID, ProjectID: row.ProjectID, TaskID: row.TaskID, RunID: row.RunID, StepID: row.StepID, Name: row.Name, Kind: row.Kind, MediaType: row.MediaType, ContentHash: row.ContentHash, SizeBytes: row.SizeBytes, Visibility: row.Visibility, SourceType: row.SourceType, CreatedAt: createdAt}
	content, err := s.blobStore.Open(ctx, row.StorageKey, artifact.SizeBytes)
	if err != nil {
		return core.Artifact{}, nil, err
	}
	return artifact, content, nil
}

func (s *Store) PublishArtifact(ctx context.Context, scope core.Scope, artifactID string, input core.PublishArtifactInput, quotaBytes int64, idempotency core.Idempotency) (core.Asset, error) {
	route := "POST /artifacts/" + artifactID + "/publish"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Asset
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Asset{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.Asset
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	artifact, err := q.GetArtifactForUser(ctx, sqlitegen.GetArtifactForUserParams{ID: artifactID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Asset{}, err
	}
	if err := validateAssetParent(ctx, q, scope, artifact.ProjectID, input.ParentID, input.Visibility); err != nil {
		return core.Asset{}, err
	}
	used, err := q.SumProjectAssetBytes(ctx, sqlitegen.SumProjectAssetBytesParams{OrganizationID: scope.OrganizationID, ProjectID: artifact.ProjectID})
	if err != nil {
		return core.Asset{}, err
	}
	if used > quotaBytes-artifact.SizeBytes {
		return core.Asset{}, &core.CapacityExceededError{LimitBytes: quotaBytes}
	}
	id, err := newID()
	if err != nil {
		return core.Asset{}, err
	}
	now := formatTime(time.Now())
	row, err := q.CreateAsset(ctx, sqlitegen.CreateAssetParams{
		ID: id, OrganizationID: artifact.OrganizationID, ProjectID: artifact.ProjectID, OwnerUserID: scope.UserID,
		ParentID: input.ParentID, Name: input.Name, Kind: core.AssetKindFile, MediaType: artifact.MediaType,
		ContentHash: artifact.ContentHash, SizeBytes: artifact.SizeBytes, Visibility: input.Visibility,
		SourceType: core.AssetSourceArtifact, SourceArtifactID: artifact.ID, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return core.Asset{}, mapAssetConflict(err, input.Name)
	}
	asset, err := assetFromRow(row)
	if err != nil {
		return core.Asset{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, asset); err != nil {
		return core.Asset{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Asset{}, err
	}
	return asset, nil
}

func validateAssetParent(ctx context.Context, q *sqlitegen.Queries, scope core.Scope, projectID, parentID, visibility string) error {
	if parentID == "" {
		return nil
	}
	parent, err := q.GetAssetForUser(ctx, sqlitegen.GetAssetForUserParams{ID: parentID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return err
	}
	if parent.ProjectID != projectID || parent.Kind != core.AssetKindFolder {
		return &core.ValidationError{Field: "parent_id", Message: "must identify a visible folder in the same project"}
	}
	if visibilityRank(visibility) > visibilityRank(parent.Visibility) {
		return &core.ValidationError{Field: "visibility", Message: "must not be broader than the parent folder"}
	}
	return nil
}

func (s *Store) CreateProjectInvitation(ctx context.Context, scope core.Scope, projectID string, input core.CreateProjectInvitationInput, correlationID string, idempotency core.Idempotency) (core.ProjectInvitation, error) {
	route := "POST /projects/" + projectID + "/invitations"
	s.idempotencyMu.Lock()
	defer s.idempotencyMu.Unlock()
	if idempotency.Key != "" {
		var replay core.ProjectInvitation
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	project, err := s.queries.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID})
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	if project.Kind != core.ProjectKindTeam {
		return core.ProjectInvitation{}, &core.ValidationError{Field: "project_id", Message: "invitations require a team project"}
	}
	requester, err := s.queries.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	if requester.Role != core.ProjectRoleOwner {
		return core.ProjectInvitation{}, &core.ForbiddenError{Message: "only a project owner can create invitations"}
	}
	now := time.Now().UTC()
	var pendingCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM project_invitations WHERE project_id = ? AND lower(email) = lower(?) AND status = 'pending' AND expires_at > ?`, projectID, input.Email, formatTime(now)).Scan(&pendingCount); err != nil {
		return core.ProjectInvitation{}, err
	}
	if pendingCount > 0 {
		return core.ProjectInvitation{}, &core.ResourceConflictError{Resource: "project invitation", Reason: "a pending invitation already exists for this email"}
	}
	id, err := newID()
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	token, err := newID()
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := now.Add(time.Duration(input.ExpiresIn) * time.Second)
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_invitations (id, organization_id, project_id, invited_by, email, display_name, role, status, token_hash, accepted_user_id, expires_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, NULL, ?, ?, ?)`, id, scope.OrganizationID, projectID, scope.UserID, input.Email, input.DisplayName, input.Role, hex.EncodeToString(tokenHash[:]), formatTime(expiresAt), formatTime(now), formatTime(now)); err != nil {
		return core.ProjectInvitation{}, err
	}
	invitation := core.ProjectInvitation{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, InvitedBy: scope.UserID, Email: input.Email, DisplayName: input.DisplayName, Role: input.Role, Status: core.InvitationStatusPending, ExpiresAt: expiresAt, CreatedAt: now, UpdatedAt: now, InviteToken: token}
	detail, _ := json.Marshal(map[string]any{"email": input.Email, "role": input.Role, "expires_at": expiresAt})
	auditID, err := newID()
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'project.invitation_created', 'project_invitation', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, projectID, scope.UserID, id, string(detail), correlationID, formatTime(now)); err != nil {
		return core.ProjectInvitation{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, invitation); err != nil {
		return core.ProjectInvitation{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ProjectInvitation{}, err
	}
	return invitation, nil
}

func (s *Store) ListProjectInvitations(ctx context.Context, scope core.Scope, projectID string) ([]core.ProjectInvitation, error) {
	project, err := s.queries.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID})
	if err != nil {
		return nil, err
	}
	if project.Kind != core.ProjectKindTeam {
		return nil, &core.ValidationError{Field: "project_id", Message: "invitations require a team project"}
	}
	requester, err := s.queries.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return nil, err
	}
	if requester.Role != core.ProjectRoleOwner {
		return nil, &core.ForbiddenError{Message: "only a project owner can list invitations"}
	}
	now := formatTime(time.Now())
	_, _ = s.db.ExecContext(ctx, `UPDATE project_invitations SET status = 'expired', updated_at = ? WHERE project_id = ? AND organization_id = ? AND status = 'pending' AND expires_at <= ?`, now, projectID, scope.OrganizationID, now)
	rows, err := s.db.QueryContext(ctx, `SELECT id, organization_id, project_id, invited_by, email, display_name, role, status, accepted_user_id, expires_at, created_at, updated_at FROM project_invitations WHERE organization_id = ? AND project_id = ? ORDER BY updated_at DESC, id DESC LIMIT 100`, scope.OrganizationID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	invitations := make([]core.ProjectInvitation, 0)
	for rows.Next() {
		var item core.ProjectInvitation
		var expiresAt, createdAt, updatedAt string
		var acceptedUserID sql.NullString
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.InvitedBy, &item.Email, &item.DisplayName, &item.Role, &item.Status, &acceptedUserID, &expiresAt, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.AcceptedUserID = acceptedUserID.String
		item.ExpiresAt, err = parseTime(expiresAt)
		if err != nil {
			return nil, err
		}
		item.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = parseTime(updatedAt)
		if err != nil {
			return nil, err
		}
		invitations = append(invitations, item)
	}
	return invitations, rows.Err()
}

func (s *Store) AcceptProjectInvitation(ctx context.Context, token, correlationID string, idempotency core.Idempotency) (core.ProjectInvitation, error) {
	tokenHash := sha256.Sum256([]byte(token))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item core.ProjectInvitation
	var expiresAt, createdAt, updatedAt string
	var tokenHashStored string
	var acceptedUserID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, organization_id, project_id, invited_by, email, display_name, role, status, accepted_user_id, token_hash, expires_at, created_at, updated_at FROM project_invitations WHERE token_hash = ?`, hex.EncodeToString(tokenHash[:])).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.InvitedBy, &item.Email, &item.DisplayName, &item.Role, &item.Status, &acceptedUserID, &tokenHashStored, &expiresAt, &createdAt, &updatedAt)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	item.AcceptedUserID = acceptedUserID.String
	item.ExpiresAt, err = parseTime(expiresAt)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	item.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	item.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	if item.Status == core.InvitationStatusAccepted {
		return item, nil
	}
	if item.Status != core.InvitationStatusPending {
		return core.ProjectInvitation{}, &core.StateConflictError{Status: item.Status, Command: "accept invitation"}
	}
	now := time.Now().UTC()
	if !item.ExpiresAt.After(now) {
		_, _ = tx.ExecContext(ctx, `UPDATE project_invitations SET status = 'expired', updated_at = ? WHERE id = ?`, formatTime(now), item.ID)
		return core.ProjectInvitation{}, &core.StateConflictError{Status: core.InvitationStatusExpired, Command: "accept invitation"}
	}
	var userID, displayName string
	err = tx.QueryRowContext(ctx, `SELECT id, display_name FROM users WHERE lower(email) = lower(?)`, item.Email).Scan(&userID, &displayName)
	if errors.Is(err, sql.ErrNoRows) {
		userID, err = newID()
		if err != nil {
			return core.ProjectInvitation{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, display_name, email, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`, userID, item.DisplayName, item.Email, formatTime(now), formatTime(now)); err != nil {
			return core.ProjectInvitation{}, err
		}
		displayName = item.DisplayName
	} else if err != nil {
		return core.ProjectInvitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?) ON CONFLICT(organization_id, user_id) DO UPDATE SET status = 'active', updated_at = excluded.updated_at`, item.OrganizationID, userID, formatTime(now), formatTime(now)); err != nil {
		return core.ProjectInvitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?) ON CONFLICT(project_id, user_id) DO UPDATE SET role = excluded.role, status = 'active', updated_at = excluded.updated_at`, item.ProjectID, userID, item.Role, formatTime(now), formatTime(now)); err != nil {
		return core.ProjectInvitation{}, err
	}
	// The invitation token is the new member's one-time bootstrap credential.
	// Issue a short-lived session in the same transaction, but never persist or
	// replay the raw token through idempotency/list APIs.
	rawSessionBytes := make([]byte, 32)
	if _, err := rand.Read(rawSessionBytes); err != nil {
		return core.ProjectInvitation{}, err
	}
	rawSession := base64.RawURLEncoding.EncodeToString(rawSessionBytes)
	sessionHash := sha256.Sum256([]byte(rawSession))
	sessionID, err := newID()
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	sessionExpires := now.Add(8 * time.Hour)
	if _, err := tx.ExecContext(ctx, `INSERT INTO auth_sessions (id, organization_id, user_id, token_hash, expires_at, revoked_at, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, '', ?, ?)`, sessionID, item.OrganizationID, userID, hex.EncodeToString(sessionHash[:]), formatTime(sessionExpires), formatTime(now), formatTime(now)); err != nil {
		return core.ProjectInvitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE project_invitations SET status = 'accepted', accepted_user_id = ?, updated_at = ? WHERE id = ? AND status = 'pending'`, userID, formatTime(now), item.ID); err != nil {
		return core.ProjectInvitation{}, err
	}
	item.Status = core.InvitationStatusAccepted
	item.AcceptedUserID = userID
	item.UpdatedAt = now
	item.InviteToken = ""
	item.SessionToken = rawSession
	detail, _ := json.Marshal(map[string]any{"email": item.Email, "role": item.Role, "accepted_user_id": userID})
	auditID, err := newID()
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'project.invitation_accepted', 'project_invitation', ?, 'succeeded', ?, ?, ?)`, auditID, item.OrganizationID, item.ProjectID, userID, item.ID, string(detail), correlationID, formatTime(now)); err != nil {
		return core.ProjectInvitation{}, err
	}
	if idempotency.Key != "" {
		persisted := item
		persisted.SessionToken = ""
		_ = saveIdempotency(ctx, tx, core.Scope{OrganizationID: item.OrganizationID, UserID: userID}, "POST /invitations/accept", idempotency, httpStatusOK, persisted)
	}
	if err := tx.Commit(); err != nil {
		return core.ProjectInvitation{}, err
	}
	_ = displayName
	return item, nil
}

func (s *Store) RevokeProjectInvitation(ctx context.Context, scope core.Scope, invitationID, correlationID string, idempotency core.Idempotency) error {
	route := "POST /invitations/" + invitationID + "/revoke"
	s.idempotencyMu.Lock()
	defer s.idempotencyMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var projectID string
	if err := tx.QueryRowContext(ctx, `SELECT project_id FROM project_invitations WHERE id = ? AND organization_id = ?`, invitationID, scope.OrganizationID).Scan(&projectID); err != nil {
		return err
	}
	requester, err := s.queries.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return err
	}
	if requester.Role != core.ProjectRoleOwner {
		return &core.ForbiddenError{Message: "only a project owner can revoke invitations"}
	}
	now := formatTime(time.Now())
	result, err := tx.ExecContext(ctx, `UPDATE project_invitations SET status = 'revoked', updated_at = ? WHERE id = ? AND organization_id = ? AND status = 'pending'`, now, invitationID, scope.OrganizationID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return &core.StateConflictError{Status: "not_pending", Command: "revoke invitation"}
	}
	auditID, err := newID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'project.invitation_revoked', 'project_invitation', ?, 'succeeded', '{}', ?, ?)`, auditID, scope.OrganizationID, projectID, scope.UserID, invitationID, correlationID, now); err != nil {
		return err
	}
	if idempotency.Key != "" {
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, map[string]bool{"revoked": true}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func visibilityRank(value string) int {
	switch value {
	case core.VisibilityProject:
		return 2
	case core.VisibilitySelected:
		return 1
	default:
		return 0
	}
}

func mapAssetConflict(err error, name string) error {
	if strings.Contains(strings.ToLower(err.Error()), "unique constraint failed: assets.project_id, assets.parent_id, assets.name") {
		return &core.ResourceConflictError{Resource: "asset", Reason: "name " + name + " already exists in this folder"}
	}
	return err
}

func (s *Store) CreateTask(ctx context.Context, scope core.Scope, projectID string, input core.CreateTaskInput, configSnapshot map[string]any, correlationID string, idempotency core.Idempotency) (core.Task, error) {
	route := "POST /projects/" + projectID + "/tasks"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Task
		found, replayErr := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if replayErr != nil {
			return core.Task{}, replayErr
		}
		if found {
			return replay, nil
		}
	}
	taskID, err := newID()
	if err != nil {
		return core.Task{}, err
	}
	runID, err := newID()
	if err != nil {
		return core.Task{}, err
	}
	stepID, err := newID()
	if err != nil {
		return core.Task{}, err
	}
	coordinatorID := ""
	if input.Mode == core.ModeAgentTeam {
		coordinatorID, err = newID()
		if err != nil {
			return core.Task{}, err
		}
	}
	conversationID := strings.TrimSpace(input.ConversationID)
	if conversationID == "" {
		conversationID, err = newID()
		if err != nil {
			return core.Task{}, err
		}
	}
	messageID, err := newID()
	if err != nil {
		return core.Task{}, err
	}
	planItemID, err := newID()
	if err != nil {
		return core.Task{}, err
	}
	now := formatTime(time.Now())
	snapshot, err := json.Marshal(configSnapshot)
	if err != nil {
		return core.Task{}, fmt.Errorf("encode config snapshot: %w", err)
	}
	stepInputValue := map[string]any{"goal": input.Goal, "acceptance": input.Acceptance, "mode": input.Mode, "stage": "plan", "role": "coordinator"}
	if input.SkillID != "" {
		stepInputValue["invoked_skill_ids"] = []string{input.SkillID}
		stepInputValue["skill_args"] = input.SkillArgs
		stepInputValue["skill_source"] = "user"
	}
	stepInput, err := json.Marshal(stepInputValue)
	if err != nil {
		return core.Task{}, fmt.Errorf("encode step input: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.Task
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil {
			return core.Task{}, replayErr
		}
		if found {
			return replay, nil
		}
	}
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.Task{}, err
	}
	if input.ConversationID == "" {
		if _, err := q.CreateConversation(ctx, sqlitegen.CreateConversationParams{
			ID: conversationID, OrganizationID: scope.OrganizationID, ProjectID: projectID,
			OwnerUserID: scope.UserID, Title: input.Title, Visibility: input.Visibility,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return core.Task{}, fmt.Errorf("create conversation: %w", err)
		}
	} else {
		var existingProjectID, existingOwnerID, existingVisibility string
		if err := tx.QueryRowContext(ctx, `
			SELECT c.project_id, c.owner_user_id, c.visibility
			FROM conversations c
			JOIN memberships m ON m.organization_id = c.organization_id
			JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = m.user_id
			WHERE c.id = ? AND c.organization_id = ? AND m.user_id = ? AND m.status = 'active'
			  AND pm.status = 'active' AND (c.visibility = 'project' OR c.owner_user_id = ?)
		`, conversationID, scope.OrganizationID, scope.UserID, scope.UserID).Scan(&existingProjectID, &existingOwnerID, &existingVisibility); err != nil {
			return core.Task{}, err
		}
		if existingProjectID != projectID || (existingVisibility != input.Visibility && strings.TrimSpace(input.Visibility) != "") {
			return core.Task{}, &core.ForbiddenError{Message: "conversation scope does not match task visibility"}
		}
	}
	if _, _, err := s.appendSessionMessageTx(ctx, tx, q, sessionMessageInput{
		MessageID:      messageID,
		OrganizationID: scope.OrganizationID,
		ProjectID:      projectID,
		ConversationID: conversationID,
		OwnerUserID:    scope.UserID,
		TaskID:         taskID,
		Role:           "user",
		Visibility:     input.Visibility,
		Content:        input.Goal,
		Now:            now,
	}); err != nil {
		return core.Task{}, fmt.Errorf("append task user message: %w", err)
	}
	row, err := q.CreateTask(ctx, sqlitegen.CreateTaskParams{
		ID:             taskID,
		OrganizationID: scope.OrganizationID,
		ProjectID:      projectID,
		OwnerUserID:    scope.UserID,
		Title:          input.Title,
		Goal:           input.Goal,
		Acceptance:     input.Acceptance,
		ConversationID: conversationID,
		Mode:           input.Mode,
		Visibility:     input.Visibility,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		return core.Task{}, fmt.Errorf("create task: %w", err)
	}
	planRank, err := q.NextPlanItemRank(ctx, sqlitegen.NextPlanItemRankParams{ProjectID: projectID, Status: core.PlanStatusTodo})
	if err != nil {
		return core.Task{}, fmt.Errorf("allocate plan item rank: %w", err)
	}
	if _, err := q.CreatePlanItem(ctx, sqlitegen.CreatePlanItemParams{
		ID: planItemID, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID,
		Title: input.Title, Description: input.Goal, Status: core.PlanStatusTodo, Source: core.PlanSourceTask,
		LinkedTaskID: taskID, Visibility: input.Visibility, Rank: planRank, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		return core.Task{}, fmt.Errorf("create task plan item: %w", err)
	}
	if _, err := q.CreateRun(ctx, sqlitegen.CreateRunParams{ID: runID, OrganizationID: scope.OrganizationID, ProjectID: projectID, TaskID: taskID, ConfigSnapshotJson: string(snapshot), CreatedAt: now, UpdatedAt: now}); err != nil {
		return core.Task{}, fmt.Errorf("create run: %w", err)
	}
	if err := freezeRunToolSnapshots(ctx, tx, runID, configSnapshot, now); err != nil {
		return core.Task{}, fmt.Errorf("freeze run tool snapshots: %w", err)
	}
	runPlanRevisionID, runPlanItemIDs, err := createInitialRunPlan(ctx, tx, scope, projectID, taskID, runID, input, now)
	if err != nil {
		return core.Task{}, fmt.Errorf("create run plan: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runs SET
    requested_provider_id = ?, requested_model_id = ?,
    effective_provider_id = ?, effective_model_id = ?,
    thinking_level = ?, model_fallback_reason = ?, model_catalog_version = ?
WHERE id = ?`,
		snapshotString(configSnapshot, "requested_provider_id"),
		snapshotString(configSnapshot, "requested_model_id"),
		snapshotString(configSnapshot, "effective_provider_id"),
		snapshotString(configSnapshot, "effective_model_id"),
		snapshotString(configSnapshot, "thinking_level"),
		snapshotString(configSnapshot, "model_fallback_reason"),
		snapshotString(configSnapshot, "model_catalog_version"),
		runID); err != nil {
		return core.Task{}, fmt.Errorf("freeze run model snapshot: %w", err)
	}
	// Zero is the durable representation of an unlimited dimension. Limits are
	// opt-in and frozen into the Run snapshot only when the user sets them.
	maxModelCalls, maxTokens := int64(0), int64(0)
	if value, ok := configSnapshot["max_model_calls"].(int64); ok && value > 0 {
		maxModelCalls = value
	}
	if value, ok := configSnapshot["max_tokens"].(int64); ok && value > 0 {
		maxTokens = value
	}
	if value, ok := configSnapshot["max_model_calls"].(float64); ok && value > 0 {
		maxModelCalls = int64(value)
	}
	if value, ok := configSnapshot["max_tokens"].(float64); ok && value > 0 {
		maxTokens = int64(value)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO run_budgets (run_id, max_model_calls, max_tokens, status, created_at, updated_at) VALUES (?, ?, ?, 'active', ?, ?)`, runID, maxModelCalls, maxTokens, now, now); err != nil {
		return core.Task{}, fmt.Errorf("create run budget: %w", err)
	}
	if input.Mode == core.ModeAgentTeam {
		if err := q.CreateAgentTeamStream(ctx, runID); err != nil {
			return core.Task{}, fmt.Errorf("create agent team stream: %w", err)
		}
		if _, err := q.CreateAgentTeamMember(ctx, sqlitegen.CreateAgentTeamMemberParams{ID: coordinatorID, OrganizationID: scope.OrganizationID, ProjectID: projectID, TaskID: taskID, RunID: runID, Role: "coordinator", DisplayName: "Coordinator", Status: "planned", CurrentStepID: stepID, CreatedAt: now, UpdatedAt: now}); err != nil {
			return core.Task{}, fmt.Errorf("create coordinator: %w", err)
		}
		if _, err := q.CreateAgentStep(ctx, sqlitegen.CreateAgentStepParams{ID: stepID, OrganizationID: scope.OrganizationID, ProjectID: projectID, RunID: runID, StepType: core.StepAgentCoordinatorPlan, Position: 1, InputJson: string(stepInput), AgentID: coordinatorID, CreatedAt: now, UpdatedAt: now}); err != nil {
			return core.Task{}, fmt.Errorf("create coordinator step: %w", err)
		}
	} else if _, err := q.CreateStep(ctx, sqlitegen.CreateStepParams{ID: stepID, OrganizationID: scope.OrganizationID, ProjectID: projectID, RunID: runID, StepType: "model_response", Position: 1, InputJson: string(stepInput), CreatedAt: now, UpdatedAt: now}); err != nil {
		return core.Task{}, fmt.Errorf("create step: %w", err)
	}
	if len(runPlanItemIDs) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, runPlanRevisionID, runPlanItemIDs[0], stepID); err != nil {
			return core.Task{}, fmt.Errorf("bind initial step to run plan: %w", err)
		}
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: scope.OrganizationID, ProjectID: projectID, RunID: runID,
		EventType: "plan.published", ActorType: "system", ActorID: scope.UserID,
		CorrelationID: correlationID, Visibility: input.Visibility,
		Payload: map[string]any{"plan_revision_id": runPlanRevisionID, "revision": 1, "trigger": "initial", "goal_summary": input.Goal, "items": runPlanItemIDs},
	}); err != nil {
		return core.Task{}, err
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: scope.OrganizationID,
		ProjectID:      projectID,
		RunID:          runID,
		EventType:      "run.created",
		ActorType:      "user",
		ActorID:        scope.UserID,
		CorrelationID:  correlationID,
		Visibility:     input.Visibility,
		Payload:        map[string]any{"task_id": taskID, "step_id": stepID, "mode": input.Mode, "requested_mode": configSnapshot["requested_mode"], "classification_reason": configSnapshot["classification_reason"], "permission_mode": configSnapshot["permission_mode"], "agent_name": runAgentDisplayName(configSnapshot), "status": core.StatusQueued},
	}); err != nil {
		return core.Task{}, err
	}
	if input.SkillID != "" {
		skillName := input.SkillID
		if invocation, ok := configSnapshot["explicit_skill_invocation"].(map[string]any); ok {
			if value, ok := invocation["skill_name"].(string); ok && strings.TrimSpace(value) != "" {
				skillName = value
			}
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: scope.OrganizationID, ProjectID: projectID, RunID: runID, EventType: "skill.invoked", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: input.Visibility, Payload: map[string]any{"skill_id": input.SkillID, "skill_name": skillName, "source": "user", "status": "queued", "step_id": stepID, "has_args": input.SkillArgs != ""}}); err != nil {
			return core.Task{}, err
		}
	}
	if input.Mode == core.ModeAgentTeam {
		workerCount := 3
		if len(input.TeamRoles) > 0 {
			workerCount = len(input.TeamRoles)
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: scope.OrganizationID, ProjectID: projectID, RunID: runID, EventType: "agent_team.created", ActorType: "agent", ActorID: coordinatorID, CorrelationID: correlationID, Visibility: input.Visibility, Payload: map[string]any{"coordinator_id": coordinatorID, "max_workers": workerCount, "write_guard": "coordinator_only"}}); err != nil {
			return core.Task{}, err
		}
	}
	task, err := taskFromRow(row, runID)
	if err != nil {
		return core.Task{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusAccepted, task); err != nil {
		return core.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Task{}, err
	}
	return task, nil
}

func snapshotString(snapshot map[string]any, key string) string {
	value, _ := snapshot[key].(string)
	return strings.TrimSpace(value)
}

func runAgentDisplayName(snapshot map[string]any) string {
	if agent, ok := snapshot["agent"].(map[string]any); ok {
		if name, ok := agent["name"].(string); ok && strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
	}
	return "Oktapod"
}

func createInitialRunPlan(ctx context.Context, tx *sql.Tx, scope core.Scope, projectID, taskID, runID string, input core.CreateTaskInput, now string) (string, []string, error) {
	revisionID, err := newID()
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_plan_revisions (
			id, organization_id, project_id, task_id, run_id, revision, trigger,
			goal_summary, status, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, 1, 'initial', ?, 'active', ?, ?)`,
		revisionID, scope.OrganizationID, projectID, taskID, runID, truncateText(strings.TrimSpace(input.Goal), 1000), scope.UserID, now,
	); err != nil {
		return "", nil, err
	}
	items := []struct {
		title       string
		description string
		status      string
	}{
		{"正在制定执行计划", "模型正在根据本次目标、会话上下文和可用能力生成完整语义子任务。", core.RunPlanItemRunning},
	}
	ids := make([]string, 0, len(items))
	for index, item := range items {
		itemID, idErr := newID()
		if idErr != nil {
			return "", nil, idErr
		}
		ids = append(ids, itemID)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO run_plan_items (
				id, organization_id, project_id, task_id, run_id, plan_revision_id,
				position, title, description, status, started_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, scope.OrganizationID, projectID, taskID, runID, revisionID, index+1,
			item.title, item.description, item.status, func() string {
				if item.status == core.RunPlanItemRunning {
					return now
				}
				return ""
			}(), now, now,
		); err != nil {
			return "", nil, err
		}
	}
	return revisionID, ids, nil
}

func (s *Store) ControlTask(ctx context.Context, scope core.Scope, taskID string, input core.ControlTaskInput, correlationID string, idempotency core.Idempotency) (core.Task, error) {
	route := "POST /tasks/" + taskID + "/commands"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Task
		found, replayErr := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if replayErr != nil {
			return core.Task{}, replayErr
		}
		if found {
			return replay, nil
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Task{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.Task
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil {
			return core.Task{}, replayErr
		}
		if found {
			return replay, nil
		}
	}
	row, err := q.GetTaskForUser(ctx, sqlitegen.GetTaskForUserParams{ID: taskID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Task{}, err
	}
	current, err := taskFromGetRow(row)
	if err != nil {
		return core.Task{}, err
	}
	targetStatus, eventType, changed, err := taskTransition(current.Status, input.Command)
	if err != nil {
		return core.Task{}, err
	}
	if changed && input.Command == core.TaskCommandRetry {
		if current.ErrorCode == "BUDGET_RESERVATION_EXCEEDED" || current.ErrorCode == "BUDGET_EXCEEDED" {
			var budgetStatus string
			if err := tx.QueryRowContext(ctx, `SELECT status FROM run_budgets WHERE run_id = ?`, current.RunID).Scan(&budgetStatus); err != nil {
				return core.Task{}, err
			}
			if budgetStatus == "exhausted" {
				return core.Task{}, &core.ResourceConflictError{Resource: "task", Reason: "increase this Run budget before retry"}
			}
		}
		var unknown int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM side_effect_receipts WHERE run_id = ? AND status = 'unknown'`, current.RunID).Scan(&unknown); err != nil {
			return core.Task{}, err
		}
		if unknown > 0 {
			return core.Task{}, &core.ResourceConflictError{Resource: "task", Reason: "unknown side effect must be resolved before retry"}
		}
	}
	if changed {
		now := formatTime(time.Now())
		eventPayload := map[string]any{"task_id": taskID, "command": input.Command, "from_status": current.Status, "to_status": targetStatus}
		controlPayload, err := s.applyTaskStepControl(ctx, tx, current.RunID, input.Command, now, correlationID)
		if err != nil {
			return core.Task{}, err
		}
		for key, value := range controlPayload {
			eventPayload[key] = value
		}
		if err := q.UpdateTaskResult(ctx, sqlitegen.UpdateTaskResultParams{Status: targetStatus, ErrorCode: controlErrorCode(current, targetStatus), ResultSummary: current.ResultSummary, UpdatedAt: now, ID: taskID}); err != nil {
			return core.Task{}, err
		}
		if err := q.UpdateRunStatus(ctx, sqlitegen.UpdateRunStatusParams{Status: targetStatus, UpdatedAt: now, ID: current.RunID}); err != nil {
			return core.Task{}, err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: current.OrganizationID, ProjectID: current.ProjectID, RunID: current.RunID, EventType: eventType, ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: current.Visibility, Payload: eventPayload}); err != nil {
			return core.Task{}, err
		}
	}
	updatedRow, err := q.GetTaskForUser(ctx, sqlitegen.GetTaskForUserParams{ID: taskID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Task{}, err
	}
	updated, err := taskFromGetRow(updatedRow)
	if err != nil {
		return core.Task{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, 200, updated); err != nil {
		return core.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Task{}, err
	}
	return updated, nil
}

type taskControlStep struct {
	ID          string
	Position    int64
	Status      string
	InputJSON   string
	RetrySource string
	IsRetry     bool
}

// applyTaskStepControl keeps task commands checkpoint-scoped. In particular,
// retry must never reactivate every historical failure in a Run: doing so can
// replay model calls and side effects that have already been superseded by a
// persisted recovery Step.
func (s *Store) applyTaskStepControl(ctx context.Context, tx *sql.Tx, runID, command, now, correlationID string) (map[string]any, error) {
	switch command {
	case core.TaskCommandPause:
		_, err := tx.ExecContext(ctx, `
			UPDATE steps
			SET status = 'paused', owner_id = '', lease_until = '',
			    fence_token = CASE WHEN status = 'running' THEN fence_token + 1 ELSE fence_token END,
			    updated_at = ?
			WHERE run_id = ? AND status IN ('queued', 'running')`, now, runID)
		return nil, err
	case core.TaskCommandResume:
		_, err := tx.ExecContext(ctx, `
			UPDATE steps SET status = 'queued', owner_id = '', lease_until = '', updated_at = ?
			WHERE run_id = ? AND status = 'paused'`, now, runID)
		return nil, err
	case core.TaskCommandCancel:
		if _, err := tx.ExecContext(ctx, `
			UPDATE steps
			SET status = 'canceled', owner_id = '', lease_until = '',
			    fence_token = CASE WHEN status = 'running' THEN fence_token + 1 ELSE fence_token END,
			    error_code = CASE WHEN error_code = '' THEN 'TASK_CANCELED' ELSE error_code END,
			    updated_at = ?
			WHERE run_id = ? AND status IN ('queued', 'running', 'waiting_approval', 'waiting_input', 'paused', 'blocked')`, now, runID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'rejected', error_code = 'TASK_CANCELED', updated_at = ? WHERE run_id = ? AND status IN ('planned', 'waiting_approval', 'queued', 'running', 'blocked')`, now, runID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = 'system', decision_note = '任务已取消', decided_at = ? WHERE run_id = ? AND status = 'pending'`, now, runID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE human_action_requests
			SET status = 'superseded', response_json = '{"reason":"task_canceled"}',
			    decided_by = 'system', decided_at = ?, version = version + 1
			WHERE run_id = ? AND status IN ('waiting_user', 'reviewing')`, now, runID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'canceled', completed_at = ?, updated_at = ? WHERE run_id = ? AND status NOT IN ('completed', 'canceled')`, now, now, runID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'canceled' WHERE run_id = ? AND status = 'active'`, runID); err != nil {
			return nil, err
		}
		// Steering belongs to the aborted Run and must not be replayed later.
		// Follow-up/Next Turn items deliberately remain pending so the user's
		// next request starts after the cancellation reaches a terminal state.
		if _, err := tx.ExecContext(ctx, `
			UPDATE conversation_message_queue
			SET status = 'canceled'
			WHERE active_task_id = (SELECT task_id FROM runs WHERE id = ?)
			  AND kind = 'steering' AND status IN ('pending', 'claimed')`, runID); err != nil {
			return nil, err
		}
		return nil, nil
	case core.TaskCommandRetry:
		return s.retryTaskFromFrontier(ctx, tx, runID, now, correlationID)
	default:
		return nil, &core.ValidationError{Field: "command", Message: "is not supported"}
	}
}

func (s *Store) retryTaskFromFrontier(ctx context.Context, tx *sql.Tx, runID, now, correlationID string) (map[string]any, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, position, status, input_json FROM steps WHERE run_id = ? ORDER BY position, id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	steps := make([]taskControlStep, 0)
	for rows.Next() {
		var step taskControlStep
		if err := rows.Scan(&step.ID, &step.Position, &step.Status, &step.InputJSON); err != nil {
			return nil, err
		}
		step.RetrySource, step.IsRetry = retryStepMetadata(step.InputJSON)
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var frontier *taskControlStep
	for index := range steps {
		step := &steps[index]
		candidate := (step.IsRetry && retryControlStatus(step.Status)) ||
			step.Status == core.StatusFailed || step.Status == core.StatusBlocked ||
			step.Status == core.StatusQueued
		if !candidate {
			continue
		}
		if frontier == nil || step.Position > frontier.Position {
			frontier = step
		}
	}
	if frontier != nil {
		if repair, repaired, err := s.retryDeniedToolThroughModel(ctx, tx, runID, *frontier, now, correlationID); err != nil {
			return nil, err
		} else if repaired {
			return repair, nil
		}
	}
	if continuationStepID, batchID, resumed, resumeErr := s.resumeCompletedToolBatch(ctx, tx, runID, now, correlationID); resumeErr != nil {
		return nil, resumeErr
	} else if resumed {
		return map[string]any{
			"step_id":       continuationStepID,
			"retry_scope":   "completed_batch_continuation",
			"tool_batch_id": batchID,
		}, nil
	}
	if frontier == nil {
		return s.retryPlanControlFailure(ctx, tx, runID, steps, now)
	}

	// A previous broad retry may have left older recovery attempts active. They
	// are audit history, not runnable work, once a newer recovery frontier exists.
	superseded := 0
	if frontier.IsRetry {
		for _, step := range steps {
			if step.ID == frontier.ID || !step.IsRetry || step.Position >= frontier.Position || !activeControlStatus(step.Status) {
				continue
			}
			result, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'canceled', owner_id = '', lease_until = '', error_code = 'RETRY_SUPERSEDED', updated_at = ? WHERE id = ?`, now, step.ID)
			if err != nil {
				return nil, err
			}
			if count, countErr := result.RowsAffected(); countErr == nil {
				superseded += int(count)
			}
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE steps
		SET status = 'queued', owner_id = '', lease_until = '',
		    fence_token = CASE WHEN status = 'running' THEN fence_token + 1 ELSE fence_token END,
		    error_code = '', updated_at = ?
		WHERE id = ? AND run_id = ?`, now, frontier.ID, runID)
	if err != nil {
		return nil, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, &core.ResourceConflictError{Resource: "task", Reason: "retry checkpoint changed concurrently"}
	}
	// A Tool Step and its frozen ToolCall form one retry checkpoint. Resetting
	// only the Step leaves the ToolCall terminal, so BeginToolExecution rejects
	// the otherwise valid retry as TOOL_INPUT_CHANGED.
	if _, err := tx.ExecContext(ctx, `
		UPDATE tool_calls
		SET status = 'queued', error_code = '', completed_at = '', updated_at = ?
		WHERE step_id = ? AND run_id = ?`, now, frontier.ID, runID); err != nil {
		return nil, err
	}
	return map[string]any{"step_id": frontier.ID, "step_position": frontier.Position, "retry_scope": "frontier_only", "superseded_steps": superseded}, nil
}

func (s *Store) retryDeniedToolThroughModel(ctx context.Context, tx *sql.Tx, runID string, frontier taskControlStep, now, correlationID string) (map[string]any, bool, error) {
	var toolName, policyDecision, toolBatchID string
	err := tx.QueryRowContext(ctx, `
		SELECT tool_name, policy_decision, tool_batch_id
		FROM tool_calls
		WHERE run_id = ? AND step_id = ?
		ORDER BY position DESC LIMIT 1`, runID, frontier.ID).Scan(&toolName, &policyDecision, &toolBatchID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if policyDecision != core.PolicyDeny {
		err = tx.QueryRowContext(ctx, `
			SELECT tool_name
			FROM tool_calls
			WHERE run_id = ? AND tool_batch_id = ? AND policy_decision = 'deny'
			ORDER BY position LIMIT 1`, runID, toolBatchID).Scan(&toolName)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
	}
	var frontierInput map[string]any
	if json.Unmarshal([]byte(frontier.InputJSON), &frontierInput) != nil {
		return nil, false, fmt.Errorf("MODEL_RETRY_INPUT_INVALID: denied tool frontier input is not JSON")
	}
	sourceStepID, _ := frontierInput["model_source_step_id"].(string)
	if strings.TrimSpace(sourceStepID) == "" {
		return nil, false, &core.ResourceConflictError{Resource: "task", Reason: "denied tool has no model checkpoint"}
	}
	var sourceStep core.ClaimedStep
	sourceStep.ID = sourceStepID
	sourceStep.RunID = runID
	if err := tx.QueryRowContext(ctx, `
		SELECT organization_id, project_id, step_type, input_json, owner_id
		FROM steps WHERE id = ? AND run_id = ?`,
		sourceStepID, runID,
	).Scan(&sourceStep.OrganizationID, &sourceStep.ProjectID, &sourceStep.StepType, &sourceStep.InputJSON, &sourceStep.OwnerID); err != nil {
		return nil, false, err
	}
	run, err := getRunContextTx(ctx, tx, runID)
	if err != nil {
		return nil, false, err
	}
	recoveryStepID, retryCount, queued, err := s.queueModelInteractionRetry(ctx, tx, sourceStep, run, "TOOL_POLICY_DENIED", now)
	if err != nil {
		return nil, false, err
	}
	if !queued {
		return nil, false, &core.ResourceConflictError{Resource: "task", Reason: "policy repair retry limit reached"}
	}
	planRevisionID, _ := frontierInput["plan_revision_id"].(string)
	planItemID, _ := frontierInput["run_plan_item_id"].(string)
	var recoveryInputJSON string
	if err := tx.QueryRowContext(ctx, `SELECT input_json FROM steps WHERE id = ?`, recoveryStepID).Scan(&recoveryInputJSON); err != nil {
		return nil, false, err
	}
	var recoveryInput map[string]any
	if json.Unmarshal([]byte(recoveryInputJSON), &recoveryInput) != nil {
		return nil, false, fmt.Errorf("MODEL_RETRY_INPUT_INVALID: policy repair input is not JSON")
	}
	recoveryInput["manual_retry"] = true
	recoveryInput["model_policy_denials"] = []map[string]any{{
		"tool_name": toolName,
		"reason":    "该工具或参数被确定性策略禁止，不能通过审批放行",
	}}
	encodedRecoveryInput, err := json.Marshal(recoveryInput)
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps
		SET input_json = ?, plan_revision_id = ?, run_plan_item_id = ?, updated_at = ?
		WHERE id = ?`, string(encodedRecoveryInput), planRevisionID, planItemID, now, recoveryStepID); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(planRevisionID) != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'active' WHERE id = ?`, planRevisionID); err != nil {
			return nil, false, err
		}
	}
	if strings.TrimSpace(planItemID) != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_plan_items
			SET status = 'running', completed_at = '', result_summary = '', updated_at = ?
			WHERE id = ? AND status = 'blocked'`, now, planItemID); err != nil {
			return nil, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps
		SET status = 'canceled', owner_id = '', lease_until = '',
		    error_code = 'POLICY_REPAIR_QUEUED', updated_at = ?
		WHERE tool_batch_id = ? AND run_id = ? AND status = 'blocked'`, now, toolBatchID, runID); err != nil {
		return nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tool_calls
		SET status = 'rejected',
		    error_code = CASE WHEN policy_decision = 'deny' THEN 'POLICY_REPAIR_QUEUED' ELSE 'PLAN_SUPERSEDED' END,
		    updated_at = ?
		WHERE tool_batch_id = ? AND run_id = ? AND status = 'blocked'`, now, toolBatchID, runID); err != nil {
		return nil, false, err
	}
	q := s.queries.WithTx(tx)
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "model.retry_queued", ActorType: "system", ActorID: sourceStep.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{
			"step_id": recoveryStepID, "source_step_id": sourceStep.ID,
			"retry_count": retryCount, "max_retries": core.DefaultModelRetryLimit,
			"error_code": "TOOL_POLICY_DENIED", "manual_retry": true,
			"policy_denials": []map[string]any{{"tool_name": toolName}},
		},
	}); err != nil {
		return nil, false, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT position FROM steps WHERE id = ?`, recoveryStepID).Scan(&position); err != nil {
		return nil, false, err
	}
	return map[string]any{
		"step_id": recoveryStepID, "step_position": position,
		"retry_scope": "model_policy_repair", "source_step_id": sourceStep.ID,
		"plan_revision_id": planRevisionID, "run_plan_item_id": planItemID,
	}, true, nil
}

func (s *Store) resumeCompletedToolBatch(ctx context.Context, tx *sql.Tx, runID, now, correlationID string) (continuationStepID, batchID string, resumed bool, err error) {
	var sourceStep core.ClaimedStep
	err = tx.QueryRowContext(ctx, `
		SELECT tb.id, source.id, source.step_type, source.input_json, source.owner_id
		FROM tool_batches tb
		JOIN tool_calls calls ON calls.tool_batch_id = tb.id
		JOIN steps source ON source.id = (
			SELECT latest.step_id
			FROM tool_calls latest
			WHERE latest.tool_batch_id = tb.id
			ORDER BY latest.batch_index DESC, latest.position DESC
			LIMIT 1
		)
		WHERE tb.run_id = ? AND tb.continuation_claimed = 0
		GROUP BY tb.id, source.id, source.step_type, source.input_json, source.owner_id, tb.created_at
		HAVING COUNT(*) = SUM(CASE WHEN calls.status = 'succeeded' THEN 1 ELSE 0 END)
		ORDER BY tb.created_at DESC
		LIMIT 1`, runID,
	).Scan(&batchID, &sourceStep.ID, &sourceStep.StepType, &sourceStep.InputJSON, &sourceStep.OwnerID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	run, err := getRunContextTx(ctx, tx, runID)
	if err != nil {
		return "", "", false, err
	}
	sourceStep.OrganizationID = run.OrganizationID
	sourceStep.ProjectID = run.ProjectID
	sourceStep.RunID = runID
	continuationStepID, err = s.queueModelContinuation(ctx, tx, s.queries.WithTx(tx), sourceStep, run, now, correlationID)
	if err != nil {
		return "", "", false, err
	}
	if continuationStepID == "" {
		return "", "", false, &core.ResourceConflictError{Resource: "task", Reason: "completed tool batch has no continuation history"}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tool_batches
		SET status = 'succeeded', continuation_claimed = 1,
		    continuation_step_id = ?, updated_at = ?
		WHERE id = ? AND continuation_claimed = 0`,
		continuationStepID, now, batchID,
	); err != nil {
		return "", "", false, err
	}
	return continuationStepID, batchID, true, nil
}

// retryPlanControlFailure recovers older task-level plan failures whose model
// step was persisted as succeeded before the plan update was rejected. Those
// runs have no failed Step frontier, but the active plan plus continuation
// history is still a valid durable checkpoint.
func (s *Store) retryPlanControlFailure(ctx context.Context, tx *sql.Tx, runID string, steps []taskControlStep, now string) (map[string]any, error) {
	var errorCode string
	if err := tx.QueryRowContext(ctx, `
		SELECT t.error_code
		FROM tasks t JOIN runs r ON r.task_id = t.id
		WHERE r.id = ?`, runID).Scan(&errorCode); err != nil {
		return nil, err
	}
	if errorCode != "RUN_PLAN_STATUS_REGRESSION" && !isRepairableRunPlanError(errorCode) {
		return nil, &core.ResourceConflictError{Resource: "task", Reason: "no retryable checkpoint remains; resolve the cause or fork a new task"}
	}

	run, err := getRunContextTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	var planRevisionID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM run_plan_revisions
		WHERE run_id = ? AND status IN ('active', 'blocked')
		ORDER BY revision DESC LIMIT 1`, runID).Scan(&planRevisionID); err != nil {
		return nil, err
	}
	var planItemID string
	if err := tx.QueryRowContext(ctx, `
		SELECT id FROM run_plan_items
		WHERE plan_revision_id = ? AND status IN ('running', 'blocked', 'waiting_user', 'pending')
		ORDER BY position LIMIT 1`, planRevisionID).Scan(&planItemID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'active' WHERE id = ?`, planRevisionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE run_plan_items
		SET status = 'running', completed_at = '', updated_at = ?
		WHERE id = ? AND status = 'blocked'`, now, planItemID); err != nil {
		return nil, err
	}

	history, err := s.collectContinuationHistory(ctx, tx, run)
	if err != nil {
		return nil, err
	}
	sourceStepID := ""
	for index := len(steps) - 1; index >= 0; index-- {
		if steps[index].ID != "" {
			sourceStepID = steps[index].ID
			break
		}
	}
	recoveryInput := map[string]any{
		"history":                    history,
		"stage":                      "plan_execute",
		"source_step_id":             sourceStepID,
		"plan_revision_id":           planRevisionID,
		"model_retry_count":          1,
		"model_retry_reason":         errorCode,
		"model_retry_source_step_id": sourceStepID,
		"manual_retry":               true,
	}
	stepID, err := insertQueuedModelStep(ctx, tx, run, recoveryInput, now)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ?
		WHERE id = ?`, planRevisionID, planItemID, stepID); err != nil {
		return nil, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT position FROM steps WHERE id = ?`, stepID).Scan(&position); err != nil {
		return nil, err
	}
	return map[string]any{
		"step_id":          stepID,
		"step_position":    position,
		"retry_scope":      "plan_recovery",
		"plan_revision_id": planRevisionID,
		"run_plan_item_id": planItemID,
	}, nil
}

func retryStepMetadata(inputJSON string) (source string, retry bool) {
	var input map[string]any
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		return "", false
	}
	for _, key := range []string{"model_retry_source_step_id", "model_compat_retry_source_step_id", "tool_recovery_source_step_id"} {
		if value, ok := input[key].(string); ok && strings.TrimSpace(value) != "" {
			return value, true
		}
	}
	for _, key := range []string{"model_retry_count", "model_compat_retry_count", "tool_recovery_count", "retry_count"} {
		if value, ok := numericToolValue(input[key]); ok && value > 0 {
			return "", true
		}
	}
	return "", false
}

func retryControlStatus(status string) bool {
	return activeControlStatus(status) || status == core.StatusFailed
}

func activeControlStatus(status string) bool {
	switch status {
	case core.StatusQueued, core.StatusRunning, core.StatusPaused, core.StatusBlocked:
		return true
	default:
		return false
	}
}

func (s *Store) AcceptTaskOutcome(ctx context.Context, scope core.Scope, taskID, note, correlationID string, idempotency core.Idempotency) (core.AcceptedOutcome, error) {
	route := "POST /tasks/" + taskID + "/acceptance"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.AcceptedOutcome
		found, replayErr := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if replayErr != nil {
			return core.AcceptedOutcome{}, replayErr
		}
		if found {
			return replay, nil
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if idempotency.Key != "" {
		var replay core.AcceptedOutcome
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil {
			return core.AcceptedOutcome{}, replayErr
		}
		if found {
			return replay, nil
		}
	}
	taskRow, err := q.GetTaskForUser(ctx, sqlitegen.GetTaskForUserParams{ID: taskID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	task, err := taskFromGetRow(taskRow)
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	if task.Status != core.StatusSucceeded {
		return core.AcceptedOutcome{}, &core.StateConflictError{Status: task.Status, Command: "accept"}
	}
	existing, err := q.GetAcceptedOutcomeByRun(ctx, task.RunID)
	if err == nil {
		outcome, convertErr := acceptedOutcomeFromRow(existing)
		if convertErr != nil {
			return core.AcceptedOutcome{}, convertErr
		}
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, outcome); err != nil {
			return core.AcceptedOutcome{}, err
		}
		if err := tx.Commit(); err != nil {
			return core.AcceptedOutcome{}, err
		}
		return outcome, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return core.AcceptedOutcome{}, err
	}
	outcomeID, err := newID()
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	now := formatTime(time.Now())
	row, err := q.CreateAcceptedOutcome(ctx, sqlitegen.CreateAcceptedOutcomeParams{ID: outcomeID, OrganizationID: task.OrganizationID, ProjectID: task.ProjectID, TaskID: task.ID, RunID: task.RunID, AcceptedBy: scope.UserID, Note: note, CreatedAt: now})
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	outcome, err := acceptedOutcomeFromRow(row)
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: task.OrganizationID, ProjectID: task.ProjectID, RunID: task.RunID, EventType: "outcome.accepted", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: task.Visibility, Payload: map[string]any{"accepted_outcome_id": outcome.ID, "task_id": task.ID, "run_id": task.RunID}}); err != nil {
		return core.AcceptedOutcome{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, outcome); err != nil {
		return core.AcceptedOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.AcceptedOutcome{}, err
	}
	return outcome, nil
}

func taskTransition(status, command string) (target, eventType string, changed bool, err error) {
	switch command {
	case core.TaskCommandPause:
		if status == core.StatusPaused {
			return status, "", false, nil
		}
		if status != core.StatusQueued && status != core.StatusRunning {
			return "", "", false, &core.StateConflictError{Status: status, Command: command}
		}
		return core.StatusPaused, "run.paused", true, nil
	case core.TaskCommandResume:
		if status == core.StatusQueued || status == core.StatusRunning {
			return status, "", false, nil
		}
		if status != core.StatusPaused {
			return "", "", false, &core.StateConflictError{Status: status, Command: command}
		}
		return core.StatusQueued, "run.resumed", true, nil
	case core.TaskCommandRetry:
		if status != core.StatusFailed && status != core.StatusBlocked {
			return "", "", false, &core.StateConflictError{Status: status, Command: command}
		}
		return core.StatusQueued, "run.retried", true, nil
	case core.TaskCommandCancel:
		if status == core.StatusCanceled {
			return status, "", false, nil
		}
		if status == core.StatusSucceeded || status == core.StatusFailed {
			return "", "", false, &core.StateConflictError{Status: status, Command: command}
		}
		return core.StatusCanceled, "run.canceled", true, nil
	default:
		return "", "", false, &core.ValidationError{Field: "command", Message: "is not supported"}
	}
}

func controlErrorCode(current core.Task, targetStatus string) string {
	if targetStatus == core.StatusQueued {
		return ""
	}
	return current.ErrorCode
}

func (s *Store) GetTask(ctx context.Context, scope core.Scope, taskID string) (core.Task, error) {
	row, err := s.queries.GetTaskForUser(ctx, sqlitegen.GetTaskForUserParams{ID: taskID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Task{}, err
	}
	return taskFromGetRow(row)
}

func (s *Store) ListTasks(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Task, error) {
	rows, err := s.queries.ListTasksForUser(ctx, sqlitegen.ListTasksForUserParams{OrganizationID: scope.OrganizationID, ProjectID: projectID, UserID: scope.UserID, OwnerUserID: scope.UserID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	tasks := make([]core.Task, 0, len(rows))
	for _, row := range rows {
		task, convertErr := taskFromListRow(row)
		if convertErr != nil {
			return nil, convertErr
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func (s *Store) ListRunEvents(ctx context.Context, scope core.Scope, runID string, after int64, limit int) ([]core.RunEvent, error) {
	rows, err := s.queries.ListRunEventsForUser(ctx, sqlitegen.ListRunEventsForUserParams{RunID: runID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID, StreamSeq: after, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	events := make([]core.RunEvent, 0, len(rows))
	for _, row := range rows {
		occurredAt, parseErr := parseTime(row.OccurredAt)
		if parseErr != nil {
			return nil, parseErr
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(row.PayloadJson), &payload); err != nil {
			return nil, fmt.Errorf("decode event %s: %w", row.ID, err)
		}
		if row.EventType == "tool.completed" {
			s.enrichToolCompletedEvent(ctx, row.RunID, payload)
		}
		events = append(events, core.RunEvent{ID: row.ID, RunID: row.RunID, StreamSeq: row.StreamSeq, EventType: row.EventType, OccurredAt: occurredAt, Payload: payload})
	}
	return events, nil
}

func (s *Store) GetActiveRunPlan(ctx context.Context, scope core.Scope, runID string) (core.RunPlanRevision, error) {
	var plan core.RunPlanRevision
	var createdAt, supersededAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT p.id, p.organization_id, p.project_id, p.task_id, p.run_id, p.revision,
		       p.trigger, p.goal_summary, p.status, p.supersedes_revision_id,
		       p.created_by, p.created_at, p.superseded_at
		FROM run_plan_revisions p
		JOIN tasks t ON t.id = p.task_id
		JOIN project_memberships pm ON pm.project_id = p.project_id
		  AND pm.user_id = ? AND pm.status = 'active'
		WHERE p.run_id = ? AND p.organization_id = ?
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)
		ORDER BY CASE p.status WHEN 'active' THEN 0 ELSE 1 END, p.revision DESC
		LIMIT 1`,
		scope.UserID, runID, scope.OrganizationID, scope.UserID,
	).Scan(
		&plan.ID, &plan.OrganizationID, &plan.ProjectID, &plan.TaskID, &plan.RunID,
		&plan.Revision, &plan.Trigger, &plan.GoalSummary, &plan.Status,
		&plan.SupersedesRevisionID, &plan.CreatedBy, &createdAt, &supersededAt,
	)
	if err != nil {
		return core.RunPlanRevision{}, err
	}
	plan.CreatedAt = mustParseTime(createdAt)
	if supersededAt != "" {
		value := mustParseTime(supersededAt)
		plan.SupersededAt = &value
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, plan_revision_id, position, title, description, status,
		       result_summary, started_at, completed_at, created_at, updated_at
		FROM run_plan_items WHERE plan_revision_id = ? ORDER BY position`, plan.ID)
	if err != nil {
		return core.RunPlanRevision{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item core.RunPlanItem
		var startedAt, completedAt, itemCreatedAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.PlanRevisionID, &item.Position, &item.Title,
			&item.Description, &item.Status, &item.ResultSummary, &startedAt,
			&completedAt, &itemCreatedAt, &updatedAt); err != nil {
			return core.RunPlanRevision{}, err
		}
		item.CreatedAt, item.UpdatedAt = mustParseTime(itemCreatedAt), mustParseTime(updatedAt)
		if startedAt != "" {
			value := mustParseTime(startedAt)
			item.StartedAt = &value
		}
		if completedAt != "" {
			value := mustParseTime(completedAt)
			item.CompletedAt = &value
		}
		plan.Items = append(plan.Items, item)
	}
	return plan, rows.Err()
}

func (s *Store) ListHumanActions(ctx context.Context, scope core.Scope, status, conversationID, runID string, limit int) ([]core.HumanAction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.organization_id, a.project_id, a.conversation_id, a.task_id,
		       a.run_id, a.step_id, a.plan_revision_id, a.run_plan_item_id,
		       a.kind, a.status, a.reviewer, a.risk_level, a.title, a.reason,
		       a.request_json, a.request_hash, a.response_json, a.requested_by,
		       a.decided_by, a.requested_at, a.expires_at, a.decided_at, a.version
		FROM human_action_requests a
		JOIN tasks t ON t.id = a.task_id
		JOIN project_memberships pm ON pm.project_id = a.project_id
		  AND pm.user_id = ? AND pm.status = 'active'
		WHERE a.organization_id = ?
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)
		  AND (? = 'all' OR a.status = ?)
		  AND (? = '' OR a.conversation_id = ?)
		  AND (? = '' OR a.run_id = ?)
		ORDER BY CASE a.status WHEN 'waiting_user' THEN 0 WHEN 'reviewing' THEN 1 ELSE 2 END,
		         a.requested_at DESC, a.id DESC
		LIMIT ?`,
		scope.UserID, scope.OrganizationID, scope.UserID,
		status, status, conversationID, conversationID, runID, runID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]core.HumanAction, 0)
	for rows.Next() {
		item, scanErr := scanHumanAction(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DecideHumanAction(ctx context.Context, scope core.Scope, actionID string, input core.DecideHumanActionInput, correlationID string, idempotency core.Idempotency) (core.HumanAction, error) {
	current, err := getHumanActionForUser(ctx, s.db, scope, actionID)
	if err != nil {
		return core.HumanAction{}, err
	}
	if current.Version != input.Version {
		return core.HumanAction{}, &core.StateConflictError{Status: current.Status, Command: input.Decision}
	}
	switch current.Kind {
	case core.HumanActionToolApproval:
		decision := input.Decision
		if decision != core.ApprovalApproved && decision != core.ApprovalRejected {
			return core.HumanAction{}, &core.ValidationError{Field: "decision", Message: "tool approval requires approved or rejected"}
		}
		if _, err := s.DecideApproval(ctx, scope, actionID, core.DecideApprovalInput{
			Decision: decision, Scope: input.Scope, Note: input.Note, Confirmed: input.Confirmed,
		}, correlationID, idempotency); err != nil {
			return core.HumanAction{}, err
		}
	case core.HumanActionSideEffectResolution:
		if _, err := s.ResolveSideEffect(ctx, scope, actionID, core.ResolveSideEffectInput{
			Decision: input.Decision, Note: input.Note,
		}, correlationID, idempotency); err != nil {
			return core.HumanAction{}, err
		}
	case core.HumanActionInputRequest, core.HumanActionPermissionEscalation, core.HumanActionBudgetRequest:
		if err := s.resolveInteractiveHumanAction(ctx, scope, current, input, correlationID); err != nil {
			return core.HumanAction{}, err
		}
	default:
		return core.HumanAction{}, &core.ValidationError{Field: "kind", Message: "human action kind is not actionable yet"}
	}
	return getHumanActionForUser(ctx, s.db, scope, actionID)
}

func (s *Store) resolveInteractiveHumanAction(ctx context.Context, scope core.Scope, action core.HumanAction, input core.DecideHumanActionInput, correlationID string) error {
	if action.Kind == core.HumanActionInputRequest && input.Decision != core.HumanActionAnswered {
		return &core.ValidationError{Field: "decision", Message: "input request requires answered"}
	}
	if action.Kind == core.HumanActionPermissionEscalation && input.Decision != core.ApprovalApproved && input.Decision != core.ApprovalRejected {
		return &core.ValidationError{Field: "decision", Message: "permission request requires approved or rejected"}
	}
	if action.Kind == core.HumanActionBudgetRequest && input.Decision != core.ApprovalApproved && input.Decision != core.ApprovalRejected {
		return &core.ValidationError{Field: "decision", Message: "budget request requires approved or rejected"}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getHumanActionForUser(ctx, tx, scope, action.ID)
	if err != nil {
		return err
	}
	if current.Version != input.Version || current.Status != core.HumanActionWaitingUser {
		return &core.StateConflictError{Status: current.Status, Command: input.Decision}
	}
	now := formatTime(time.Now())
	responseJSON, _ := json.Marshal(map[string]any{"decision": input.Decision, "answer": input.Answer, "note": input.Note})
	actionStatus := input.Decision
	result, err := tx.ExecContext(ctx, `
		UPDATE human_action_requests
		SET status = ?, response_json = ?, decided_by = ?, decided_at = ?, version = version + 1
		WHERE id = ? AND version = ? AND status = 'waiting_user'`,
		actionStatus, string(responseJSON), scope.UserID, now, action.ID, input.Version,
	)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &core.StateConflictError{Status: current.Status, Command: input.Decision}
	}
	stepStatus := core.StatusSucceeded
	if input.Decision == core.ApprovalRejected {
		stepStatus = core.StatusCanceled
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = ?, output_json = ?, updated_at = ? WHERE id = ? AND status = 'waiting_input'`, stepStatus, string(responseJSON), now, action.StepID); err != nil {
		return err
	}
	run, err := getRunContextTx(ctx, tx, action.RunID)
	if err != nil {
		return err
	}
	if action.Kind == core.HumanActionPermissionEscalation && input.Decision == core.ApprovalApproved {
		var snapshot map[string]any
		if json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot) != nil {
			return fmt.Errorf("decode run config snapshot")
		}
		scenePolicy, _ := snapshot["scene_policy"].(map[string]any)
		if scenePolicy == nil {
			scenePolicy = map[string]any{}
		}
		scenePolicy["workspace_command"] = true
		snapshot["scene_policy"] = scenePolicy
		snapshot["permission_escalation_id"] = action.ID
		encoded, _ := json.Marshal(snapshot)
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET config_snapshot_json = ?, updated_at = ? WHERE id = ?`, string(encoded), now, action.RunID); err != nil {
			return err
		}
		run.ConfigSnapshotJSON = string(encoded)
	}
	if action.Kind == core.HumanActionBudgetRequest && input.Decision == core.ApprovalApproved {
		maxCalls, _ := numericToolValue(action.Request["proposed_max_model_calls"])
		maxTokens, _ := numericToolValue(action.Request["proposed_max_tokens"])
		if maxCalls < 1 || maxTokens < 1 {
			return &core.ValidationError{Field: "answer", Message: "budget request has invalid proposed limits"}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_budgets SET max_model_calls = ?, max_tokens = ?, status = 'approved_exception', updated_at = ? WHERE run_id = ?`, maxCalls, maxTokens, now, action.RunID); err != nil {
			return err
		}
	}
	resumePlanRevisionID := action.PlanRevisionID
	resumePlanItemID := action.RunPlanItemID
	planChanged := false
	if action.Kind == core.HumanActionPermissionEscalation && input.Decision == core.ApprovalApproved {
		resumePlanRevisionID, resumePlanItemID, err = replaceRunPlanForSteering(ctx, tx, run, "权限范围已批准，基于新能力快照重新规划原目标："+run.Goal, scope.UserID, now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_plan_items
			SET title = '正在基于已批准权限更新执行计划',
			    description = '权限已生效，模型将基于新增能力生成真实语义步骤。',
			    updated_at = ?
			WHERE id = ?`, now, resumePlanItemID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET trigger = 'permission_change', goal_summary = ? WHERE id = ?`, truncateText(run.Goal, 1000), resumePlanRevisionID); err != nil {
			return err
		}
		planChanged = true
	}
	resumeStage := "human_action_resume"
	if action.Kind == core.HumanActionPermissionEscalation && input.Decision == core.ApprovalApproved {
		resumeStage = "permission_replan"
	}
	resumeInput := map[string]any{
		"stage": resumeStage, "human_action_id": action.ID,
		"human_action_kind": action.Kind, "decision": input.Decision,
		"answer": input.Answer, "note": input.Note, "source_step_id": action.StepID,
	}
	resumeStepID, err := insertQueuedModelStep(ctx, tx, run, resumeInput, now)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, resumePlanRevisionID, resumePlanItemID, resumeStepID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'running', started_at = CASE WHEN started_at = '' THEN ? ELSE started_at END, updated_at = ? WHERE id = ?`, now, now, resumePlanItemID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'queued', error_code = '', result_summary = '', updated_at = ? WHERE id = ?`, now, action.TaskID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'queued', updated_at = ? WHERE id = ?`, now, action.RunID); err != nil {
		return err
	}
	q := s.queries.WithTx(tx)
	if planChanged {
		if err := s.appendEvent(ctx, q, eventInput{
			OrganizationID: action.OrganizationID, ProjectID: action.ProjectID, RunID: action.RunID,
			EventType: "plan.published", ActorType: "user", ActorID: scope.UserID,
			CorrelationID: correlationID, Visibility: run.Visibility,
			Payload: map[string]any{"plan_revision_id": resumePlanRevisionID, "trigger": "permission_change", "goal_summary": run.Goal},
		}); err != nil {
			return err
		}
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: action.OrganizationID, ProjectID: action.ProjectID, RunID: action.RunID,
		EventType: "human_action.resolved", ActorType: "user", ActorID: scope.UserID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"human_action_id": action.ID, "kind": action.Kind, "status": actionStatus, "decision": input.Decision, "resume_step_id": resumeStepID},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// enrichToolCompletedEvent turns the durable receipt into a presentation-safe
// tool result. This also upgrades historical runs whose original event payload
// predates the Codex-style tool activity UI, without rewriting audit events.
func (s *Store) enrichToolCompletedEvent(ctx context.Context, runID string, payload map[string]any) {
	toolCallID, _ := payload["tool_call_id"].(string)
	if strings.TrimSpace(toolCallID) == "" {
		return
	}
	var toolName, argumentsJSON, outputJSON, status, errorCode string
	var exitCode int64
	err := s.db.QueryRowContext(ctx, `
		SELECT tc.tool_name, tc.arguments_json,
		       COALESCE(r.output_json, '{}'), COALESCE(r.status, tc.status),
		       COALESCE(NULLIF(r.error_code, ''), tc.error_code), COALESCE(r.exit_code, -1)
		FROM tool_calls tc
		LEFT JOIN side_effect_receipts r ON r.tool_call_id = tc.id
		WHERE tc.id = ? AND tc.run_id = ?`, toolCallID, runID,
	).Scan(&toolName, &argumentsJSON, &outputJSON, &status, &errorCode, &exitCode)
	if err != nil {
		return
	}
	arguments := map[string]any{}
	output := map[string]any{}
	_ = json.Unmarshal([]byte(argumentsJSON), &arguments)
	_ = json.Unmarshal([]byte(outputJSON), &output)
	payload["tool_name"] = toolName
	payload["input_summary"] = toolInputSummary(toolName, arguments)
	payload["output_summary"] = toolOutputSummary(toolName, status, errorCode, exitCode, output)
	payload["output_excerpt"] = toolOutputExcerpt(toolName, output)
}

func toolInputSummary(toolName string, arguments map[string]any) string {
	switch toolName {
	case core.ToolWorkspaceWriteFile:
		path, _ := arguments["path"].(string)
		content, _ := arguments["content"].(string)
		return fmt.Sprintf("%s · %d B", filepath.ToSlash(filepath.Clean(path)), len([]byte(content)))
	case core.ToolWorkspaceReadFile, core.ToolWorkspaceListFiles, core.ToolWorkspaceFindFiles,
		core.ToolWorkspaceGrep, core.ToolWorkspaceEditFile, core.ToolGitDiff, core.ToolGitRestore:
		path, _ := arguments["path"].(string)
		if path == "" {
			path = "."
		}
		return filepath.ToSlash(filepath.Clean(path))
	case core.ToolGitBranch:
		name, _ := arguments["name"].(string)
		return "创建分支 " + name
	case core.ToolGitCommit:
		message, _ := arguments["message"].(string)
		return "提交 · " + truncateToolText(message, 260)
	case core.ToolGitStatus:
		return "读取工作区状态"
	case core.ToolGitLog:
		return "读取提交历史"
	case core.ToolWorkspaceRunCommand:
		program, _ := arguments["program"].(string)
		parts := []string{program}
		if values, ok := arguments["args"].([]any); ok {
			for _, value := range values {
				parts = append(parts, fmt.Sprint(value))
			}
		}
		return truncateToolText(redactToolText(strings.Join(parts, " ")), 360)
	case core.ToolWebSearch:
		query, _ := arguments["query"].(string)
		if maxResults, ok := numericToolValue(arguments["max_results"]); ok {
			return fmt.Sprintf("%s · 最多 %d 条", truncateToolText(query, 260), maxResults)
		}
		return truncateToolText(query, 300)
	case core.ToolSkillInvoke:
		skill, _ := arguments["skill"].(string)
		return "加载 " + skill
	default:
		keys := make([]string, 0, len(arguments))
		for key := range arguments {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return "无参数"
		}
		return "参数：" + strings.Join(keys, "、")
	}
}

func toolOutputSummary(toolName, status, errorCode string, exitCode int64, output map[string]any) string {
	result := "成功"
	if status != "succeeded" {
		result = "失败"
	}
	if errorCode != "" {
		result += " · " + errorCode
	}
	switch toolName {
	case core.ToolWorkspaceWriteFile, core.ToolWorkspaceEditFile:
		path, _ := output["path"].(string)
		if size, ok := numericToolValue(output["size_bytes"]); ok {
			return fmt.Sprintf("%s · %s · %d B", result, path, size)
		}
	case core.ToolWorkspaceReadFile:
		path, _ := output["path"].(string)
		if lines, ok := numericToolValue(output["total_lines"]); ok {
			return fmt.Sprintf("%s · %s · %d 行", result, path, lines)
		}
	case core.ToolWorkspaceListFiles, core.ToolWorkspaceFindFiles, core.ToolWorkspaceGrep:
		if count, ok := numericToolValue(output["total_matches"]); ok {
			return fmt.Sprintf("%s · %d 项", result, count)
		}
		if count, ok := numericToolValue(output["total_scanned"]); ok {
			return fmt.Sprintf("%s · %d 项", result, count)
		}
	case core.ToolGitStatus:
		return result + " · 状态快照"
	case core.ToolGitDiff:
		diff, _ := output["diff"].(string)
		return fmt.Sprintf("%s · Diff %d B", result, len([]byte(diff)))
	case core.ToolGitLog:
		log, _ := output["log"].(string)
		return fmt.Sprintf("%s · Log %d B", result, len([]byte(log)))
	case core.ToolGitBranch, core.ToolGitCommit, core.ToolGitRestore:
		return result + " · 本地 Git 变更"
	case core.ToolWorkspaceRunCommand:
		stdout, _ := output["stdout"].(string)
		stderr, _ := output["stderr"].(string)
		return fmt.Sprintf("%s · 退出码 %d · 输出 %d B", result, exitCode, len([]byte(stdout))+len([]byte(stderr)))
	case core.ToolWebSearch:
		provider, _ := output["provider"].(string)
		if count, ok := numericToolValue(output["result_count"]); ok {
			return fmt.Sprintf("%s · %d 条结果 · %s", result, count, provider)
		}
	case core.ToolSkillInvoke:
		name, _ := output["skill_name"].(string)
		version, _ := output["version"].(string)
		return strings.TrimSpace(fmt.Sprintf("%s · %s %s", result, name, version))
	default:
		if tool, _ := output["tool_name"].(string); tool != "" {
			return result + " · " + tool
		}
	}
	return result
}

func toolOutputExcerpt(toolName string, output map[string]any) string {
	if toolName == core.ToolWorkspaceRunCommand {
		sections := make([]string, 0, 2)
		if stdout, _ := output["stdout"].(string); strings.TrimSpace(stdout) != "" {
			sections = append(sections, "stdout\n"+stdout)
		}
		if stderr, _ := output["stderr"].(string); strings.TrimSpace(stderr) != "" {
			sections = append(sections, "stderr\n"+stderr)
		}
		if len(sections) > 0 {
			return truncateToolText(redactToolText(strings.Join(sections, "\n\n")), 6000)
		}
	}
	safe := redactToolValue(output)
	encoded, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return ""
	}
	return truncateToolText(string(encoded), 6000)
}

func redactToolValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "api_key") || strings.Contains(lower, "apikey") || strings.Contains(lower, "authorization") {
				result[key] = "[REDACTED]"
				continue
			}
			result[key] = redactToolValue(child)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, child := range typed {
			result[index] = redactToolValue(child)
		}
		return result
	case string:
		return redactToolText(typed)
	default:
		return value
	}
}

var toolSecretPattern = regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password|authorization)(\s*[:=]\s*)([^\s,;"']+)`)
var toolBearerPattern = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`)

func redactToolText(value string) string {
	value = toolBearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	return toolSecretPattern.ReplaceAllString(value, "$1$2[REDACTED]")
}

func truncateToolText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "\n…输出已截断"
}

func numericToolValue(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	default:
		return 0, false
	}
}

func (s *Store) GetRunUsage(ctx context.Context, scope core.Scope, runID string) (core.RunUsage, error) {
	usage := core.RunUsage{RunID: runID, CostStatus: "unpriced", Currency: "USD"}
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(mc.id),
		       COALESCE(SUM(mc.input_tokens), 0),
		       COALESCE(SUM(mc.output_tokens), 0),
		       COALESCE(SUM(mc.cached_tokens), 0),
		       COALESCE(SUM(mc.reasoning_tokens), 0),
		       COALESCE(SUM(mc.usage_estimated), 0),
		       COALESCE(SUM(mc.latency_ms), 0)
		FROM runs r
		JOIN tasks t ON t.id = r.task_id
		JOIN memberships m ON m.organization_id = r.organization_id
		JOIN project_memberships pm ON pm.project_id = r.project_id AND pm.user_id = m.user_id
		LEFT JOIN model_calls mc ON mc.run_id = r.id
		WHERE r.id = ?
		  AND r.organization_id = ?
		  AND m.user_id = ?
		  AND m.status = 'active'
		  AND pm.status = 'active'
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)
		GROUP BY r.id`,
		runID, scope.OrganizationID, scope.UserID, scope.UserID,
	).Scan(&usage.ModelCalls, &usage.InputTokens, &usage.OutputTokens, &usage.CachedTokens, &usage.ReasoningTokens, &usage.EstimatedCalls, &usage.LatencyMS)
	if err != nil {
		return core.RunUsage{}, err
	}
	var budgetStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT max_model_calls, max_tokens, status, reserved_model_calls, reserved_tokens FROM run_budgets WHERE run_id = ?`, runID).Scan(&usage.BudgetMaxCalls, &usage.BudgetMaxTokens, &budgetStatus, &usage.ReservedCalls, &usage.ReservedTokens); err == nil {
		usage.BudgetStatus = budgetStatus
		if usage.BudgetMaxCalls > 0 {
			usage.RemainingCalls = usage.BudgetMaxCalls - usage.ModelCalls - usage.ReservedCalls
			if usage.RemainingCalls < 0 {
				usage.RemainingCalls = 0
			}
		}
		if usage.BudgetMaxTokens > 0 {
			usage.RemainingTokens = usage.BudgetMaxTokens - (usage.InputTokens + usage.OutputTokens) - usage.ReservedTokens
			if usage.RemainingTokens < 0 {
				usage.RemainingTokens = 0
			}
		}
	}
	var costValue float64
	var confirmed, estimated int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CAST(cost_decimal AS REAL)), 0), COALESCE(MAX(confirmed), 0), COALESCE(MAX(CASE WHEN confirmed = 0 THEN 1 ELSE 0 END), 0) FROM usage_ledger WHERE run_id = ?`, runID).Scan(&costValue, &confirmed, &estimated); err == nil {
		usage.CostDecimal = strconv.FormatFloat(costValue, 'f', 6, 64)
		if confirmed == 1 {
			usage.CostStatus = "confirmed"
		} else if estimated == 1 {
			usage.CostStatus = "estimated"
		}
	}
	return usage, nil
}

func (s *Store) GetProjectUsage(ctx context.Context, scope core.Scope, projectID string, since time.Time) (core.ProjectUsage, error) {
	usage := core.ProjectUsage{ProjectID: projectID, CostStatus: "unpriced", Currency: "USD", ByDay: []core.UsageBucket{}, ByModel: []core.UsageBucket{}, ByMember: []core.UsageBucket{}, ByAgent: []core.UsageBucket{}}
	sinceText := formatTime(since.UTC())
	rows, err := s.db.QueryContext(ctx, `
		SELECT model, input_tokens, output_tokens, cached_tokens, reasoning_tokens,
		       usage_estimated, latency_ms, created_at, owner_user_id, owner_name,
		       config_snapshot_json, cost_decimal, confirmed, has_ledger
		FROM (
			SELECT mc.id AS sort_id, mc.model, mc.input_tokens, mc.output_tokens, mc.cached_tokens,
			       mc.reasoning_tokens, mc.usage_estimated, mc.latency_ms, mc.created_at,
			       t.owner_user_id, COALESCE(u.display_name, '') AS owner_name, r.config_snapshot_json,
			       COALESCE(ul.cost_decimal, '0') AS cost_decimal, COALESCE(ul.confirmed, 0) AS confirmed,
			       CASE WHEN ul.call_id IS NULL THEN 0 ELSE 1 END AS has_ledger
			FROM model_calls mc
			JOIN projects p ON p.id = mc.project_id AND p.organization_id = mc.organization_id
			JOIN project_memberships viewer_pm ON viewer_pm.project_id = p.id
			  AND viewer_pm.user_id = ? AND viewer_pm.status = 'active'
			  AND viewer_pm.role IN ('owner', 'approver')
			JOIN tasks t ON t.id = mc.task_id
			JOIN users u ON u.id = t.owner_user_id
			JOIN runs r ON r.id = mc.run_id
			LEFT JOIN usage_ledger ul ON ul.call_id = mc.id
			WHERE mc.project_id = ? AND mc.organization_id = ? AND mc.created_at >= ?
			UNION ALL
			SELECT cmc.id AS sort_id, cmc.model, cmc.input_tokens, cmc.output_tokens, cmc.cached_tokens,
			       cmc.reasoning_tokens, cmc.usage_estimated, cmc.latency_ms, cmc.created_at,
			       cmc.owner_user_id, COALESCE(u.display_name, '') AS owner_name,
			       '{"mode":"answer"}' AS config_snapshot_json, cmc.cost_decimal, 0 AS confirmed, 1 AS has_ledger
			FROM conversation_model_calls cmc
			JOIN projects p ON p.id = cmc.project_id AND p.organization_id = cmc.organization_id
			JOIN project_memberships viewer_pm ON viewer_pm.project_id = p.id
			  AND viewer_pm.user_id = ? AND viewer_pm.status = 'active'
			  AND viewer_pm.role IN ('owner', 'approver')
			JOIN users u ON u.id = cmc.owner_user_id
			WHERE cmc.project_id = ? AND cmc.organization_id = ? AND cmc.created_at >= ?
		) usage_calls
		ORDER BY created_at ASC, sort_id ASC
		LIMIT 10000`, scope.UserID, projectID, scope.OrganizationID, sinceText, scope.UserID, projectID, scope.OrganizationID, sinceText)
	if err != nil {
		return core.ProjectUsage{}, err
	}
	defer rows.Close()
	dayBuckets := map[string]*usageBucketAccumulator{}
	modelBuckets := map[string]*usageBucketAccumulator{}
	memberBuckets := map[string]*usageBucketAccumulator{}
	agentBuckets := map[string]*usageBucketAccumulator{}
	var total usageBucketAccumulator
	for rows.Next() {
		var model, createdAt, ownerID, ownerName, snapshot, cost string
		var inputTokens, outputTokens, cachedTokens, reasoningTokens, estimated, latency, confirmed, hasLedger int64
		if err := rows.Scan(&model, &inputTokens, &outputTokens, &cachedTokens, &reasoningTokens, &estimated, &latency, &createdAt, &ownerID, &ownerName, &snapshot, &cost, &confirmed, &hasLedger); err != nil {
			return core.ProjectUsage{}, err
		}
		at, err := parseTime(createdAt)
		if err != nil {
			return core.ProjectUsage{}, err
		}
		agentKey, agentLabel := usageAgentDimension(snapshot)
		memberKey := ownerID
		memberLabel := ownerName
		if memberKey == "" {
			memberKey, memberLabel = "unknown", "未知成员"
		}
		if memberLabel == "" {
			memberLabel = memberKey
		}
		dayKey := at.UTC().Format("2006-01-02")
		addUsageBucket(&total, inputTokens, outputTokens, cachedTokens, reasoningTokens, estimated, latency, cost, confirmed, hasLedger)
		addUsageBucket(bucketFor(dayBuckets, dayKey, dayKey), inputTokens, outputTokens, cachedTokens, reasoningTokens, estimated, latency, cost, confirmed, hasLedger)
		addUsageBucket(bucketFor(modelBuckets, model, model), inputTokens, outputTokens, cachedTokens, reasoningTokens, estimated, latency, cost, confirmed, hasLedger)
		addUsageBucket(bucketFor(memberBuckets, memberKey, memberLabel), inputTokens, outputTokens, cachedTokens, reasoningTokens, estimated, latency, cost, confirmed, hasLedger)
		addUsageBucket(bucketFor(agentBuckets, agentKey, agentLabel), inputTokens, outputTokens, cachedTokens, reasoningTokens, estimated, latency, cost, confirmed, hasLedger)
	}
	if err := rows.Err(); err != nil {
		return core.ProjectUsage{}, err
	}
	usage.ModelCalls = total.ModelCalls
	usage.InputTokens = total.InputTokens
	usage.OutputTokens = total.OutputTokens
	usage.CachedTokens = total.CachedTokens
	usage.ReasoningTokens = total.ReasoningTokens
	usage.EstimatedCalls = total.EstimatedCalls
	usage.LatencyMS = total.LatencyMS
	usage.CostDecimal = formatUsageCost(total.Cost)
	if total.HasConfirmed {
		usage.CostStatus = "confirmed"
	} else if total.HasEstimated {
		usage.CostStatus = "estimated"
	}
	usage.ByDay = usageBuckets(dayBuckets)
	usage.ByModel = usageBuckets(modelBuckets)
	usage.ByMember = usageBuckets(memberBuckets)
	usage.ByAgent = usageBuckets(agentBuckets)
	return usage, nil
}

type usageBucketAccumulator struct {
	Key, Label      string
	ModelCalls      int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	EstimatedCalls  int64
	LatencyMS       int64
	Cost            float64
	HasConfirmed    bool
	HasEstimated    bool
}

func addUsageBucket(bucket *usageBucketAccumulator, input, output, cached, reasoning, estimated, latency int64, cost string, confirmed, hasLedger int64) {
	bucket.ModelCalls++
	bucket.InputTokens += input
	bucket.OutputTokens += output
	bucket.CachedTokens += cached
	bucket.ReasoningTokens += reasoning
	bucket.EstimatedCalls += estimated
	bucket.LatencyMS += latency
	if hasLedger == 1 {
		if value, err := strconv.ParseFloat(strings.TrimSpace(cost), 64); err == nil {
			bucket.Cost += value
		}
		if confirmed == 1 {
			bucket.HasConfirmed = true
		} else {
			bucket.HasEstimated = true
		}
	}
}

func bucketFor(buckets map[string]*usageBucketAccumulator, key, label string) *usageBucketAccumulator {
	if bucket, ok := buckets[key]; ok {
		return bucket
	}
	bucket := &usageBucketAccumulator{Key: key, Label: label}
	buckets[key] = bucket
	return bucket
}

func usageBuckets(values map[string]*usageBucketAccumulator) []core.UsageBucket {
	items := make([]core.UsageBucket, 0, len(values))
	for _, value := range values {
		items = append(items, core.UsageBucket{Key: value.Key, Label: value.Label, ModelCalls: value.ModelCalls, InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, CachedTokens: value.CachedTokens, ReasoningTokens: value.ReasoningTokens, EstimatedCalls: value.EstimatedCalls, LatencyMS: value.LatencyMS, CostDecimal: formatUsageCost(value.Cost)})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}

func formatUsageCost(value float64) string { return strconv.FormatFloat(value, 'f', 6, 64) }

func usageAgentDimension(snapshot string) (string, string) {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) == nil {
		if agent, ok := config["agent"].(map[string]any); ok {
			id, _ := agent["id"].(string)
			name, _ := agent["name"].(string)
			if strings.TrimSpace(id) != "" {
				if strings.TrimSpace(name) == "" {
					name = id
				}
				return id, name
			}
		}
		if mode, _ := config["mode"].(string); mode == core.ModeAgentTeam {
			return "agent_team", "Agent Team"
		}
	}
	return "default", "项目默认 Agent"
}

func (s *Store) UpdateRunBudget(ctx context.Context, scope core.Scope, runID string, input core.UpdateRunBudgetInput, correlationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var organizationID, projectID, taskID, visibility string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.organization_id, r.project_id, r.task_id, t.visibility
		FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE r.id = ? AND r.organization_id = ? AND t.owner_user_id = ?
	`, runID, scope.OrganizationID, scope.UserID).Scan(&organizationID, &projectID, &taskID, &visibility); err != nil {
		return err
	}
	var oldCalls, oldTokens int64
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT max_model_calls, max_tokens, status FROM run_budgets WHERE run_id = ?`, runID).Scan(&oldCalls, &oldTokens, &status); err != nil {
		return err
	}
	callsExtending := oldCalls > 0 && (input.MaxModelCalls == 0 || input.MaxModelCalls > oldCalls)
	tokensExtending := oldTokens > 0 && (input.MaxTokens == 0 || input.MaxTokens > oldTokens)
	extending := callsExtending || tokensExtending
	if extending && !input.Approve {
		return &core.ForbiddenError{Message: "increasing a Run budget requires explicit approval"}
	}
	newStatus := "active"
	if extending && input.Approve {
		newStatus = "approved_exception"
	}
	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE run_budgets SET max_model_calls = ?, max_tokens = ?, status = ?, updated_at = ? WHERE run_id = ?`, input.MaxModelCalls, input.MaxTokens, newStatus, now, runID); err != nil {
		return err
	}
	q := s.queries.WithTx(tx)
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: organizationID, ProjectID: projectID, RunID: runID, EventType: "budget.updated", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: visibility, Payload: map[string]any{"old_max_model_calls": oldCalls, "old_max_tokens": oldTokens, "max_model_calls": input.MaxModelCalls, "max_tokens": input.MaxTokens, "status": newStatus, "approved": input.Approve, "task_id": taskID}}); err != nil {
		return err
	}
	return tx.Commit()
}

// ReserveModelBudget atomically reserves one model call and an upper-bound
// token estimate for a claimed step. The reservation is attached to the step
// so a lease recovery does not double-reserve the same logical execution.
func (s *Store) ReserveModelBudget(ctx context.Context, run core.RunContext, step core.ClaimedStep, estimatedTokens int64, correlationID string) error {
	if estimatedTokens < 1 {
		estimatedTokens = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Start with the write instead of reading the step and then promoting a
	// deferred transaction. Under WAL, a concurrent approval/SSE write can make
	// that promotion fail with SQLITE_BUSY_SNAPSHOT even though budget remains.
	// The EXISTS guard keeps replay idempotent inside the same atomic write.
	result, err := tx.ExecContext(ctx, `
		UPDATE run_budgets
		SET reserved_model_calls = reserved_model_calls + 1,
		    reserved_tokens = reserved_tokens + ?,
		    updated_at = ?
		WHERE run_id = ? AND status IN ('active', 'approved_exception')
		  AND EXISTS (SELECT 1 FROM steps WHERE id = ? AND run_id = ? AND budget_reserved_tokens = 0)
		  AND (max_model_calls = 0 OR max_model_calls > (SELECT COUNT(*) FROM model_calls WHERE run_id = ?) + reserved_model_calls)
		  AND (max_tokens = 0 OR max_tokens >= (SELECT COALESCE(SUM(input_tokens + output_tokens), 0) FROM model_calls WHERE run_id = ?) + reserved_tokens + ?)`,
		estimatedTokens, formatTime(time.Now()), run.RunID, step.ID, run.RunID, run.RunID, run.RunID, estimatedTokens)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil {
		return err
	} else if count != 1 {
		var alreadyReserved int64
		if err := tx.QueryRowContext(ctx, `SELECT budget_reserved_tokens FROM steps WHERE id = ? AND run_id = ?`, step.ID, run.RunID).Scan(&alreadyReserved); err != nil {
			return err
		}
		if alreadyReserved > 0 {
			return nil
		}
		return fmt.Errorf("BUDGET_RESERVATION_EXCEEDED: no model call or token budget remains")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET budget_reserved_tokens = ? WHERE id = ? AND budget_reserved_tokens = 0`, estimatedTokens, step.ID); err != nil {
		return err
	}
	q := s.queries.WithTx(tx)
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "budget.reserved", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "estimated_tokens": estimatedTokens}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateAdvisorReport(ctx context.Context, scope core.Scope, runID string, input core.CreateAdvisorInput, correlationID string) (core.AdvisorReport, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.AdvisorReport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var organizationID, projectID, taskID, visibility string
	if err := tx.QueryRowContext(ctx, `
		SELECT r.organization_id, r.project_id, r.task_id, t.visibility
		FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE r.id = ? AND r.organization_id = ? AND t.owner_user_id = ?
	`, runID, scope.OrganizationID, scope.UserID).Scan(&organizationID, &projectID, &taskID, &visibility); err != nil {
		return core.AdvisorReport{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT stream_seq, event_type FROM run_events WHERE run_id = ? ORDER BY stream_seq DESC LIMIT 20`, runID)
	if err != nil {
		return core.AdvisorReport{}, err
	}
	defer rows.Close()
	evidence := make([]string, 0, 20)
	for rows.Next() {
		var sequence int64
		var eventType string
		if err := rows.Scan(&sequence, &eventType); err != nil {
			return core.AdvisorReport{}, err
		}
		evidence = append(evidence, fmt.Sprintf("run:%s#%d:%s", runID, sequence, eventType))
	}
	if err := rows.Err(); err != nil {
		return core.AdvisorReport{}, err
	}
	sort.Strings(evidence)
	status := "preview"
	eventType := "advisor.previewed"
	if input.Approve {
		status = "completed"
		eventType = "advisor.completed"
	}
	// Preview always exposes a conservative incremental estimate. The actual
	// model call is made only after the user explicitly approves the report and
	// only when the application has a configured Advisor gateway.
	estimatedTokens := int64(1536)
	summary := fmt.Sprintf("Advisor 预览：因%s分析了 %d 条已提交 RunEvent；预计增量 1 次模型调用、最多 %d Token。Advisor 不得批准安全动作或扩大权限。", input.TriggerReason, len(evidence), estimatedTokens)
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return core.AdvisorReport{}, err
	}
	now := time.Now()
	id, err := newID()
	if err != nil {
		return core.AdvisorReport{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO advisor_reports(id, organization_id, project_id, task_id, run_id, requested_by, trigger_reason, status, summary, evidence_json, estimated_model_calls, estimated_tokens, estimated_cost_decimal, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, '0', ?)`, id, organizationID, projectID, taskID, runID, scope.UserID, input.TriggerReason, status, summary, string(evidenceJSON), estimatedTokens, formatTime(now)); err != nil {
		return core.AdvisorReport{}, err
	}
	q := s.queries.WithTx(tx)
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: organizationID, ProjectID: projectID, RunID: runID, EventType: eventType, ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: visibility, Payload: map[string]any{"advisor_report_id": id, "trigger_reason": input.TriggerReason, "status": status, "model_calls": 0, "estimated_tokens": estimatedTokens}}); err != nil {
		return core.AdvisorReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.AdvisorReport{}, err
	}
	return core.AdvisorReport{ID: id, OrganizationID: organizationID, ProjectID: projectID, TaskID: taskID, RunID: runID, RequestedBy: scope.UserID, TriggerReason: input.TriggerReason, Status: status, Summary: summary, Evidence: evidence, EstimatedModelCalls: 1, EstimatedTokens: estimatedTokens, EstimatedCostDecimal: "0", CreatedAt: now}, nil
}

// FinalizeAdvisorReport replaces the conservative preview with the bounded
// model review after explicit approval and records the call in the same usage
// ledger used by normal Steps. On provider failure the report stays in preview
// status and retains a deterministic explanation, so a transient error never
// masquerades as a completed review.
func (s *Store) FinalizeAdvisorReport(ctx context.Context, scope core.Scope, reportID string, model core.ModelResult, modelErr error) (core.AdvisorReport, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.AdvisorReport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var report core.AdvisorReport
	var evidenceJSON, createdAt, visibility string
	if err := tx.QueryRowContext(ctx, `SELECT ar.id, ar.organization_id, ar.project_id, ar.task_id, ar.run_id, ar.requested_by, ar.trigger_reason, ar.status, ar.summary, ar.evidence_json, ar.estimated_model_calls, ar.estimated_tokens, ar.estimated_cost_decimal, ar.created_at, t.visibility FROM advisor_reports ar JOIN tasks t ON t.id = ar.task_id JOIN project_memberships pm ON pm.project_id = ar.project_id AND pm.user_id = ? AND pm.status = 'active' WHERE ar.id = ? AND ar.organization_id = ?`, scope.UserID, reportID, scope.OrganizationID).Scan(&report.ID, &report.OrganizationID, &report.ProjectID, &report.TaskID, &report.RunID, &report.RequestedBy, &report.TriggerReason, &report.Status, &report.Summary, &evidenceJSON, &report.EstimatedModelCalls, &report.EstimatedTokens, &report.EstimatedCostDecimal, &createdAt, &visibility); err != nil {
		return core.AdvisorReport{}, err
	}
	if err := json.Unmarshal([]byte(evidenceJSON), &report.Evidence); err != nil {
		return core.AdvisorReport{}, err
	}
	report.CreatedAt = mustParseTime(createdAt)
	now := formatTime(time.Now())
	if modelErr != nil {
		report.Status = "preview"
		errorSummary := truncateText(modelErr.Error(), 500)
		report.Summary = "Advisor 模型复核失败，保留确定性预览：" + errorSummary
		report.Evidence = append(report.Evidence, "advisor:model_error:"+errorSummary)
	} else {
		report.Status = "completed"
		report.Summary = strings.TrimSpace(model.Content)
		if report.Summary == "" {
			report.Summary = "Advisor 模型未返回可审阅内容。"
		}
		report.Evidence = append(report.Evidence, fmt.Sprintf("advisor:model=%s provider=%s input_tokens=%d output_tokens=%d", model.Model, model.Provider, model.Usage.InputTokens, model.Usage.OutputTokens))
		report.EstimatedModelCalls = 1
		report.EstimatedTokens = model.Usage.InputTokens + model.Usage.OutputTokens
		_, report.EstimatedCostDecimal = resolveModelPricing(model.Model, model.Usage)
		if model.CallID == "" {
			modelID, idErr := newID()
			if idErr != nil {
				return core.AdvisorReport{}, idErr
			}
			model.CallID = modelID
		}
		var stepID string
		_ = tx.QueryRowContext(ctx, `SELECT id FROM steps WHERE run_id = ? ORDER BY position DESC, id DESC LIMIT 1`, report.RunID).Scan(&stepID)
		if stepID != "" {
			pricingSnapshot, costDecimal := resolveModelPricing(model.Model, model.Usage)
			estimated := 0
			confirmed := 1
			if model.Usage.Estimated {
				estimated = 1
				confirmed = 0
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO model_calls(id, organization_id, project_id, task_id, run_id, step_id, provider, endpoint, model, routing_reason, status, provider_request_id, input_tokens, output_tokens, cached_tokens, reasoning_tokens, usage_estimated, latency_ms, error_code, created_at, pricing_snapshot_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'advisor', 'succeeded', ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`, model.CallID, report.OrganizationID, report.ProjectID, report.TaskID, report.RunID, stepID, model.Provider, model.Endpoint, model.Model, model.ProviderRequestID, model.Usage.InputTokens, model.Usage.OutputTokens, model.Usage.CachedTokens, model.Usage.ReasoningTokens, estimated, model.Latency.Milliseconds(), now, pricingSnapshot); err != nil {
				return core.AdvisorReport{}, err
			}
			requested, effective := persistedModelRefs(model)
			if _, err := tx.ExecContext(ctx, `UPDATE model_calls SET purpose = 'advisor', requested_provider_id = ?, requested_model_id = ?, effective_provider_id = ?, effective_model_id = ?, stop_reason = ?, context_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?, usage_semantics_version = ? WHERE id = ?`, requested.ProviderID, requested.ModelID, effective.ProviderID, effective.ModelID, model.StopReason, model.Usage.ContextTokens, persistedCacheReadTokens(model.Usage), model.Usage.CacheWriteTokens, persistedUsageVersion(model.Usage), model.CallID); err != nil {
				return core.AdvisorReport{}, err
			}
			ledgerID, idErr := newID()
			if idErr != nil {
				return core.AdvisorReport{}, idErr
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO usage_ledger(id, organization_id, project_id, task_id, run_id, step_id, call_id, input_tokens, output_tokens, cached_tokens, reasoning_tokens, cost_decimal, currency, confirmed, created_at, pricing_snapshot_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'USD', ?, ?, ?)`, ledgerID, report.OrganizationID, report.ProjectID, report.TaskID, report.RunID, stepID, model.CallID, model.Usage.InputTokens, model.Usage.OutputTokens, model.Usage.CachedTokens, model.Usage.ReasoningTokens, costDecimal, confirmed, now, pricingSnapshot); err != nil {
				return core.AdvisorReport{}, err
			}
		}
	}
	evidence, err := json.Marshal(report.Evidence)
	if err != nil {
		return core.AdvisorReport{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE advisor_reports SET status = ?, summary = ?, evidence_json = ?, estimated_model_calls = ?, estimated_tokens = ?, estimated_cost_decimal = ? WHERE id = ? AND organization_id = ?`, report.Status, report.Summary, string(evidence), report.EstimatedModelCalls, report.EstimatedTokens, report.EstimatedCostDecimal, report.ID, report.OrganizationID); err != nil {
		return core.AdvisorReport{}, err
	}
	q := s.queries.WithTx(tx)
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: report.OrganizationID, ProjectID: report.ProjectID, RunID: report.RunID, EventType: "advisor.model_reviewed", ActorType: "advisor", ActorID: scope.UserID, CorrelationID: report.ID, Visibility: visibility, Payload: map[string]any{"advisor_report_id": report.ID, "status": report.Status, "model": model.Model, "model_calls": report.EstimatedModelCalls, "tokens": report.EstimatedTokens}}); err != nil {
		return core.AdvisorReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.AdvisorReport{}, err
	}
	return report, nil
}

func (s *Store) ListRunArtifacts(ctx context.Context, scope core.Scope, runID string) ([]core.Artifact, error) {
	rows, err := s.queries.ListRunArtifactsForUser(ctx, sqlitegen.ListRunArtifactsForUserParams{RunID: runID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return nil, err
	}
	artifacts := make([]core.Artifact, 0, len(rows))
	for _, row := range rows {
		artifact, err := artifactFromRow(row)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func (s *Store) GetAgentTeamRoom(ctx context.Context, scope core.Scope, runID string) (core.AgentTeamRoom, error) {
	memberRows, err := s.queries.ListAgentTeamMembersForUser(ctx, sqlitegen.ListAgentTeamMembersForUserParams{RunID: runID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.AgentTeamRoom{}, err
	}
	messageRows, err := s.queries.ListAgentTeamMessagesForUser(ctx, sqlitegen.ListAgentTeamMessagesForUserParams{RunID: runID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.AgentTeamRoom{}, err
	}
	if len(memberRows) == 0 {
		return core.AgentTeamRoom{}, sql.ErrNoRows
	}
	members := make([]core.AgentTeamMember, 0, len(memberRows))
	for _, row := range memberRows {
		createdAt, parseErr := parseTime(row.CreatedAt)
		if parseErr != nil {
			return core.AgentTeamRoom{}, parseErr
		}
		updatedAt, parseErr := parseTime(row.UpdatedAt)
		if parseErr != nil {
			return core.AgentTeamRoom{}, parseErr
		}
		members = append(members, core.AgentTeamMember{ID: row.ID, RunID: row.RunID, Role: row.Role, DisplayName: row.DisplayName, Status: row.Status, CurrentStepID: row.CurrentStepID, Model: row.Model, CreatedAt: createdAt, UpdatedAt: updatedAt})
	}
	messages := make([]core.AgentTeamMessage, 0, len(messageRows))
	var cursor int64
	for _, row := range messageRows {
		createdAt, parseErr := parseTime(row.CreatedAt)
		if parseErr != nil {
			return core.AgentTeamRoom{}, parseErr
		}
		messages = append(messages, core.AgentTeamMessage{ID: row.ID, RunID: row.RunID, SenderAgentID: row.SenderAgentID, ServerSeq: row.ServerSeq, MessageType: row.MessageType, Content: row.Content, ContentHash: row.ContentHash, CreatedAt: createdAt})
		if row.ServerSeq > cursor {
			cursor = row.ServerSeq
		}
	}
	return core.AgentTeamRoom{RunID: runID, Members: members, Messages: messages, NextCursor: cursor}, nil
}

func (s *Store) CreateSchedule(ctx context.Context, scope core.Scope, projectID string, input core.CreateScheduleInput, nextRunAt time.Time, idempotency core.Idempotency) (core.Schedule, error) {
	route := "POST /projects/" + projectID + "/schedules"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Schedule
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.Schedule{}, err
		}
		if found {
			return replay, nil
		}
	}
	id, err := newID()
	if err != nil {
		return core.Schedule{}, err
	}
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Schedule{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.Schedule{}, err
	}
	row, err := q.CreateSchedule(ctx, sqlitegen.CreateScheduleParams{
		ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID,
		Name: input.Name, CronExpression: input.CronExpression, Timezone: input.Timezone, Goal: input.Goal,
		Acceptance: input.Acceptance, Mode: input.Mode, Visibility: input.Visibility,
		MisfirePolicy: input.MisfirePolicy, OverlapPolicy: input.OverlapPolicy, MisfireGraceSeconds: input.MisfireGraceSeconds,
		NextRunAt: formatTime(nextRunAt), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return core.Schedule{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "schedule.created", TargetType: "schedule", TargetID: id, Outcome: "succeeded", DetailJson: fmt.Sprintf(`{"name":%q,"next_run_at":%q}`, input.Name, nextRunAt.Format(time.RFC3339Nano)), CorrelationID: id, OccurredAt: now}); err != nil {
		return core.Schedule{}, err
	}
	schedule, err := scheduleFromRow(row)
	if err != nil {
		return core.Schedule{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, schedule); err != nil {
		return core.Schedule{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Schedule{}, err
	}
	return schedule, nil
}

func (s *Store) ListSchedules(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Schedule, error) {
	rows, err := s.queries.ListSchedulesForUser(ctx, sqlitegen.ListSchedulesForUserParams{ProjectID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	items := make([]core.Schedule, 0, len(rows))
	for _, row := range rows {
		item, err := scheduleFromRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Store) SetScheduleStatus(ctx context.Context, scope core.Scope, scheduleID, status string, idempotency core.Idempotency) (core.Schedule, error) {
	route := "POST /schedules/" + scheduleID + "/status"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Schedule
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.Schedule{}, err
		}
		if found {
			return replay, nil
		}
	}
	now := formatTime(time.Now())
	row, err := s.queries.SetScheduleStatusAsOwner(ctx, sqlitegen.SetScheduleStatusAsOwnerParams{Status: status, UpdatedAt: now, ID: scheduleID, OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.Schedule{}, sql.ErrNoRows
		}
		return core.Schedule{}, err
	}
	schedule, err := scheduleFromRow(row)
	if err != nil {
		return core.Schedule{}, err
	}
	if err := saveIdempotency(ctx, s.db, scope, route, idempotency, httpStatusOK, schedule); err != nil {
		return core.Schedule{}, err
	}
	if auditID, auditErr := newID(); auditErr == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'schedule.status_changed', 'schedule', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, schedule.ProjectID, scope.UserID, scheduleID, fmt.Sprintf(`{"status":%q}`, status), auditID, formatTime(time.Now()))
	}
	return schedule, nil
}

func (s *Store) GetDueSchedule(ctx context.Context, now time.Time) (core.Schedule, error) {
	row, err := s.queries.GetDueSchedule(ctx, formatTime(now))
	if err != nil {
		return core.Schedule{}, err
	}
	return scheduleFromRow(row)
}

func (s *Store) ClaimScheduleFire(ctx context.Context, schedule core.Schedule, nextRunAt time.Time) (core.ScheduleFire, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ScheduleFire{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	now := formatTime(time.Now())
	leaseUntil := formatTime(time.Now().Add(10 * time.Second))
	leaseResult, leaseErr := tx.ExecContext(ctx, `INSERT INTO scheduler_leases(lease_key, owner_id, lease_until, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(lease_key) DO UPDATE SET owner_id = excluded.owner_id, lease_until = excluded.lease_until, updated_at = excluded.updated_at WHERE scheduler_leases.lease_until < ? OR scheduler_leases.owner_id = ?`, "schedule:"+schedule.ID, s.schedulerOwnerID, leaseUntil, now, now, s.schedulerOwnerID)
	if leaseErr != nil {
		return core.ScheduleFire{}, leaseErr
	}
	if affected, _ := leaseResult.RowsAffected(); affected == 0 {
		return core.ScheduleFire{}, fmt.Errorf("SCHEDULE_LEASE_BUSY: schedule %s is claimed by another scheduler", schedule.ID)
	}
	var activeRuns int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM schedule_fires f JOIN tasks t ON t.id = f.task_id
		WHERE f.schedule_id = ? AND t.status IN ('queued','running','waiting_approval','waiting_input','paused')
	`, schedule.ID).Scan(&activeRuns); err != nil {
		return core.ScheduleFire{}, err
	}
	fireStatus := core.ScheduleFirePending
	if activeRuns > 0 && schedule.OverlapPolicy == core.ScheduleOverlapForbid {
		fireStatus = core.ScheduleFireSkipped
	}
	if activeRuns > 0 && schedule.OverlapPolicy == core.ScheduleOverlapReplace {
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'canceled', error_code = 'SCHEDULE_REPLACED', updated_at = ? WHERE id IN (SELECT task_id FROM schedule_fires WHERE schedule_id = ? AND task_id <> '' AND task_id IN (SELECT id FROM tasks WHERE status IN ('queued','running','waiting_approval','waiting_input','paused')))`, now, schedule.ID); err != nil {
			return core.ScheduleFire{}, err
		}
	}
	fireID, err := newID()
	if err != nil {
		return core.ScheduleFire{}, err
	}
	fire, err := q.CreateScheduleFire(ctx, sqlitegen.CreateScheduleFireParams{ID: fireID, ScheduleID: schedule.ID, OrganizationID: schedule.OrganizationID, ProjectID: schedule.ProjectID, PlannedAt: formatTime(schedule.NextRunAt), Generation: 1, Status: fireStatus, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fire, err = q.GetScheduleFire(ctx, sqlitegen.GetScheduleFireParams{ScheduleID: schedule.ID, PlannedAt: formatTime(schedule.NextRunAt), Generation: 1})
		}
		if err != nil {
			return core.ScheduleFire{}, err
		}
	}
	if err := q.UpdateScheduleAfterFire(ctx, sqlitegen.UpdateScheduleAfterFireParams{NextRunAt: formatTime(nextRunAt), UpdatedAt: now, ID: schedule.ID, NextRunAt_2: formatTime(schedule.NextRunAt)}); err != nil {
		return core.ScheduleFire{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ScheduleFire{}, err
	}
	return scheduleFireFromRow(fire)
}

func (s *Store) CreateScheduledTask(ctx context.Context, schedule core.Schedule, fire core.ScheduleFire, correlationID string) (core.Task, error) {
	return s.CreateTask(ctx, core.Scope{OrganizationID: schedule.OrganizationID, UserID: schedule.OwnerUserID}, schedule.ProjectID, core.CreateTaskInput{Title: schedule.Name, Goal: schedule.Goal, Acceptance: schedule.Acceptance, Mode: schedule.Mode, Visibility: schedule.Visibility}, map[string]any{"source": "schedule", "schedule_id": schedule.ID, "fire_id": fire.ID}, correlationID, core.Idempotency{Key: "schedule-fire-" + fire.ID, RequestHash: fire.ID})
}

func (s *Store) AdvanceScheduleAfterFire(ctx context.Context, scheduleID string, plannedAt, nextRunAt time.Time, fireStatus, taskID string) error {
	_ = nextRunAt
	_, err := s.db.ExecContext(ctx, `UPDATE schedule_fires SET status = ?, task_id = ?, updated_at = ? WHERE schedule_id = ? AND planned_at = ? AND generation = 1 AND status = 'pending'`, fireStatus, nullableString(taskID), formatTime(time.Now()), scheduleID, formatTime(plannedAt))
	return err
}

func (s *Store) CreateCapability(ctx context.Context, scope core.Scope, projectID string, input core.CreateCapabilityInput, contentHash, status, riskLevel string, idempotency core.Idempotency) (core.Capability, error) {
	route := "POST /projects/" + projectID + "/capabilities"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Capability
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.Capability{}, err
		}
		if found {
			return replay, nil
		}
	}
	id, err := newID()
	if err != nil {
		return core.Capability{}, err
	}
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Capability{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.Capability{}, err
	}
	scopeID := projectID
	if input.ScopeType == core.CapabilityScopeGlobal {
		var role string
		if err := tx.QueryRowContext(ctx, `SELECT role FROM memberships WHERE organization_id = ? AND user_id = ? AND status = 'active'`, scope.OrganizationID, scope.UserID).Scan(&role); err != nil {
			return core.Capability{}, err
		}
		if role != core.ProjectRoleOwner {
			return core.Capability{}, &core.ForbiddenError{Message: "global capability installation requires the instance owner"}
		}
		scopeID = scope.OrganizationID
	}
	var existingID, existingHash string
	conflictErr := tx.QueryRowContext(ctx, `SELECT id, content_hash FROM capabilities WHERE organization_id = ? AND scope_type = ? AND scope_id = ? AND kind = ? AND name = ? AND version = ? LIMIT 1`, scope.OrganizationID, input.ScopeType, scopeID, input.Kind, input.Name, input.Version).Scan(&existingID, &existingHash)
	if conflictErr == nil {
		if existingHash != contentHash {
			return core.Capability{}, &core.ResourceConflictError{Resource: "capability version", Reason: "the same capability name and version already has different content"}
		}
		return s.getCapabilityByIDTx(ctx, tx, scope, existingID)
	}
	if !errors.Is(conflictErr, sql.ErrNoRows) {
		return core.Capability{}, conflictErr
	}
	bundleStorageKey := ""
	if len(input.BundleData) > 0 {
		storageKey, writeErr := s.writeCapabilityBundle(ctx, input.BundleData, contentHash)
		if writeErr != nil {
			return core.Capability{}, writeErr
		}
		bundleStorageKey = storageKey
	}
	installJobID, err := newID()
	if err != nil {
		return core.Capability{}, err
	}
	row, err := q.CreateCapability(ctx, sqlitegen.CreateCapabilityParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, Kind: input.Kind, Name: input.Name, Version: input.Version, ManifestJson: input.ManifestJSON, ContentHash: contentHash, Status: status, RiskLevel: riskLevel, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return core.Capability{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capabilities SET scope_type = ?, scope_id = ?, source_filename = ?, bundle_storage_key = ?, install_job_id = ? WHERE id = ?`, input.ScopeType, scopeID, input.SourceFilename, bundleStorageKey, installJobID, id); err != nil {
		return core.Capability{}, err
	}
	jobState := "succeeded"
	if status == core.CapabilityStatusQuarantined {
		jobState = "awaiting_approval"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO capability_install_jobs (id, organization_id, project_id, actor_user_id, scope_type, scope_id, source_filename, bundle_hash, state, capability_id, error_code, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?)`, installJobID, scope.OrganizationID, projectID, scope.UserID, input.ScopeType, scopeID, input.SourceFilename, contentHash, jobState, id, now, now); err != nil {
		return core.Capability{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "capability.created", TargetType: "capability", TargetID: id, Outcome: "succeeded", DetailJson: fmt.Sprintf(`{"kind":%q,"name":%q,"version":%q,"content_hash":%q,"status":%q}`, input.Kind, input.Name, input.Version, contentHash, status), CorrelationID: id, OccurredAt: now}); err != nil {
		return core.Capability{}, err
	}
	capability := capabilityFromRow(row)
	capability.ScopeType = input.ScopeType
	capability.ScopeID = scopeID
	capability.SourceFilename = input.SourceFilename
	capability.BundleHash = contentHash
	capability.InstallJobID = installJobID
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, capability); err != nil {
		return core.Capability{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Capability{}, err
	}
	return capability, nil
}

func (s *Store) ListCapabilities(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Capability, error) {
	if _, err := s.queries.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, organization_id, project_id, owner_user_id, kind, name, version,
		       manifest_json, content_hash, status, risk_level, scope_type, scope_id,
		       source_filename, install_job_id, created_at, updated_at
		FROM capabilities
		WHERE organization_id = ?
		  AND ((scope_type = 'project' AND scope_id = ?) OR (scope_type = 'global' AND scope_id = ?))
		ORDER BY CASE scope_type WHEN 'project' THEN 0 ELSE 1 END, updated_at DESC, id ASC
		LIMIT ?`, scope.OrganizationID, projectID, scope.OrganizationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]core.Capability, 0)
	for rows.Next() {
		var item core.Capability
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.OwnerUserID, &item.Kind, &item.Name, &item.Version, &item.ManifestJSON, &item.ContentHash, &item.Status, &item.RiskLevel, &item.ScopeType, &item.ScopeID, &item.SourceFilename, &item.InstallJobID, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.BundleHash = item.ContentHash
		item.PermissionDiff = core.CapabilityPermissionDiff(item.ManifestJSON)
		item.CreatedAt = mustParseTime(createdAt)
		item.UpdatedAt = mustParseTime(updatedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetCapabilityStatus(ctx context.Context, scope core.Scope, capabilityID, status string, idempotency core.Idempotency) (core.Capability, error) {
	route := "POST /capabilities/" + capabilityID + "/status"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Capability
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.Capability{}, err
		}
		if found {
			return replay, nil
		}
	}
	if status == core.CapabilityStatusEnabled {
		var currentStatus, riskLevel, lastDecision string
		if err := s.db.QueryRowContext(ctx, `SELECT c.status, c.risk_level, COALESCE((SELECT cr.decision FROM capability_reviews cr WHERE cr.capability_id = c.id ORDER BY cr.created_at DESC LIMIT 1), '') FROM capabilities c JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = ? AND pm.status = 'active' WHERE c.id = ? AND c.organization_id = ?`, scope.UserID, capabilityID, scope.OrganizationID).Scan(&currentStatus, &riskLevel, &lastDecision); err != nil {
			return core.Capability{}, err
		}
		if riskLevel == "review" && lastDecision != core.CapabilityReviewApproved {
			return core.Capability{}, &core.ForbiddenError{Message: "quarantined capabilities require an explicit review decision"}
		}
	}
	row, err := s.queries.SetCapabilityStatusAsOwner(ctx, sqlitegen.SetCapabilityStatusAsOwnerParams{Status: status, UpdatedAt: formatTime(time.Now()), ID: capabilityID, OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.Capability{}, err
	}
	capability := capabilityFromRow(row)
	if enriched, enrichErr := s.getCapabilityByID(ctx, scope, capabilityID); enrichErr == nil {
		capability = enriched
	}
	if err := saveIdempotency(ctx, s.db, scope, route, idempotency, httpStatusOK, capability); err != nil {
		return core.Capability{}, err
	}
	if auditID, auditErr := newID(); auditErr == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'capability.status_changed', 'capability', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, capability.ProjectID, scope.UserID, capabilityID, fmt.Sprintf(`{"status":%q}`, status), auditID, formatTime(time.Now()))
	}
	return capability, nil
}

func (s *Store) ReviewCapability(ctx context.Context, scope core.Scope, capabilityID string, input core.ReviewCapabilityInput, correlationID string, idempotency core.Idempotency) (core.Capability, error) {
	route := "POST /capabilities/" + capabilityID + "/review"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Capability
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Capability{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.Capability
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	var capability core.Capability
	var role string
	var createdAt, updatedAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT c.id, c.organization_id, c.project_id, c.owner_user_id, c.kind,
		       c.name, c.version, c.manifest_json, c.content_hash, c.status,
		       c.risk_level, c.created_at, c.updated_at, pm.role
		FROM capabilities c
		JOIN project_memberships pm ON pm.project_id = c.project_id
		WHERE c.id = ? AND c.organization_id = ? AND pm.user_id = ? AND pm.status = 'active'`,
		capabilityID, scope.OrganizationID, scope.UserID,
	).Scan(&capability.ID, &capability.OrganizationID, &capability.ProjectID, &capability.OwnerUserID, &capability.Kind, &capability.Name, &capability.Version, &capability.ManifestJSON, &capability.ContentHash, &capability.Status, &capability.RiskLevel, &createdAt, &updatedAt, &role); err != nil {
		return core.Capability{}, err
	}
	capability.CreatedAt = mustParseTime(createdAt)
	capability.UpdatedAt = mustParseTime(updatedAt)
	capability.PermissionDiff = core.CapabilityPermissionDiff(capability.ManifestJSON)
	_ = tx.QueryRowContext(ctx, `SELECT scope_type, scope_id, source_filename, install_job_id FROM capabilities WHERE id = ?`, capability.ID).Scan(&capability.ScopeType, &capability.ScopeID, &capability.SourceFilename, &capability.InstallJobID)
	capability.BundleHash = capability.ContentHash
	if role != core.ProjectRoleOwner && role != core.ProjectRoleApprover {
		return core.Capability{}, &core.ForbiddenError{Message: "only project owners or approvers can review capabilities"}
	}
	if capability.Status != core.CapabilityStatusQuarantined {
		return core.Capability{}, &core.StateConflictError{Status: capability.Status, Command: "review"}
	}
	newStatus := core.CapabilityStatusDisabled
	if input.Decision == core.CapabilityReviewApproved {
		newStatus = core.CapabilityStatusEnabled
	}
	now := formatTime(time.Now())
	reviewID, err := newID()
	if err != nil {
		return core.Capability{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO capability_reviews (id, organization_id, project_id, capability_id, reviewer_user_id, requested_hash, decision, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, reviewID, capability.OrganizationID, capability.ProjectID, capability.ID, scope.UserID, capability.ContentHash, input.Decision, input.Note, now); err != nil {
		return core.Capability{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capabilities SET status = ?, updated_at = ? WHERE id = ? AND status = 'quarantined'`, newStatus, now, capability.ID); err != nil {
		return core.Capability{}, err
	}
	jobState := "rejected"
	if input.Decision == core.CapabilityReviewApproved {
		jobState = "succeeded"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capability_install_jobs SET state = ?, updated_at = ? WHERE capability_id = ? AND state = 'awaiting_approval'`, jobState, now, capability.ID); err != nil {
		return core.Capability{}, err
	}
	capability.Status = newStatus
	capability.UpdatedAt = mustParseTime(now)
	if auditID, auditErr := newID(); auditErr == nil {
		detail := fmt.Sprintf(`{"decision":%q,"note":%q,"content_hash":%q}`, input.Decision, input.Note, capability.ContentHash)
		if _, auditErr = tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'capability.reviewed', 'capability', ?, 'succeeded', ?, ?, ?)`, auditID, capability.OrganizationID, capability.ProjectID, scope.UserID, capability.ID, detail, correlationID, now); auditErr != nil {
			return core.Capability{}, auditErr
		}
	}
	if idempotency.Key != "" {
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, capability); err != nil {
			return core.Capability{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return core.Capability{}, err
	}
	return capability, nil
}

func (s *Store) ConnectMCP(ctx context.Context, scope core.Scope, capabilityID, correlationID string) (core.MCPHealth, error) {
	var projectID, kind, manifest, capabilityStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT project_id, kind, manifest_json, status FROM capabilities WHERE id = ? AND organization_id = ? AND owner_user_id = ?`, capabilityID, scope.OrganizationID, scope.UserID).Scan(&projectID, &kind, &manifest, &capabilityStatus); err != nil {
		return core.MCPHealth{}, err
	}
	if kind != core.CapabilityKindMCP {
		return core.MCPHealth{}, &core.ValidationError{Field: "capability_id", Message: "capability is not an MCP"}
	}
	if capabilityStatus != core.CapabilityStatusEnabled {
		return core.MCPHealth{}, &core.ForbiddenError{Message: "MCP must be explicitly enabled before connecting"}
	}
	health, connectErr := mcpadapter.Connect(ctx, manifest)
	status := core.MCPStatusFailed
	if connectErr == nil {
		status = core.MCPStatusConnected
	}
	toolsJSON, _ := json.Marshal(health.Tools)
	toolsHash := sha256.Sum256(toolsJSON)
	schemaHash := "sha256:" + hex.EncodeToString(toolsHash[:])
	now := formatTime(time.Now())
	connectionID := ""
	_ = s.db.QueryRowContext(ctx, `SELECT id FROM mcp_connections WHERE capability_id = ?`, capabilityID).Scan(&connectionID)
	if connectionID == "" {
		connectionID, _ = newID()
		if _, err := s.db.ExecContext(ctx, `INSERT INTO mcp_connections(id, organization_id, project_id, capability_id, transport, status, protocol, tool_count, tools_json, schema_hash, config_version, health_state, last_error, connected_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`, connectionID, scope.OrganizationID, projectID, capabilityID, health.Transport, status, health.Protocol, len(health.Tools), string(toolsJSON), schemaHash, status, health.LastError, nullableTime(health.ConnectedAt), now, now); err != nil {
			return core.MCPHealth{}, err
		}
	} else if _, err := s.db.ExecContext(ctx, `UPDATE mcp_connections SET status = ?, transport = ?, protocol = ?, tool_count = ?, tools_json = ?, schema_hash = ?, config_version = config_version + 1, health_state = ?, last_error = ?, connected_at = ?, updated_at = ? WHERE capability_id = ? AND organization_id = ?`, status, health.Transport, health.Protocol, len(health.Tools), string(toolsJSON), schemaHash, status, health.LastError, nullableTime(health.ConnectedAt), now, capabilityID, scope.OrganizationID); err != nil {
		return core.MCPHealth{}, err
	}
	s.writeCapabilityAudit(ctx, scope, projectID, "mcp.connected", capabilityID, map[string]any{"status": status, "tool_count": len(health.Tools), "error": health.LastError}, correlationID)
	result, readErr := s.GetMCPHealth(ctx, scope, capabilityID)
	if connectErr != nil {
		return result, connectErr
	}
	return result, readErr
}

func (s *Store) DisconnectMCP(ctx context.Context, scope core.Scope, capabilityID, correlationID string) (core.MCPHealth, error) {
	var projectID string
	if err := s.db.QueryRowContext(ctx, `SELECT project_id FROM capabilities WHERE id = ? AND organization_id = ? AND owner_user_id = ? AND kind = 'mcp'`, capabilityID, scope.OrganizationID, scope.UserID).Scan(&projectID); err != nil {
		return core.MCPHealth{}, err
	}
	now := formatTime(time.Now())
	if _, err := s.db.ExecContext(ctx, `UPDATE mcp_connections SET status = 'disabled', health_state = 'disabled', last_error = '', updated_at = ? WHERE capability_id = ? AND organization_id = ?`, now, capabilityID, scope.OrganizationID); err != nil {
		return core.MCPHealth{}, err
	}
	s.writeCapabilityAudit(ctx, scope, projectID, "mcp.disconnected", capabilityID, map[string]any{"status": core.MCPStatusDisabled}, correlationID)
	return s.GetMCPHealth(ctx, scope, capabilityID)
}

func (s *Store) GetMCPHealth(ctx context.Context, scope core.Scope, capabilityID string) (core.MCPHealth, error) {
	var item core.MCPHealth
	var connectedAt, updatedAt string
	// Connection health is project-scoped read data: any active project member
	// may use an enabled MCP capability, while connect/disconnect remains owner-only.
	err := s.db.QueryRowContext(ctx, `SELECT id, capability_id, project_id, transport, status, protocol, tool_count, tools_json, schema_hash, config_version, health_state, last_error, connected_at, updated_at FROM mcp_connections WHERE capability_id = ? AND organization_id = ? AND EXISTS (SELECT 1 FROM capabilities c JOIN project_memberships pm ON pm.project_id = c.project_id AND pm.user_id = ? AND pm.status = 'active' WHERE c.id = mcp_connections.capability_id AND c.organization_id = ?)`, capabilityID, scope.OrganizationID, scope.UserID, scope.OrganizationID).Scan(&item.ID, &item.CapabilityID, &item.ProjectID, &item.Transport, &item.Status, &item.Protocol, &item.ToolCount, &item.ToolsJSON, &item.SchemaHash, &item.ConfigVersion, &item.HealthState, &item.LastError, &connectedAt, &updatedAt)
	if err != nil {
		return core.MCPHealth{}, err
	}
	item.ConnectedAt = mustParseTimeOrZero(connectedAt)
	item.UpdatedAt = mustParseTime(updatedAt)
	return item, nil
}

func freezeRunToolSnapshots(ctx context.Context, tx *sql.Tx, runID string, config map[string]any, now string) error {
	insert := func(identity, invocation, source, capabilityID string, schema map[string]any, capabilities []string, reason string) error {
		schemaJSON, err := json.Marshal(schema)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(schemaJSON)
		capabilitiesJSON, err := json.Marshal(capabilities)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO run_tool_snapshots
			    (run_id, revision, tool_identity, invocation_name, source, capability_id,
			     schema_hash, capabilities_json, selection_reason, policy_version, created_at)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, identity, invocation, source, capabilityID,
			"sha256:"+hex.EncodeToString(sum[:]), string(capabilitiesJSON), reason, core.ToolPolicyVersion, now,
		)
		return err
	}
	for _, spec := range core.BuiltinToolSpecs() {
		if err := insert(spec.Identity, spec.Name, spec.Source, "", spec.Parameters, spec.Capabilities, "builtin_at_run_creation"); err != nil {
			return err
		}
	}
	rawMCP, ok := config["mcp_capabilities"]
	if !ok {
		return nil
	}
	body, err := json.Marshal(rawMCP)
	if err != nil {
		return err
	}
	var snapshots []struct {
		ID    string `json:"id"`
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &snapshots); err != nil {
		return err
	}
	for _, capability := range snapshots {
		for _, tool := range capability.Tools {
			if capability.ID == "" || tool.Name == "" {
				continue
			}
			identity := "mcp:" + capability.ID + ":" + tool.Name
			invocation := "mcp__" + capability.ID + "__" + tool.Name
			if err := insert(identity, invocation, "mcp", capability.ID, tool.InputSchema, nil, "enabled_and_healthy_at_run_creation"); err != nil {
				return err
			}
		}
	}
	return nil
}

func nullableTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return formatTime(value)
}

func mustParseTimeOrZero(value string) time.Time {
	if strings.TrimSpace(value) == "" {
		return time.Time{}
	}
	return mustParseTime(value)
}

func (s *Store) writeCapabilityAudit(ctx context.Context, scope core.Scope, projectID, action, targetID string, detail map[string]any, correlationID string) {
	data, _ := json.Marshal(detail)
	auditID, err := newID()
	if err != nil {
		return
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, ?, 'mcp_connection', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, projectID, scope.UserID, action, targetID, string(data), correlationID, formatTime(time.Now()))
}

func (s *Store) CreateMemory(ctx context.Context, scope core.Scope, projectID string, input core.CreateMemoryInput, sourceHash string, idempotency core.Idempotency) (core.Memory, error) {
	route := "POST /projects/" + projectID + "/memories"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Memory
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.Memory{}, err
		}
		if found {
			return replay, nil
		}
	}
	id, err := newID()
	if err != nil {
		return core.Memory{}, err
	}
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Memory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.Memory{}, err
	}
	row, err := q.CreateMemory(ctx, sqlitegen.CreateMemoryParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, Kind: input.Kind, Title: input.Title, Content: input.Content, SourceHash: sourceHash, Visibility: input.Visibility, SourceRefsJson: input.SourceRefsJSON, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return core.Memory{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "memory.created", TargetType: "memory", TargetID: id, Outcome: "succeeded", DetailJson: fmt.Sprintf(`{"kind":%q,"source_hash":%q}`, input.Kind, sourceHash), CorrelationID: id, OccurredAt: now}); err != nil {
		return core.Memory{}, err
	}
	memory := memoryFromRow(row)
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, memory); err != nil {
		return core.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Memory{}, err
	}
	return memory, nil
}

func (s *Store) ListMemories(ctx context.Context, scope core.Scope, projectID, query string, limit int) ([]core.Memory, error) {
	rows, err := s.queries.ListMemoriesForUser(ctx, sqlitegen.ListMemoriesForUserParams{UserID: scope.UserID, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, OwnerUserID_2: scope.UserID, Column6: query, Column7: sql.NullString{String: query, Valid: true}, Column8: sql.NullString{String: query, Valid: true}, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	items := make([]core.Memory, 0, len(rows))
	for _, row := range rows {
		items = append(items, memoryFromRow(row))
	}
	return items, nil
}

func (s *Store) UpdateMemory(ctx context.Context, scope core.Scope, memoryID string, input core.UpdateMemoryInput, sourceHash string, idempotency core.Idempotency) (core.Memory, error) {
	route := "PATCH /memories/" + memoryID
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Memory
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Memory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.Memory
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	var memory core.Memory
	var createdAt, updatedAt string
	var role string
	if err := tx.QueryRowContext(ctx, `
		SELECT m.id, m.organization_id, m.project_id, m.owner_user_id, m.kind,
		       m.title, m.content, m.source_hash, m.visibility, m.status, m.source_refs_json, m.created_at,
		       m.updated_at, pm.role
		FROM memories m
		JOIN project_memberships pm ON pm.project_id = m.project_id AND pm.user_id = ? AND pm.status = 'active'
		WHERE m.id = ? AND m.organization_id = ? AND (m.owner_user_id = ? OR (m.visibility = 'project' AND pm.role IN ('owner', 'approver')))`,
		scope.UserID, memoryID, scope.OrganizationID, scope.UserID,
	).Scan(&memory.ID, &memory.OrganizationID, &memory.ProjectID, &memory.OwnerUserID, &memory.Kind, &memory.Title, &memory.Content, &memory.SourceHash, &memory.Visibility, &memory.Status, &memory.SourceRefsJSON, &createdAt, &updatedAt, &role); err != nil {
		return core.Memory{}, err
	}
	memory.CreatedAt = mustParseTime(createdAt)
	memory.UpdatedAt = mustParseTime(updatedAt)
	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET title = ?, content = ?, source_hash = ?, visibility = ?, source_refs_json = ?, updated_at = ? WHERE id = ? AND organization_id = ?`, input.Title, input.Content, sourceHash, input.Visibility, input.SourceRefsJSON, now, memoryID, scope.OrganizationID); err != nil {
		return core.Memory{}, err
	}
	memory.Title, memory.Content, memory.SourceHash, memory.Visibility, memory.SourceRefsJSON, memory.UpdatedAt = input.Title, input.Content, sourceHash, input.Visibility, input.SourceRefsJSON, mustParseTime(now)
	auditID, err := newID()
	if err != nil {
		return core.Memory{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'memory.updated', 'memory', ?, 'succeeded', ?, ?, ?)`, auditID, memory.OrganizationID, memory.ProjectID, scope.UserID, memory.ID, fmt.Sprintf(`{"source_hash":%q,"visibility":%q}`, sourceHash, input.Visibility), auditID, now); err != nil {
		return core.Memory{}, err
	}
	if idempotency.Key != "" {
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, memory); err != nil {
			return core.Memory{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return core.Memory{}, err
	}
	return memory, nil
}

func (s *Store) SetMemoryStatus(ctx context.Context, scope core.Scope, memoryID, status string, idempotency core.Idempotency) (core.Memory, error) {
	route := "POST /memories/" + memoryID + "/status"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Memory
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Memory{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.Memory
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	var memory core.Memory
	var createdAt, updatedAt, role string
	if err := tx.QueryRowContext(ctx, `
		SELECT m.id, m.organization_id, m.project_id, m.owner_user_id, m.kind,
		       m.title, m.content, m.source_hash, m.visibility, m.status, m.source_refs_json, m.created_at,
		       m.updated_at, pm.role
		FROM memories m
		JOIN project_memberships pm ON pm.project_id = m.project_id AND pm.user_id = ? AND pm.status = 'active'
		WHERE m.id = ? AND m.organization_id = ?
		  AND (m.owner_user_id = ? OR (m.visibility = 'project' AND pm.role IN ('owner', 'approver')))`,
		scope.UserID, memoryID, scope.OrganizationID, scope.UserID,
	).Scan(&memory.ID, &memory.OrganizationID, &memory.ProjectID, &memory.OwnerUserID, &memory.Kind, &memory.Title, &memory.Content, &memory.SourceHash, &memory.Visibility, &memory.Status, &memory.SourceRefsJSON, &createdAt, &updatedAt, &role); err != nil {
		return core.Memory{}, err
	}
	memory.CreatedAt = mustParseTime(createdAt)
	memory.UpdatedAt = mustParseTime(updatedAt)
	now := formatTime(time.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE memories SET status = ?, updated_at = ? WHERE id = ? AND organization_id = ?`, status, now, memoryID, scope.OrganizationID); err != nil {
		return core.Memory{}, err
	}
	memory.Status, memory.UpdatedAt = status, mustParseTime(now)
	auditID, err := newID()
	if err != nil {
		return core.Memory{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'memory.status_changed', 'memory', ?, 'succeeded', ?, ?, ?)`, auditID, memory.OrganizationID, memory.ProjectID, scope.UserID, memory.ID, fmt.Sprintf(`{"status":%q}`, status), auditID, now); err != nil {
		return core.Memory{}, err
	}
	if idempotency.Key != "" {
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, memory); err != nil {
			return core.Memory{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return core.Memory{}, err
	}
	return memory, nil
}

func (s *Store) DeleteMemory(ctx context.Context, scope core.Scope, memoryID, correlationID string, idempotency core.Idempotency) error {
	route := "DELETE /memories/" + memoryID
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay map[string]any
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay map[string]any
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replayErr
		}
	}
	var projectID, ownerID, visibility, role string
	if err := tx.QueryRowContext(ctx, `SELECT m.project_id, m.owner_user_id, m.visibility, pm.role FROM memories m JOIN project_memberships pm ON pm.project_id = m.project_id AND pm.user_id = ? AND pm.status = 'active' WHERE m.id = ? AND m.organization_id = ? AND (m.owner_user_id = ? OR (m.visibility = 'project' AND pm.role IN ('owner', 'approver')))`, scope.UserID, memoryID, scope.OrganizationID, scope.UserID).Scan(&projectID, &ownerID, &visibility, &role); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE id = ? AND organization_id = ?`, memoryID, scope.OrganizationID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return &core.StateConflictError{Status: "deleted", Command: "delete"}
	}
	now := formatTime(time.Now())
	auditID, err := newID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'memory.deleted', 'memory', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, projectID, scope.UserID, memoryID, fmt.Sprintf(`{"owner_user_id":%q,"visibility":%q}`, ownerID, visibility), correlationID, now); err != nil {
		return err
	}
	if idempotency.Key != "" {
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusNoContent, map[string]any{"deleted": true, "memory_id": memoryID}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) PromoteMemoryToSkill(ctx context.Context, scope core.Scope, memoryID string, idempotency core.Idempotency) (core.Capability, error) {
	route := "POST /memories/" + memoryID + "/promote"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Capability
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.Capability{}, err
		}
		if found {
			return replay, nil
		}
	}
	var projectID, ownerID, title, content, sourceHash, sourceRefsJSON string
	var visibility string
	if err := s.db.QueryRowContext(ctx, `
		SELECT m.project_id, m.owner_user_id, m.title, m.content, m.source_hash, m.visibility, m.source_refs_json
		FROM memories m
		JOIN project_memberships pm ON pm.project_id = m.project_id AND pm.user_id = ? AND pm.status = 'active'
		WHERE m.id = ? AND m.organization_id = ? AND m.owner_user_id = ? AND m.status = 'active'`, scope.UserID, memoryID, scope.OrganizationID, scope.UserID).
		Scan(&projectID, &ownerID, &title, &content, &sourceHash, &visibility, &sourceRefsJSON); err != nil {
		return core.Capability{}, err
	}
	if len([]rune(content)) > 64*1024 {
		return core.Capability{}, &core.ValidationError{Field: "memory_id", Message: "memory content exceeds Skill draft limit"}
	}
	manifest, err := json.Marshal(map[string]any{
		"description":      "由记忆晋级的 Skill 草稿：" + title,
		"instructions":     content,
		"source_memory_id": memoryID,
		"source_hash":      sourceHash,
		"source_refs":      json.RawMessage(sourceRefsJSON),
		"visibility":       visibility,
	})
	if err != nil {
		return core.Capability{}, err
	}
	if err := core.ValidateCreateCapability(core.CreateCapabilityInput{Kind: core.CapabilityKindSkill, Name: title, Version: "0.1.0", ManifestJSON: string(manifest), ScopeType: core.CapabilityScopeProject}); err != nil {
		return core.Capability{}, err
	}
	id, err := newID()
	if err != nil {
		return core.Capability{}, err
	}
	hash := sha256.Sum256(manifest)
	contentHash := "sha256:" + hex.EncodeToString(hash[:])
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Capability{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	row, err := q.CreateCapability(ctx, sqlitegen.CreateCapabilityParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: ownerID, Kind: core.CapabilityKindSkill, Name: title, Version: "0.1.0", ManifestJson: string(manifest), ContentHash: contentHash, Status: core.CapabilityStatusQuarantined, RiskLevel: "memory-draft", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return core.Capability{}, err
	}
	installJobID, err := newID()
	if err != nil {
		return core.Capability{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE capabilities SET scope_type = 'project', scope_id = ?, install_job_id = ? WHERE id = ?`, projectID, installJobID, id); err != nil {
		return core.Capability{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO capability_install_jobs (id, organization_id, project_id, actor_user_id, scope_type, scope_id, source_filename, bundle_hash, state, capability_id, error_code, created_at, updated_at) VALUES (?, ?, ?, ?, 'project', ?, '', ?, 'awaiting_approval', ?, '', ?, ?)`, installJobID, scope.OrganizationID, projectID, scope.UserID, projectID, contentHash, id, now, now); err != nil {
		return core.Capability{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "memory.promoted_to_skill", TargetType: "capability", TargetID: id, Outcome: "succeeded", DetailJson: fmt.Sprintf(`{"memory_id":%q,"content_hash":%q,"status":%q}`, memoryID, contentHash, core.CapabilityStatusQuarantined), CorrelationID: id, OccurredAt: now}); err != nil {
		return core.Capability{}, err
	}
	capability := capabilityFromRow(row)
	capability.ScopeType = core.CapabilityScopeProject
	capability.ScopeID = projectID
	capability.BundleHash = contentHash
	capability.InstallJobID = installJobID
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, capability); err != nil {
		return core.Capability{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Capability{}, err
	}
	return capability, nil
}

func (s *Store) CreateAgentDefinition(ctx context.Context, scope core.Scope, projectID string, input core.CreateAgentInput, status string, idempotency core.Idempotency) (core.AgentDefinition, error) {
	route := "POST /projects/" + projectID + "/agents"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.AgentDefinition
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.AgentDefinition{}, err
		}
		if found {
			return replay, nil
		}
	}
	id, err := newID()
	if err != nil {
		return core.AgentDefinition{}, err
	}
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.AgentDefinition{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.AgentDefinition{}, err
	}
	row, err := q.CreateAgentDefinition(ctx, sqlitegen.CreateAgentDefinitionParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, Name: input.Name, Version: input.Version, Instructions: input.Instructions, Model: input.Model, FallbackModel: input.FallbackModel, ConfigJson: input.ConfigJSON, Status: status, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return core.AgentDefinition{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "agent.created", TargetType: "agent", TargetID: id, Outcome: "succeeded", DetailJson: fmt.Sprintf(`{"name":%q,"version":%q,"status":%q}`, input.Name, input.Version, status), CorrelationID: id, OccurredAt: now}); err != nil {
		return core.AgentDefinition{}, err
	}
	agent := agentDefinitionFromRow(row)
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, agent); err != nil {
		return core.AgentDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.AgentDefinition{}, err
	}
	return agent, nil
}

func (s *Store) ListAgentDefinitions(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.AgentDefinition, error) {
	rows, err := s.queries.ListAgentDefinitionsForUser(ctx, sqlitegen.ListAgentDefinitionsForUserParams{UserID: scope.UserID, OrganizationID: scope.OrganizationID, ProjectID: projectID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	items := make([]core.AgentDefinition, 0, len(rows))
	for _, row := range rows {
		items = append(items, agentDefinitionFromRow(row))
	}
	return items, nil
}

func (s *Store) SetAgentDefinitionStatus(ctx context.Context, scope core.Scope, agentID, status string, idempotency core.Idempotency) (core.AgentDefinition, error) {
	route := "POST /agents/" + agentID + "/status"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.AgentDefinition
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.AgentDefinition{}, err
		}
		if found {
			return replay, nil
		}
	}
	row, err := s.queries.SetAgentDefinitionStatusAsOwner(ctx, sqlitegen.SetAgentDefinitionStatusAsOwnerParams{Status: status, UpdatedAt: formatTime(time.Now()), ID: agentID, OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID})
	if err != nil {
		return core.AgentDefinition{}, err
	}
	agent := agentDefinitionFromRow(row)
	if err := saveIdempotency(ctx, s.db, scope, route, idempotency, httpStatusOK, agent); err != nil {
		return core.AgentDefinition{}, err
	}
	if auditID, auditErr := newID(); auditErr == nil {
		_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_events (id, organization_id, project_id, actor_user_id, action, target_type, target_id, outcome, detail_json, correlation_id, occurred_at) VALUES (?, ?, ?, ?, 'agent.status_changed', 'agent', ?, 'succeeded', ?, ?, ?)`, auditID, scope.OrganizationID, agent.ProjectID, scope.UserID, agentID, fmt.Sprintf(`{"status":%q}`, status), auditID, formatTime(time.Now()))
	}
	return agent, nil
}

func (s *Store) CreateTeamDefinition(ctx context.Context, scope core.Scope, projectID string, input core.CreateTeamDefinitionInput, idempotency core.Idempotency) (core.TeamDefinition, error) {
	route := "POST /projects/" + projectID + "/teams"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.TeamDefinition
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.TeamDefinition{}, err
		}
		if found {
			return replay, nil
		}
	}
	id, err := newID()
	if err != nil {
		return core.TeamDefinition{}, err
	}
	rolesJSON, err := json.Marshal(input.Roles)
	if err != nil {
		return core.TeamDefinition{}, err
	}
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.TeamDefinition{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if _, err := q.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
		return core.TeamDefinition{}, err
	}
	if input.ConfigJSON == "" {
		input.ConfigJSON = "{}"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO team_definitions (id, organization_id, project_id, owner_user_id, name, version, status, roles_json, config_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, 'enabled', ?, ?, ?, ?)`, id, scope.OrganizationID, projectID, scope.UserID, input.Name, input.Version, string(rolesJSON), input.ConfigJSON, now, now); err != nil {
		return core.TeamDefinition{}, err
	}
	if err := q.CreateAuditEvent(ctx, sqlitegen.CreateAuditEventParams{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, ActorUserID: scope.UserID, Action: "team_definition.created", TargetType: "team_definition", TargetID: id, Outcome: "succeeded", DetailJson: fmt.Sprintf(`{"name":%q,"version":%q}`, input.Name, input.Version), CorrelationID: id, OccurredAt: now}); err != nil {
		return core.TeamDefinition{}, err
	}
	definition := core.TeamDefinition{ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID, OwnerUserID: scope.UserID, Name: input.Name, Version: input.Version, Status: core.CapabilityStatusEnabled, Roles: input.Roles, ConfigJSON: input.ConfigJSON, CreatedAt: mustParseTimeOrZero(now), UpdatedAt: mustParseTimeOrZero(now)}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusCreated, definition); err != nil {
		return core.TeamDefinition{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.TeamDefinition{}, err
	}
	return definition, nil
}

func (s *Store) ListTeamDefinitions(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.TeamDefinition, error) {
	if projectID != "" {
		if _, err := s.queries.GetProjectForUser(ctx, sqlitegen.GetProjectForUserParams{ID: projectID, OrganizationID: scope.OrganizationID, UserID: scope.UserID}); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT td.id, td.organization_id, td.project_id, td.owner_user_id, td.name, td.version, td.status, td.roles_json, td.config_json, td.created_at, td.updated_at FROM team_definitions td JOIN projects p ON p.id = td.project_id JOIN memberships m ON m.organization_id = td.organization_id AND m.user_id = ? AND m.status = 'active' WHERE td.organization_id = ? AND (? = '' OR td.project_id = ?) AND p.id = td.project_id ORDER BY td.updated_at DESC, td.id LIMIT ?`, scope.UserID, scope.OrganizationID, projectID, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]core.TeamDefinition, 0)
	for rows.Next() {
		var item core.TeamDefinition
		var rolesJSON, createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.OwnerUserID, &item.Name, &item.Version, &item.Status, &rolesJSON, &item.ConfigJSON, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(rolesJSON), &item.Roles); err != nil {
			return nil, fmt.Errorf("decode team definition roles: %w", err)
		}
		item.CreatedAt = mustParseTimeOrZero(createdAt)
		item.UpdatedAt = mustParseTimeOrZero(updatedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SetTeamDefinitionStatus(ctx context.Context, scope core.Scope, teamID, status string, idempotency core.Idempotency) (core.TeamDefinition, error) {
	route := "POST /teams/" + teamID + "/status"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.TeamDefinition
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil {
			return core.TeamDefinition{}, err
		}
		if found {
			return replay, nil
		}
	}
	now := formatTime(time.Now())
	result, err := s.db.ExecContext(ctx, `UPDATE team_definitions SET status = ?, updated_at = ? WHERE id = ? AND organization_id = ? AND owner_user_id = ?`, status, now, teamID, scope.OrganizationID, scope.UserID)
	if err != nil {
		return core.TeamDefinition{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return core.TeamDefinition{}, sql.ErrNoRows
	}
	items, err := s.ListTeamDefinitions(ctx, scope, "", 1000)
	if err != nil {
		return core.TeamDefinition{}, err
	}
	var definition core.TeamDefinition
	for _, item := range items {
		if item.ID == teamID {
			definition = item
			break
		}
	}
	if definition.ID == "" {
		return core.TeamDefinition{}, sql.ErrNoRows
	}
	if err := saveIdempotency(ctx, s.db, scope, route, idempotency, httpStatusOK, definition); err != nil {
		return core.TeamDefinition{}, err
	}
	return definition, nil
}

func (s *Store) ListApprovals(ctx context.Context, scope core.Scope, status string, limit int) ([]core.Approval, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ar.id, ar.organization_id, ar.project_id, ar.task_id, ar.run_id,
		       ar.step_id, ar.tool_call_id, ar.tool_name, ar.risk_level, ar.status,
		       ar.reason, ar.frozen_params_json, ar.input_hash, ar.requested_by,
		       ar.decided_by, ar.requested_at, ar.expires_at, ar.decided_at
		FROM approval_requests ar
		JOIN tasks t ON t.id = ar.task_id
		JOIN memberships m ON m.organization_id = ar.organization_id
		JOIN project_memberships pm ON pm.project_id = ar.project_id AND pm.user_id = m.user_id
		WHERE ar.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)
		  AND (? = 'all' OR ar.status = ?)
		ORDER BY CASE ar.status WHEN 'pending' THEN 0 ELSE 1 END, ar.requested_at ASC, ar.id ASC
		LIMIT ?`,
		scope.OrganizationID, scope.UserID, scope.UserID, status, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	approvals := make([]core.Approval, 0)
	for rows.Next() {
		approval, scanErr := scanApproval(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		approvals = append(approvals, approval)
	}
	return approvals, rows.Err()
}

// ExpireApprovals deterministically rejects unattended pending approvals and
// converges the associated task/run to BLOCKED. It runs in one transaction so
// a stale approval can never remain actionable while its task is already
// terminal, and every expiry is represented in the Run Event stream.
func (s *Store) ExpireApprovals(ctx context.Context, now time.Time) (int, error) {
	nowText := formatTime(now.UTC())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, organization_id, project_id, task_id, run_id, step_id, tool_call_id
		FROM approval_requests
		WHERE status = 'pending'
		  AND ((expires_at <> '' AND expires_at <= ?) OR (expires_at = '' AND requested_at <= ?))
		ORDER BY CASE WHEN expires_at = '' THEN requested_at ELSE expires_at END ASC, id ASC`, nowText, formatTime(now.UTC().Add(-core.DefaultApprovalTTL)))
	if err != nil {
		return 0, err
	}
	type expiredApproval struct{ id, organizationID, projectID, taskID, runID, stepID, toolCallID string }
	items := make([]expiredApproval, 0)
	for rows.Next() {
		var item expiredApproval
		if err := rows.Scan(&item.id, &item.organizationID, &item.projectID, &item.taskID, &item.runID, &item.stepID, &item.toolCallID); err != nil {
			_ = rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()
	q := s.queries.WithTx(tx)
	expiredCount := 0
	for _, item := range items {
		result, err := tx.ExecContext(ctx, `
			UPDATE approval_requests
			SET status = 'rejected', decided_by = '', decision_note = ?, decided_at = ?
			WHERE id = ? AND status = 'pending'`, "审批已过期，系统默认拒绝", nowText, item.id)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			if err != nil {
				return 0, err
			}
			continue
		}
		expiredCount++
		if _, err := tx.ExecContext(ctx, `
			UPDATE human_action_requests
			SET status = 'expired', response_json = '{"decision":"rejected","reason":"expired"}',
			    decided_by = 'system', decided_at = ?, version = version + 1
			WHERE id = ? AND status IN ('waiting_user', 'reviewing')`, nowText, item.id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'rejected', error_code = 'APPROVAL_EXPIRED', updated_at = ? WHERE id = ?`, nowText, item.toolCallID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'canceled', error_code = 'APPROVAL_EXPIRED', owner_id = '', lease_until = '', updated_at = ? WHERE id = ? AND status = 'waiting_approval'`, nowText, item.stepID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'blocked', error_code = 'APPROVAL_EXPIRED', result_summary = ?, updated_at = ? WHERE id = ?`, "审批已过期，风险动作未执行", nowText, item.taskID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = 'blocked', updated_at = ? WHERE id = ?`, nowText, item.runID); err != nil {
			return 0, err
		}
		if err := s.blockToolBatchAfterApproval(ctx, tx, q, item.organizationID, item.projectID, item.runID, item.toolCallID, "APPROVAL_EXPIRED", nowText, "", item.id); err != nil {
			return 0, err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: item.organizationID, ProjectID: item.projectID, RunID: item.runID, EventType: "approval.expired", ActorType: "system", ActorID: "", CorrelationID: item.id, Visibility: core.VisibilityPrivate, Payload: map[string]any{"approval_id": item.id, "tool_call_id": item.toolCallID, "step_id": item.stepID, "decision": core.ApprovalRejected, "reason": "expired"}}); err != nil {
			return 0, err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: item.organizationID, ProjectID: item.projectID, RunID: item.runID, EventType: "human_action.expired", ActorType: "system", ActorID: "runtime", CorrelationID: item.id, Visibility: core.VisibilityPrivate, Payload: map[string]any{"human_action_id": item.id, "kind": core.HumanActionToolApproval, "status": core.HumanActionExpired}}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return expiredCount, nil
}

func (s *Store) blockToolBatchAfterApproval(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, organizationID, projectID, runID, toolCallID, errorCode, now, actorID, correlationID string) error {
	var batchID string
	if err := tx.QueryRowContext(ctx, `SELECT tool_batch_id FROM tool_calls WHERE id = ?`, toolCallID).Scan(&batchID); err != nil {
		return err
	}
	if batchID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = 'rejected', decided_by = ?, decision_note = ?, decided_at = ?
		WHERE status = 'pending'
		  AND tool_call_id IN (SELECT id FROM tool_calls WHERE tool_batch_id = ?)`,
		actorID, "同一工具批次中的审批被拒绝或过期，剩余动作已取消", now, batchID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE human_action_requests
		SET status = 'superseded',
		    response_json = '{"reason":"tool_batch_canceled"}',
		    decided_by = ?, decided_at = ?, version = version + 1
		WHERE status IN ('waiting_user', 'reviewing')
		  AND id IN (
		    SELECT ar.id FROM approval_requests ar
		    JOIN tool_calls tc ON tc.id = ar.tool_call_id
		    WHERE tc.tool_batch_id = ?
		  )`,
		actorID, now, batchID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE tool_calls
		SET status = 'blocked', error_code = ?, completed_at = ?, updated_at = ?
		WHERE tool_batch_id = ? AND status IN ('planned', 'queued', 'waiting_approval')`,
		"BATCH_CANCELED_AFTER_"+errorCode, now, now, batchID,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps
		SET status = 'canceled', error_code = ?, owner_id = '', lease_until = '', updated_at = ?
		WHERE tool_batch_id = ? AND status IN ('queued', 'waiting_approval')`,
		"BATCH_CANCELED_AFTER_"+errorCode, now, batchID,
	); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tool_batches SET status = 'blocked', updated_at = ? WHERE id = ? AND status <> 'blocked'`, now, batchID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return err
	}
	return s.appendEvent(ctx, q, eventInput{
		OrganizationID: organizationID,
		ProjectID:      projectID,
		RunID:          runID,
		EventType:      "tool.batch_completed",
		ActorType:      "system",
		ActorID:        actorID,
		CorrelationID:  correlationID,
		Visibility:     core.VisibilityPrivate,
		Payload: map[string]any{
			"batch_id":   batchID,
			"status":     "blocked",
			"error_code": errorCode,
		},
	})
}

func (s *Store) ListInboxHistory(ctx context.Context, scope core.Scope, limit int) ([]core.InboxHistoryItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.event_type, e.project_id, t.id, t.title, e.payload_json, e.occurred_at
		FROM run_events e
		JOIN runs r ON r.id = e.run_id
		JOIN tasks t ON t.id = r.task_id
		JOIN memberships m ON m.organization_id = e.organization_id
		JOIN project_memberships pm ON pm.project_id = e.project_id AND pm.user_id = m.user_id
		WHERE e.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (e.visibility = 'project' OR t.owner_user_id = ?)
		  AND e.event_type IN ('approval.decided', 'approval.expired', 'tool.side_effect_resolved', 'run.paused', 'run.resumed', 'run.retried', 'run.canceled', 'outcome.accepted')
		ORDER BY e.occurred_at DESC, e.id DESC
		LIMIT ?`, scope.OrganizationID, scope.UserID, scope.UserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]core.InboxHistoryItem, 0)
	for rows.Next() {
		var id, eventType, projectID, taskID, title, payloadJSON, occurredAtText string
		if err := rows.Scan(&id, &eventType, &projectID, &taskID, &title, &payloadJSON, &occurredAtText); err != nil {
			return nil, err
		}
		occurredAt, err := parseTime(occurredAtText)
		if err != nil {
			return nil, err
		}
		payload := map[string]any{}
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return nil, fmt.Errorf("decode inbox history payload: %w", err)
		}
		kind, status, summary := inboxHistorySummary(eventType, payload)
		items = append(items, core.InboxHistoryItem{ID: id, Kind: kind, ProjectID: projectID, TaskID: taskID, Title: title, Summary: summary, Status: status, OccurredAt: occurredAt})
	}
	return items, rows.Err()
}

func inboxHistorySummary(eventType string, payload map[string]any) (kind, status, summary string) {
	stringValue := func(key string) string {
		value, _ := payload[key].(string)
		return value
	}
	switch eventType {
	case "approval.decided":
		decision := stringValue("decision")
		if decision == core.ApprovalApproved {
			if stringValue("approval_scope") == core.ApprovalScopeConversation {
				return "approval", decision, "本会话已允许同类动作，冻结参数保持不变"
			}
			if stringValue("approval_scope") == core.ApprovalScopeProject {
				return "approval", decision, "当前项目已允许同类窄规则动作 30 天，可随时撤销"
			}
			return "approval", decision, "审批已批准一次，冻结参数保持不变"
		}
		return "approval", decision, "审批已拒绝，风险动作未执行"
	case "approval.expired":
		return "approval", core.ApprovalRejected, "审批已过期，系统默认拒绝"
	case "tool.side_effect_resolved":
		return "side_effect", stringValue("decision"), "未知副作用已由人工完成核查"
	case "run.paused":
		return "task", core.StatusPaused, "任务已暂停"
	case "run.resumed":
		return "task", core.StatusQueued, "任务已恢复排队"
	case "run.retried":
		return "task", core.StatusQueued, "任务已从安全点重试"
	case "run.canceled":
		return "task", core.StatusCanceled, "任务已取消"
	case "outcome.accepted":
		return "task", core.StatusSucceeded, "任务结果已被人工验收"
	default:
		return "task", "", "人机协作状态已更新"
	}
}

func (s *Store) ListSideEffectReceipts(ctx context.Context, scope core.Scope, status string, limit int) ([]core.SideEffectReceipt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.organization_id, r.project_id, r.task_id, r.run_id,
		       r.step_id, r.tool_call_id, tc.tool_name, r.input_hash, r.idempotent,
		       r.status, r.error_code, r.started_at, r.finished_at
		FROM side_effect_receipts r
		JOIN tool_calls tc ON tc.id = r.tool_call_id
		JOIN tasks t ON t.id = r.task_id
		JOIN memberships m ON m.organization_id = r.organization_id
		JOIN project_memberships pm ON pm.project_id = r.project_id AND pm.user_id = m.user_id
		WHERE r.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)
		  AND (? = 'all' OR r.status = ?)
		ORDER BY CASE r.status WHEN 'unknown' THEN 0 ELSE 1 END, r.started_at ASC, r.id ASC
		LIMIT ?`,
		scope.OrganizationID, scope.UserID, scope.UserID, status, status, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]core.SideEffectReceipt, 0)
	for rows.Next() {
		item, scanErr := scanSideEffectReceipt(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ResolveSideEffect(ctx context.Context, scope core.Scope, receiptID string, input core.ResolveSideEffectInput, correlationID string, idempotency core.Idempotency) (core.SideEffectReceipt, error) {
	route := "POST /side-effect-receipts/" + receiptID + "/resolution"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.SideEffectReceipt
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.SideEffectReceipt
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	receipt, err := getSideEffectReceiptForUserTx(ctx, tx, scope, receiptID)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	var role string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM project_memberships WHERE project_id = ? AND user_id = ? AND status = 'active'`, receipt.ProjectID, scope.UserID).Scan(&role); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if role != core.ProjectRoleOwner && role != core.ProjectRoleApprover {
		return core.SideEffectReceipt{}, &core.ForbiddenError{Message: "project members cannot resolve side effects"}
	}
	if receipt.Status != core.SideEffectReceiptUnknown {
		return core.SideEffectReceipt{}, &core.StateConflictError{Status: receipt.Status, Command: input.Decision}
	}
	var existingDecision string
	_ = tx.QueryRowContext(ctx, `SELECT decision FROM side_effect_resolutions WHERE receipt_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, receiptID).Scan(&existingDecision)
	if existingDecision != "" {
		return core.SideEffectReceipt{}, &core.StateConflictError{Status: existingDecision, Command: input.Decision}
	}
	run, err := getRunContextTx(ctx, tx, receipt.RunID)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	now := formatTime(time.Now())
	resolutionID, err := newID()
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO side_effect_resolutions (id, organization_id, project_id, task_id, run_id, receipt_id, decision, note, actor_user_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, resolutionID, receipt.OrganizationID, receipt.ProjectID, receipt.TaskID, receipt.RunID, receipt.ID, input.Decision, input.Note, scope.UserID, now); err != nil {
		return core.SideEffectReceipt{}, err
	}
	confirmed := input.Decision == core.SideEffectDecisionConfirmApplied
	receiptStatus := core.SideEffectReceiptFailed
	errorCode := "SIDE_EFFECT_" + strings.ToUpper(strings.TrimPrefix(input.Decision, "confirm_"))
	if confirmed {
		receiptStatus = core.SideEffectReceiptSucceeded
		errorCode = "MANUALLY_CONFIRMED"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE side_effect_receipts SET status = ?, error_code = ?, finished_at = ? WHERE id = ? AND status = 'unknown'`, receiptStatus, errorCode, now, receipt.ID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	actionStatus := core.HumanActionRejected
	if confirmed {
		actionStatus = core.HumanActionApproved
	}
	actionResponse, _ := json.Marshal(map[string]any{"decision": input.Decision, "note": input.Note})
	if _, err := tx.ExecContext(ctx, `
		UPDATE human_action_requests
		SET status = ?, response_json = ?, decided_by = ?, decided_at = ?, version = version + 1
		WHERE id = ? AND status IN ('waiting_user', 'reviewing')`,
		actionStatus, string(actionResponse), scope.UserID, now, receipt.ID,
	); err != nil {
		return core.SideEffectReceipt{}, err
	}
	toolStatus := core.StatusBlocked
	stepStatus := core.StatusBlocked
	taskStatus := core.StatusBlocked
	taskError := errorCode
	taskSummary := "副作用结果未确认执行，已由人工终止；如需重试，请 Fork 任务并重新审批。"
	if confirmed {
		toolStatus = core.StatusSucceeded
		stepStatus = core.StatusSucceeded
		taskStatus = core.StatusSucceeded
		taskError = ""
		taskSummary = "外部副作用已由用户人工确认完成。"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = ?, error_code = ?, updated_at = ? WHERE id = ?`, toolStatus, taskError, now, receipt.ToolCallID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	stepOutput, _ := json.Marshal(map[string]any{"manual_resolution": input.Decision, "note": input.Note, "receipt_id": receipt.ID})
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = ?, output_json = ?, error_code = ?, updated_at = ? WHERE id = ? AND status = 'blocked'`, stepStatus, string(stepOutput), taskError, now, receipt.StepID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE step_attempts SET status = ?, error_code = ?, finished_at = ? WHERE id = (SELECT id FROM step_attempts WHERE step_id = ? ORDER BY attempt_number DESC LIMIT 1)`, stepStatus, taskError, now, receipt.StepID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, error_code = ?, result_summary = ?, updated_at = ? WHERE id = ?`, taskStatus, taskError, taskSummary, now, receipt.TaskID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, taskStatus, now, receipt.RunID); err != nil {
		return core.SideEffectReceipt{}, err
	}
	validationID, err := newID()
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	validationStatus := "failed"
	if confirmed {
		validationStatus = "passed"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO validation_results (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, validator, status, input_hash, details_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, validationID, receipt.OrganizationID, receipt.ProjectID, receipt.TaskID, receipt.RunID, receipt.StepID, receipt.ToolCallID, "manual_side_effect_resolution", validationStatus, receipt.InputHash, string(stepOutput), now); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendEvent(ctx, s.queries.WithTx(tx), eventInput{OrganizationID: receipt.OrganizationID, ProjectID: receipt.ProjectID, RunID: receipt.RunID, EventType: "tool.side_effect_resolved", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"receipt_id": receipt.ID, "tool_call_id": receipt.ToolCallID, "decision": input.Decision, "note": input.Note, "validation_status": validationStatus}}); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendEvent(ctx, s.queries.WithTx(tx), eventInput{OrganizationID: receipt.OrganizationID, ProjectID: receipt.ProjectID, RunID: receipt.RunID, EventType: "human_action.resolved", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": receipt.ID, "kind": core.HumanActionSideEffectResolution, "status": actionStatus, "decision": input.Decision}}); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendEvent(ctx, s.queries.WithTx(tx), eventInput{OrganizationID: receipt.OrganizationID, ProjectID: receipt.ProjectID, RunID: receipt.RunID, EventType: "step." + stepStatus, ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": receipt.StepID, "receipt_id": receipt.ID, "decision": input.Decision, "error_code": taskError}}); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := s.appendConversationMessage(ctx, tx, s.queries.WithTx(tx), run, "assistant", taskSummary, now); err != nil {
		return core.SideEffectReceipt{}, err
	}
	receipt.Status = receiptStatus
	receipt.ErrorCode = errorCode
	finishedAt, _ := parseTime(now)
	receipt.FinishedAt = &finishedAt
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, httpStatusOK, receipt); err != nil {
		return core.SideEffectReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.SideEffectReceipt{}, err
	}
	return receipt, nil
}

func (s *Store) DecideApproval(ctx context.Context, scope core.Scope, approvalID string, input core.DecideApprovalInput, correlationID string, idempotency core.Idempotency) (core.Approval, error) {
	route := "POST /approvals/" + approvalID + "/decision"
	if idempotency.Key != "" {
		s.idempotencyMu.Lock()
		defer s.idempotencyMu.Unlock()
		var replay core.Approval
		found, err := replayIdempotency(ctx, s.db, scope, route, idempotency, &replay)
		if err != nil || found {
			return replay, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Approval{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if idempotency.Key != "" {
		var replay core.Approval
		found, replayErr := replayIdempotency(ctx, tx, scope, route, idempotency, &replay)
		if replayErr != nil || found {
			return replay, replayErr
		}
	}
	approval, err := getApprovalForUser(ctx, tx, scope, approvalID)
	if err != nil {
		return core.Approval{}, err
	}
	var projectRole string
	if err := tx.QueryRowContext(ctx, `SELECT role FROM project_memberships WHERE project_id = ? AND user_id = ? AND status = 'active'`, approval.ProjectID, scope.UserID).Scan(&projectRole); err != nil {
		return core.Approval{}, err
	}
	if projectRole != core.ProjectRoleOwner && projectRole != core.ProjectRoleApprover {
		return core.Approval{}, &core.ForbiddenError{Message: "project members cannot decide approvals"}
	}
	if input.Scope == core.ApprovalScopeProject && projectRole != core.ProjectRoleOwner {
		return core.Approval{}, &core.ForbiddenError{Message: "only a project owner can create project approval rules"}
	}
	if approval.Status != core.ApprovalPending {
		if approval.Status != input.Decision {
			return core.Approval{}, &core.StateConflictError{Status: approval.Status, Command: input.Decision}
		}
		if err := saveIdempotency(ctx, tx, scope, route, idempotency, 200, approval); err != nil {
			return core.Approval{}, err
		}
		if err := tx.Commit(); err != nil {
			return core.Approval{}, err
		}
		return approval, nil
	}
	now := formatTime(time.Now())
	result, err := tx.ExecContext(ctx, `
		UPDATE approval_requests
		SET status = ?, decided_by = ?, decision_note = ?, decided_at = ?
		WHERE id = ? AND status = 'pending'`,
		input.Decision, scope.UserID, input.Note, now, approvalID,
	)
	if err != nil {
		return core.Approval{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return core.Approval{}, &core.StateConflictError{Status: approval.Status, Command: input.Decision}
	}
	humanStatus := core.HumanActionApproved
	if input.Decision == core.ApprovalRejected {
		humanStatus = core.HumanActionRejected
	}
	responseJSON, _ := json.Marshal(map[string]any{"decision": input.Decision, "scope": input.Scope, "note": input.Note})
	if _, err := tx.ExecContext(ctx, `
		UPDATE human_action_requests
		SET status = ?, response_json = ?, decided_by = ?, decided_at = ?, version = version + 1
		WHERE id = ? AND status IN ('waiting_user', 'reviewing')`,
		humanStatus, string(responseJSON), scope.UserID, now, approvalID,
	); err != nil {
		return core.Approval{}, err
	}
	taskStatus := core.StatusQueued
	errorCode := ""
	resultSummary := "已批准冻结的工具参数，等待 Worker 执行。"
	toolStatus := "queued"
	stepStatus := core.StatusQueued
	if input.Decision == core.ApprovalRejected {
		taskStatus = core.StatusBlocked
		errorCode = "APPROVAL_REJECTED"
		resultSummary = "审批被拒绝，风险动作未执行。"
		toolStatus = "rejected"
		stepStatus = core.StatusCanceled
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = ?, error_code = ?, updated_at = ? WHERE id = ?`, toolStatus, errorCode, now, approval.ToolCallID); err != nil {
		return core.Approval{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = ?, error_code = ?, owner_id = '', lease_until = '', updated_at = ? WHERE id = ? AND status = 'waiting_approval'`, stepStatus, errorCode, now, approval.StepID); err != nil {
		return core.Approval{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = ?, error_code = ?, result_summary = ?, updated_at = ? WHERE id = ?`, taskStatus, errorCode, resultSummary, now, approval.TaskID); err != nil {
		return core.Approval{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET status = ?, updated_at = ? WHERE id = ?`, taskStatus, now, approval.RunID); err != nil {
		return core.Approval{}, err
	}
	planItemStatus := core.RunPlanItemRunning
	if input.Decision == core.ApprovalRejected {
		planItemStatus = core.RunPlanItemBlocked
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE run_plan_items SET status = ?, updated_at = ?,
		       completed_at = CASE WHEN ? = 'blocked' THEN ? ELSE completed_at END,
		       result_summary = CASE WHEN ? = 'blocked' THEN ? ELSE result_summary END
		WHERE id = (SELECT run_plan_item_id FROM human_action_requests WHERE id = ?)`,
		planItemStatus, now, planItemStatus, now, planItemStatus, resultSummary, approvalID,
	); err != nil {
		return core.Approval{}, err
	}
	q := s.queries.WithTx(tx)
	if input.Decision == core.ApprovalRejected {
		if err := s.blockToolBatchAfterApproval(ctx, tx, q, approval.OrganizationID, approval.ProjectID, approval.RunID, approval.ToolCallID, "APPROVAL_REJECTED", now, scope.UserID, correlationID); err != nil {
			return core.Approval{}, err
		}
	}
	if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeConversation {
		var conversationID, argumentsJSON string
		if err := tx.QueryRowContext(ctx, `
			SELECT t.conversation_id, tc.arguments_json
			FROM tasks t
			JOIN tool_calls tc ON tc.id = ?
			WHERE t.id = ?`, approval.ToolCallID, approval.TaskID).Scan(&conversationID, &argumentsJSON); err != nil {
			return core.Approval{}, err
		}
		if ruleKey, matchJSON, ok := approvalRuleSignature(approval.ToolName, argumentsJSON); ok {
			ruleID, err := newID()
			if err != nil {
				return core.Approval{}, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO conversation_approval_rules
				(id, organization_id, project_id, conversation_id, tool_name, rule_key, created_from_approval_id, created_by, created_at, expires_at, rule_version, match_json, match_hash, risk_level)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 2, ?, ?, ?)`,
				ruleID, approval.OrganizationID, approval.ProjectID, conversationID, approval.ToolName, ruleKey,
				approval.ID, scope.UserID, now, formatTime(approval.ExpiresAt), matchJSON, ruleKey, approval.RiskLevel); err != nil {
				return core.Approval{}, err
			}
		}
	}
	if input.Decision == core.ApprovalApproved && input.Scope == core.ApprovalScopeProject {
		var argumentsJSON string
		if err := tx.QueryRowContext(ctx, `SELECT arguments_json FROM tool_calls WHERE id = ?`, approval.ToolCallID).Scan(&argumentsJSON); err != nil {
			return core.Approval{}, err
		}
		if ruleKey, matchJSON, ok := approvalRuleSignature(approval.ToolName, argumentsJSON); ok {
			ruleID, err := newID()
			if err != nil {
				return core.Approval{}, err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO project_approval_rules
				(id, organization_id, project_id, tool_name, rule_key, created_from_approval_id, created_by, created_at, expires_at, revoked_at, revoked_by, rule_version, match_json, match_hash, risk_level)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 2, ?, ?, ?)
				ON CONFLICT(project_id, tool_name, rule_key) DO UPDATE SET
					created_from_approval_id = excluded.created_from_approval_id,
					created_by = excluded.created_by,
					created_at = excluded.created_at,
					expires_at = excluded.expires_at,
					rule_version = excluded.rule_version,
					match_json = excluded.match_json,
					match_hash = excluded.match_hash,
					risk_level = excluded.risk_level,
					revoked_at = '',
					revoked_by = ''`,
				ruleID, approval.OrganizationID, approval.ProjectID, approval.ToolName, ruleKey,
				approval.ID, scope.UserID, now, formatTime(time.Now().UTC().Add(30*24*time.Hour)), matchJSON, ruleKey, approval.RiskLevel); err != nil {
				return core.Approval{}, err
			}
		}
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: approval.OrganizationID, ProjectID: approval.ProjectID, RunID: approval.RunID, EventType: "approval.decided", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: core.VisibilityPrivate, Payload: map[string]any{"approval_id": approval.ID, "tool_call_id": approval.ToolCallID, "step_id": approval.StepID, "decision": input.Decision, "approval_scope": input.Scope, "input_hash": approval.InputHash, "parameters_changed": false}}); err != nil {
		return core.Approval{}, err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: approval.OrganizationID, ProjectID: approval.ProjectID, RunID: approval.RunID, EventType: "human_action.resolved", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: core.VisibilityPrivate, Payload: map[string]any{"human_action_id": approval.ID, "kind": core.HumanActionToolApproval, "status": humanStatus, "decision": input.Decision, "scope": input.Scope}}); err != nil {
		return core.Approval{}, err
	}
	updated, err := getApprovalForUser(ctx, tx, scope, approvalID)
	if err != nil {
		return core.Approval{}, err
	}
	if err := saveIdempotency(ctx, tx, scope, route, idempotency, 200, updated); err != nil {
		return core.Approval{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Approval{}, err
	}
	return updated, nil
}

func (s *Store) ClaimNextStep(ctx context.Context, workerID string, now, leaseUntil time.Time) (core.ClaimedStep, error) {
	// Keep the common idle poll read-only. The UPDATE ... RETURNING below is
	// still the atomic claim/CAS, but it is attempted only when a candidate
	// exists so four idle Workers do not continuously contend with HTTP writes
	// and active checkpoint transactions for SQLite's single writer.
	const findBatchAwareCandidate = `
		SELECT candidate.id
		FROM steps AS candidate
		JOIN runs AS candidate_run ON candidate_run.id = candidate.run_id
		WHERE (candidate.status = 'queued'
		   OR (candidate.status = 'running' AND candidate.lease_until <> '' AND candidate.lease_until < ?))
		  AND candidate_run.status IN ('queued', 'running')
		  AND (
		      EXISTS (
		          SELECT 1 FROM steps same_run
		          WHERE same_run.run_id = candidate.run_id
		            AND same_run.status = 'running'
		            AND same_run.lease_until >= ?
		      )
		      OR (
		          SELECT COUNT(DISTINCT active.run_id)
		          FROM steps active
		          WHERE active.status = 'running'
		            AND active.lease_until >= ?
		      ) < 4
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM steps AS earlier
		      WHERE earlier.run_id = candidate.run_id
		        AND earlier.position < candidate.position
		        AND earlier.status IN ('queued', 'running', 'waiting_approval')
		        AND (
		            candidate.tool_batch_id = ''
		            OR earlier.tool_batch_id <> candidate.tool_batch_id
		            OR NOT EXISTS (
		                SELECT 1 FROM tool_batches batch
		                WHERE batch.id = candidate.tool_batch_id
		                  AND batch.execution_mode = 'parallel'
		            )
		        )
		  )
		  AND (
		      candidate.tool_batch_id = ''
		      OR EXISTS (
		          SELECT 1 FROM tool_batches parallel_batch
		          WHERE parallel_batch.id = candidate.tool_batch_id
		            AND parallel_batch.execution_mode = 'parallel'
		      )
		      OR NOT EXISTS (
		          SELECT 1 FROM steps batch_failure
		          WHERE batch_failure.tool_batch_id = candidate.tool_batch_id
		            AND batch_failure.status IN ('blocked', 'failed', 'canceled')
		      )
		  )
		ORDER BY candidate.created_at ASC, candidate.id ASC
		LIMIT 1`
	nowText := formatTime(now)
	var candidateID string
	if err := s.db.QueryRowContext(ctx, findBatchAwareCandidate, nowText, nowText, nowText).Scan(&candidateID); errors.Is(err, sql.ErrNoRows) {
		return core.ClaimedStep{}, core.ErrNoWork
	} else if err != nil {
		return core.ClaimedStep{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ClaimedStep{}, err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	const claimNextBatchAwareStep = `
		UPDATE steps
		SET status = 'running',
		    owner_id = ?,
		    lease_until = ?,
		    fence_token = fence_token + 1,
		    attempt_count = attempt_count + 1,
		    updated_at = ?
		WHERE id = (
		    SELECT candidate.id
		    FROM steps AS candidate
		    JOIN runs AS candidate_run ON candidate_run.id = candidate.run_id
		    WHERE (candidate.status = 'queued'
		       OR (candidate.status = 'running' AND candidate.lease_until <> '' AND candidate.lease_until < ?))
		      AND candidate_run.status IN ('queued', 'running')
		      AND (
		          EXISTS (
		              SELECT 1 FROM steps same_run
		              WHERE same_run.run_id = candidate.run_id
		                AND same_run.status = 'running'
		                AND same_run.lease_until >= ?
		          )
		          OR (
		              SELECT COUNT(DISTINCT active.run_id)
		              FROM steps active
		              WHERE active.status = 'running'
		                AND active.lease_until >= ?
		          ) < 4
		      )
		      AND NOT EXISTS (
		          SELECT 1
		          FROM steps AS earlier
		          WHERE earlier.run_id = candidate.run_id
		            AND earlier.position < candidate.position
		            AND earlier.status IN ('queued', 'running', 'waiting_approval')
		            AND (
		                candidate.tool_batch_id = ''
		                OR earlier.tool_batch_id <> candidate.tool_batch_id
		                OR NOT EXISTS (
		                    SELECT 1 FROM tool_batches batch
		                    WHERE batch.id = candidate.tool_batch_id
		                      AND batch.execution_mode = 'parallel'
		                )
		            )
		      )
		      AND (
		          candidate.tool_batch_id = ''
		          OR EXISTS (
		              SELECT 1 FROM tool_batches parallel_batch
		              WHERE parallel_batch.id = candidate.tool_batch_id
		                AND parallel_batch.execution_mode = 'parallel'
		          )
		          OR NOT EXISTS (
		              SELECT 1 FROM steps batch_failure
		              WHERE batch_failure.tool_batch_id = candidate.tool_batch_id
		                AND batch_failure.status IN ('blocked', 'failed', 'canceled')
		          )
		      )
		    ORDER BY candidate.created_at ASC, candidate.id ASC
		    LIMIT 1
		)
		RETURNING id, organization_id, project_id, run_id, step_type, position, status,
		          input_json, output_json, attempt_count, owner_id, lease_until,
		          fence_token, error_code, created_at, updated_at, agent_id`
	var row sqlitegen.Step
	err = tx.QueryRowContext(ctx, claimNextBatchAwareStep, workerID, formatTime(leaseUntil), nowText, nowText, nowText, nowText).Scan(
		&row.ID,
		&row.OrganizationID,
		&row.ProjectID,
		&row.RunID,
		&row.StepType,
		&row.Position,
		&row.Status,
		&row.InputJson,
		&row.OutputJson,
		&row.AttemptCount,
		&row.OwnerID,
		&row.LeaseUntil,
		&row.FenceToken,
		&row.ErrorCode,
		&row.CreatedAt,
		&row.UpdatedAt,
		&row.AgentID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ClaimedStep{}, core.ErrNoWork
	}
	if err != nil {
		return core.ClaimedStep{}, err
	}
	if row.AttemptCount > 1 {
		if err := q.LosePreviousStepAttempts(ctx, sqlitegen.LosePreviousStepAttemptsParams{FinishedAt: formatTime(now), StepID: row.ID}); err != nil {
			return core.ClaimedStep{}, err
		}
	}
	attemptID, err := newID()
	if err != nil {
		return core.ClaimedStep{}, err
	}
	attemptKind := "initial"
	if row.AttemptCount > 1 {
		attemptKind = "recovery"
	}
	if err := q.CreateStepAttempt(ctx, sqlitegen.CreateStepAttemptParams{
		ID:             attemptID,
		OrganizationID: row.OrganizationID,
		ProjectID:      row.ProjectID,
		RunID:          row.RunID,
		StepID:         row.ID,
		AttemptNumber:  row.AttemptCount,
		AttemptKind:    attemptKind,
		OwnerID:        row.OwnerID,
		FenceToken:     row.FenceToken,
		StartedAt:      formatTime(now),
	}); err != nil {
		return core.ClaimedStep{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ClaimedStep{}, err
	}
	return core.ClaimedStep{ID: row.ID, AttemptID: attemptID, AttemptNumber: row.AttemptCount, Recovery: row.AttemptCount > 1, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, RunID: row.RunID, StepType: row.StepType, AgentID: row.AgentID, InputJSON: row.InputJson, OwnerID: row.OwnerID, FenceToken: row.FenceToken}, nil
}

func (s *Store) HeartbeatStep(ctx context.Context, step core.ClaimedStep, now, leaseUntil time.Time) (bool, error) {
	count, err := s.queries.HeartbeatStep(ctx, sqlitegen.HeartbeatStepParams{LeaseUntil: formatTime(leaseUntil), UpdatedAt: formatTime(now), ID: step.ID, OwnerID: step.OwnerID, FenceToken: step.FenceToken})
	return count == 1, err
}

func (s *Store) GetRunContext(ctx context.Context, runID string) (core.RunContext, error) {
	row, err := s.queries.GetRunContext(ctx, runID)
	if err != nil {
		return core.RunContext{}, err
	}
	var configSnapshot string
	var requestedProviderID, requestedModelID, effectiveProviderID, effectiveModelID string
	var thinking, fallbackReason, catalogVersion string
	if err := s.db.QueryRowContext(ctx, `
SELECT config_snapshot_json, requested_provider_id, requested_model_id,
       effective_provider_id, effective_model_id, thinking_level,
       model_fallback_reason, model_catalog_version
FROM runs WHERE id = ?`, runID).Scan(
		&configSnapshot, &requestedProviderID, &requestedModelID,
		&effectiveProviderID, &effectiveModelID, &thinking,
		&fallbackReason, &catalogVersion,
	); err != nil {
		return core.RunContext{}, err
	}
	return core.RunContext{
		RunID: row.RunID, TaskID: row.TaskID, ProjectID: row.ProjectID,
		OrganizationID: row.OrganizationID, OwnerUserID: row.OwnerUserID,
		ConversationID: row.ConversationID, Title: row.Title, Goal: row.Goal,
		Acceptance: row.Acceptance, Mode: row.Mode, Visibility: row.Visibility,
		ConfigSnapshotJSON: configSnapshot,
		RequestedModel:     core.ModelRef{ProviderID: requestedProviderID, ModelID: requestedModelID},
		EffectiveModel:     core.ModelRef{ProviderID: effectiveProviderID, ModelID: effectiveModelID},
		Thinking:           thinking, ModelFallbackReason: fallbackReason, ModelCatalogVersion: catalogVersion,
	}, nil
}

func (s *Store) MarkStepStarted(ctx context.Context, step core.ClaimedStep, run core.RunContext, correlationID string) error {
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	if step.AgentID != "" {
		if _, err := q.UpdateAgentTeamMemberStatus(ctx, sqlitegen.UpdateAgentTeamMemberStatusParams{Status: "working", CurrentStepID: step.ID, Model: "", UpdatedAt: now, ID: step.AgentID, RunID: run.RunID}); err != nil {
			return err
		}
	}
	if err := q.UpdateTaskResult(ctx, sqlitegen.UpdateTaskResultParams{Status: core.StatusRunning, UpdatedAt: now, ID: run.TaskID}); err != nil {
		return err
	}
	if err := q.UpdateRunStatus(ctx, sqlitegen.UpdateRunStatusParams{Status: core.StatusRunning, UpdatedAt: now, ID: run.RunID}); err != nil {
		return err
	}
	if step.Recovery {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "step.recovered", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "attempt_id": step.AttemptID, "attempt_number": step.AttemptNumber, "fence_token": step.FenceToken}}); err != nil {
			return err
		}
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "step.started", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "attempt_id": step.AttemptID, "attempt_number": step.AttemptNumber, "fence_token": step.FenceToken}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CommitCheckpoint(ctx context.Context, step core.ClaimedStep, run core.RunContext, kind string, state map[string]any, correlationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM steps
		WHERE id = ? AND owner_id = ? AND fence_token = ? AND status = 'running'`,
		step.ID, step.OwnerID, step.FenceToken,
	).Scan(&owned); err != nil {
		return err
	}
	if owned != 1 {
		return fmt.Errorf("LEASE_LOST: cannot checkpoint step %s fence %d", step.ID, step.FenceToken)
	}
	q := s.queries.WithTx(tx)
	checkpointID, contentHash, err := createCheckpoint(ctx, q, step, run, kind, state)
	if err != nil {
		return err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "checkpoint.committed", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"checkpoint_id": checkpointID, "checkpoint_kind": kind, "content_hash": contentHash, "step_id": step.ID, "attempt_id": step.AttemptID, "fence_token": step.FenceToken}}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BeginToolExecution(ctx context.Context, step core.ClaimedStep, run core.RunContext, call core.ToolCall, inputHash string, idempotent bool, correlationID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var owned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE id = ? AND owner_id = ? AND fence_token = ? AND status = 'running'`, step.ID, step.OwnerID, step.FenceToken).Scan(&owned); err != nil {
		return "", err
	}
	if owned != 1 {
		return "", fmt.Errorf("LEASE_LOST: cannot execute tool step %s fence %d", step.ID, step.FenceToken)
	}
	var storedName string
	var storedHash string
	var storedRisk string
	var storedDecision string
	var toolStatus string
	if err := tx.QueryRowContext(ctx, `SELECT tool_name, arguments_hash, risk_level, policy_decision, status FROM tool_calls WHERE id = ? AND step_id = ? AND run_id = ?`, call.ID, step.ID, run.RunID).Scan(&storedName, &storedHash, &storedRisk, &storedDecision, &toolStatus); err != nil {
		return "", err
	}
	planned := core.PlanTool(call)
	if strings.HasPrefix(call.Name, "mcp__") {
		risk, plannedIdempotent, reason := mcpPolicyFromSnapshot(run.ConfigSnapshotJSON, call.Name)
		planned.RiskLevel = risk
		planned.Idempotent = plannedIdempotent
		if reason != "" {
			planned.Reason = reason
		}
	}
	authorization := core.ExecutionAuthorization{
		ToolIdentity: storedName, InputHash: storedHash,
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		RiskLevel: storedRisk, PolicyDecision: storedDecision, PolicyVersion: core.ToolPolicyVersion,
	}
	if authErr := core.ValidateExecutionAuthorization(planned, authorization); authErr != nil || storedHash != inputHash || (toolStatus != "queued" && toolStatus != "running") {
		if authErr == nil {
			authErr = errors.New("frozen tool input or state no longer matches")
		}
		return "", fmt.Errorf("TOOL_AUTHORIZATION_INVALID: %w", authErr)
	}

	var receiptID string
	var invocationID string
	var receiptStatus string
	var storedIdempotent int64
	queryErr := tx.QueryRowContext(ctx, `SELECT id, invocation_id, status, idempotent FROM side_effect_receipts WHERE tool_call_id = ?`, call.ID).Scan(&receiptID, &invocationID, &receiptStatus, &storedIdempotent)
	unknown := false
	now := formatTime(time.Now())
	if errors.Is(queryErr, sql.ErrNoRows) {
		receiptID, err = newID()
		if err != nil {
			return "", err
		}
		invocationID, err = newID()
		if err != nil {
			return "", err
		}
		idempotentValue := 0
		if idempotent {
			idempotentValue = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO side_effect_receipts (
				id, organization_id, project_id, task_id, run_id, step_id, tool_call_id,
				invocation_id, input_hash, idempotent, status, started_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'started', ?)`,
			receiptID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, step.ID, call.ID,
			invocationID, inputHash, idempotentValue, now,
		); err != nil {
			return "", err
		}
	} else if queryErr != nil {
		return "", queryErr
	} else if !idempotent || storedIdempotent != 1 {
		unknown = true
		if _, err := tx.ExecContext(ctx, `UPDATE side_effect_receipts SET status = 'unknown', error_code = 'SIDE_EFFECT_STATUS_UNKNOWN', finished_at = ? WHERE id = ?`, now, receiptID); err != nil {
			return "", err
		}
	} else if _, err := tx.ExecContext(ctx, `
			UPDATE side_effect_receipts
			SET status = 'started', exit_code = -1, output_json = '{}',
			    error_code = '', started_at = ?, finished_at = ''
			WHERE id = ?`, now, receiptID); err != nil {
		return "", err
	}
	if unknown {
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'blocked', error_code = 'SIDE_EFFECT_STATUS_UNKNOWN', updated_at = ? WHERE id = ?`, now, call.ID); err != nil {
			return "", err
		}
		var planRevisionID, planItemID string
		_ = tx.QueryRowContext(ctx, `SELECT plan_revision_id, run_plan_item_id FROM steps WHERE id = ?`, step.ID).Scan(&planRevisionID, &planItemID)
		expiresAt := formatTime(time.Now().UTC().Add(core.DefaultApprovalTTL))
		requestJSON, _ := json.Marshal(map[string]any{"receipt_id": receiptID, "tool_call_id": call.ID, "tool_name": call.Name, "input_hash": inputHash, "invocation_id": invocationID})
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO human_action_requests (
				id, organization_id, project_id, conversation_id, task_id, run_id,
				step_id, plan_revision_id, run_plan_item_id, kind, status, reviewer,
				risk_level, title, reason, request_json, request_hash, requested_by,
				requested_at, expires_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'side_effect_resolution', 'waiting_user', 'user', 'L3', ?, ?, ?, ?, ?, ?, ?)`,
			receiptID, run.OrganizationID, run.ProjectID, run.ConversationID, run.TaskID, run.RunID,
			step.ID, planRevisionID, planItemID, call.Name, "系统无法确认外部操作是否已执行，必须人工核查后才能继续。",
			string(requestJSON), inputHash, step.OwnerID, now, expiresAt,
		); err != nil {
			return "", err
		}
		if planItemID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'waiting_user', updated_at = ? WHERE id = ?`, now, planItemID); err != nil {
				return "", err
			}
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'running', updated_at = ? WHERE id = ?`, now, call.ID); err != nil {
		return "", err
	}
	q := s.queries.WithTx(tx)
	eventType := "tool.execution_started"
	if unknown {
		eventType = "tool.side_effect_unknown"
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: eventType, ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"tool_call_id": call.ID, "step_id": step.ID, "tool_name": call.Name, "input_summary": toolInputSummary(call.Name, call.Arguments), "invocation_id": invocationID, "input_hash": inputHash, "idempotent": idempotent}}); err != nil {
		return "", err
	}
	if unknown {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.requested", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": receiptID, "kind": core.HumanActionSideEffectResolution, "status": core.HumanActionWaitingUser, "title": call.Name}}); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	if unknown {
		return "", fmt.Errorf("SIDE_EFFECT_STATUS_UNKNOWN: refusing to repeat non-idempotent invocation %s", invocationID)
	}
	return invocationID, nil
}

func (s *Store) FinishStep(ctx context.Context, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, correlationID string) error {
	now := formatTime(time.Now())
	output, err := json.Marshal(outcome.Output)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)
	// Release the pre-execution reservation before accounting the actual model
	// usage. This keeps the reservation bounded to the in-flight call while the
	// completion transaction remains the source of truth for measured usage.
	var reservedTokens int64
	if err := tx.QueryRowContext(ctx, `SELECT budget_reserved_tokens FROM steps WHERE id = ?`, step.ID).Scan(&reservedTokens); err == nil && reservedTokens > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_budgets
			SET reserved_model_calls = CASE WHEN reserved_model_calls > 0 THEN reserved_model_calls - 1 ELSE 0 END,
			    reserved_tokens = CASE WHEN reserved_tokens >= ? THEN reserved_tokens - ? ELSE 0 END,
			    updated_at = ?
			WHERE run_id = ?`, reservedTokens, reservedTokens, now, run.RunID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET budget_reserved_tokens = 0 WHERE id = ?`, step.ID); err != nil {
			return err
		}
	}
	if outcome.Model != nil {
		var maxCalls, maxTokens int64
		var budgetStatus string
		if err := tx.QueryRowContext(ctx, `SELECT max_model_calls, max_tokens, status FROM run_budgets WHERE run_id = ?`, run.RunID).Scan(&maxCalls, &maxTokens, &budgetStatus); err == nil && budgetStatus == "active" {
			var calls, tokens int64
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(input_tokens + output_tokens), 0) FROM model_calls WHERE run_id = ?`, run.RunID).Scan(&calls, &tokens); err != nil {
				return err
			}
			incoming := outcome.Model.Usage.InputTokens + outcome.Model.Usage.OutputTokens
			if (maxCalls > 0 && calls+1 > maxCalls) || (maxTokens > 0 && tokens+incoming > maxTokens) {
				outcome.Status = core.StatusBlocked
				outcome.ErrorCode = "BUDGET_EXCEEDED"
				outcome.ResultSummary = fmt.Sprintf("Run budget reached (calls %d/%d, tokens %d/%d); choose a smaller scope or approve an exception.", calls+1, maxCalls, tokens+incoming, maxTokens)
				output, err = json.Marshal(outcome.Output)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE run_budgets SET status = 'exhausted', updated_at = ? WHERE run_id = ?`, now, run.RunID); err != nil {
					return err
				}
			}
		}
	}
	count, err := q.CompleteStep(ctx, sqlitegen.CompleteStepParams{Status: outcome.Status, OutputJson: string(output), ErrorCode: outcome.ErrorCode, UpdatedAt: now, ID: step.ID, OwnerID: step.OwnerID, FenceToken: step.FenceToken})
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("LEASE_LOST: step %s fence %d is no longer owned", step.ID, step.FenceToken)
	}
	attemptCount, err := q.FinishStepAttempt(ctx, sqlitegen.FinishStepAttemptParams{Status: outcome.Status, ErrorCode: outcome.ErrorCode, FinishedAt: now, ID: step.AttemptID, OwnerID: step.OwnerID, FenceToken: step.FenceToken})
	if err != nil {
		return err
	}
	if attemptCount != 1 {
		return fmt.Errorf("ATTEMPT_LOST: attempt %s is no longer active", step.AttemptID)
	}
	checkpointID, contentHash, err := createCheckpoint(ctx, q, step, run, "after_execution", map[string]any{"status": outcome.Status, "error_code": outcome.ErrorCode, "output": outcome.Output, "result_summary": outcome.ResultSummary})
	if err != nil {
		return err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "checkpoint.committed", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"checkpoint_id": checkpointID, "checkpoint_kind": "after_execution", "content_hash": contentHash, "step_id": step.ID, "attempt_id": step.AttemptID, "fence_token": step.FenceToken}}); err != nil {
		return err
	}
	if outcome.Model != nil {
		if err := persistModelUsage(ctx, tx, q, step, run, *outcome.Model, now); err != nil {
			return err
		}
	}

	taskStatus := outcome.Status
	taskSummary := outcome.ResultSummary
	taskErrorCode := outcome.ErrorCode
	modelRetryStepID := ""
	modelRetryCount := 0
	toolRecoveryStepID := ""
	toolRecoveryCount := 0
	toolRecoveryCallID := ""
	if outcome.ErrorCode == "BUDGET_RESERVATION_EXCEEDED" || outcome.ErrorCode == "BUDGET_EXCEEDED" {
		if err := s.createBudgetHumanAction(ctx, tx, q, step, run, outcome, now, correlationID); err != nil {
			return err
		}
		taskStatus, taskSummary, taskErrorCode = core.StatusWaitingInput, "本次运行预算不足，等待你确认扩展后继续。", ""
	}
	reviewStep := step.StepType == core.StepHumanActionReview
	if reviewStep {
		taskStatus, taskSummary, taskErrorCode, err = s.applyAutoReviewOutcome(ctx, tx, q, step, run, outcome, now, correlationID)
		if err != nil {
			return err
		}
	}
	if !reviewStep && outcome.Status == core.StatusSucceeded && outcome.Model != nil && strings.TrimSpace(outcome.ResultSummary) != "" {
		phase := "final"
		if len(outcome.Model.ToolCalls) > 0 || len(lengthTruncatedToolCalls(outcome.Output)) > 0 {
			phase = "progress"
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.message", ActorType: "agent", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "phase": phase, "content": outcome.ResultSummary, "model": outcome.Model.Model, "tool_call_count": len(outcome.Model.ToolCalls), "tool_argument_repairs": outcome.Model.ToolArgumentRepairs}}); err != nil {
			return err
		}
	}
	if !reviewStep && outcome.Status == core.StatusSucceeded && outcome.Model != nil && len(outcome.Model.ToolCalls) > 0 {
		taskStatus, taskErrorCode, err = s.planToolSteps(ctx, tx, q, step, run, outcome.Model.ToolCalls, now, correlationID)
		if err != nil {
			return err
		}
	}
	if !reviewStep && outcome.Status == core.StatusSucceeded && outcome.Model != nil {
		if calls := lengthTruncatedToolCalls(outcome.Output); len(calls) > 0 {
			if err := s.recordLengthTruncatedToolCalls(ctx, tx, q, step, run, calls, now, correlationID); err != nil {
				return err
			}
			continuationStepID, queueErr := s.queueLengthTruncatedModelContinuation(ctx, tx, q, step, run, now, correlationID)
			if queueErr != nil {
				return queueErr
			}
			if continuationStepID == "" {
				return fmt.Errorf("MODEL_CONTINUATION_NOT_QUEUED: length-truncated tool calls have no recovery step")
			}
			taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
		}
	}
	if run.Mode == core.ModeSingleAgent && outcome.Status == core.StatusFailed && outcome.Model != nil && isRetryableModelInteractionError(outcome.ErrorCode) {
		var queued bool
		modelRetryStepID, modelRetryCount, queued, err = s.queueModelInteractionRetry(ctx, tx, step, run, outcome.ErrorCode, now)
		if err != nil {
			return err
		}
		if queued {
			taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
		}
	}
	if outcome.ToolExecution != nil {
		if err := s.finishToolExecution(ctx, tx, q, step, run, *outcome.ToolExecution, now, correlationID); err != nil {
			return err
		}
		if outcome.Status == core.StatusSucceeded {
			taskStatus, taskErrorCode, err = deriveRunStatus(ctx, tx, run.RunID)
			if err != nil {
				return err
			}
			if taskStatus == core.StatusSucceeded {
				batchReady, continuationClaimed, batchID, batchErr := s.completeToolBatchState(ctx, tx, q, step, run, now, correlationID)
				if batchErr != nil {
					return batchErr
				}
				if !batchReady {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				} else if !continuationClaimed {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				} else {
					queued := false
					var queueErr error
					var queuedStepID string
					if step.StepType == core.ToolSkillInvoke {
						queued, queueErr = s.queueSkillResume(ctx, tx, q, step, run, outcome.Output, now, correlationID)
					} else {
						forceSynthesis := false
						if step.StepType == core.ToolWebSearch {
							forceSynthesis, queueErr = webSearchSynthesisThresholdReached(ctx, tx, run.RunID)
						}
						if queueErr == nil && forceSynthesis {
							queued, queueErr = s.queueWebSearchSynthesis(ctx, tx, q, step, run, now, correlationID)
						} else if queueErr == nil {
							var continuationStepID string
							continuationStepID, queueErr = s.queueModelContinuation(ctx, tx, q, step, run, now, correlationID)
							queued = continuationStepID != ""
							queuedStepID = continuationStepID
						}
					}
					if queueErr != nil {
						return queueErr
					}
					if queued {
						if batchID != "" && queuedStepID != "" {
							if _, err := tx.ExecContext(ctx, `UPDATE tool_batches SET continuation_step_id = ?, updated_at = ? WHERE id = ?`, queuedStepID, now, batchID); err != nil {
								return err
							}
						}
						taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
					} else if step.StepType != core.ToolSkillInvoke {
						return fmt.Errorf("MODEL_CONTINUATION_NOT_QUEUED: completed tool batch has no next model step")
					}
				}
			}
		} else {
			batchReady, recoveryClaimed, _, batchErr := s.completeToolBatchState(ctx, tx, q, step, run, now, correlationID)
			if batchErr != nil {
				return batchErr
			}
			if !batchReady || !recoveryClaimed {
				taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
			} else if outcome.Status == core.StatusFailed && isRecoverableToolExecutionFailure(outcome.ErrorCode) {
				var queued bool
				toolRecoveryStepID, toolRecoveryCount, toolRecoveryCallID, queued, err = s.queueToolFailureRecovery(ctx, tx, q, step, run, outcome.ErrorCode, now, correlationID)
				if err != nil {
					return err
				}
				if queued {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				} else {
					synthesized, synthErr := s.queueWebSearchSynthesis(ctx, tx, q, step, run, now, correlationID)
					if synthErr != nil {
						return synthErr
					}
					if synthesized {
						taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
					} else if actionErr := s.createToolRecoveryHumanAction(ctx, tx, q, step, run, outcome.ErrorCode, now, correlationID); actionErr != nil {
						return actionErr
					} else {
						taskStatus, taskSummary, taskErrorCode = core.StatusWaitingInput, "自动恢复已用尽，需要你选择下一步。", ""
					}
				}
			}
		}
	} else if step.StepType == core.ToolWorkspaceWriteFile || step.StepType == core.ToolWorkspaceRunCommand {
		if outcome.Status == core.StatusBlocked {
			if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'blocked', error_code = ?, updated_at = ? WHERE step_id = ?`, outcome.ErrorCode, now, step.ID); err != nil {
				return err
			}
		} else if outcome.Status == core.StatusFailed {
			if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'failed', error_code = ?, updated_at = ? WHERE step_id = ?`, outcome.ErrorCode, now, step.ID); err != nil {
				return err
			}
			if isRecoverableToolExecutionFailure(outcome.ErrorCode) {
				var queued bool
				toolRecoveryStepID, toolRecoveryCount, toolRecoveryCallID, queued, err = s.queueToolFailureRecovery(ctx, tx, q, step, run, outcome.ErrorCode, now, correlationID)
				if err != nil {
					return err
				}
				if queued {
					taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
				}
			}
		}
	}
	if run.Mode == core.ModeAgentTeam {
		taskStatus, taskSummary, taskErrorCode, err = s.advanceAgentTeam(ctx, tx, q, step, run, outcome, now, correlationID)
		if err != nil {
			return err
		}
	}
	if run.Mode == core.ModeSingleAgent && taskStatus == core.StatusSucceeded && outcome.Model != nil && len(outcome.Model.ToolCalls) == 0 {
		reason, invariantErr := completionInvariantFailure(ctx, tx, run)
		if invariantErr != nil {
			return invariantErr
		}
		if reason != "" {
			if modelStageFromInput(step.InputJSON) == "completion_recovery" {
				taskStatus, taskErrorCode = core.StatusBlocked, reason
				if reason == "WEB_EVIDENCE_MISSING" {
					taskSummary = "任务要求联网检索，但当前 Run 没有成功的互联网检索收据，不能标记为完成。"
				} else {
					taskSummary = "任务要求生成文档交付物，但当前 Run 没有成功写入任何工作区文件，不能标记为完成。"
				}
			} else {
				history, historyErr := s.collectContinuationHistory(ctx, tx, run)
				if historyErr != nil {
					return historyErr
				}
				nextStepID, queueErr := insertQueuedModelStep(ctx, tx, run, map[string]any{
					"history": history, "stage": "completion_recovery",
					"source_step_id": step.ID, "completion_recovery_reason": reason,
				}, now)
				if queueErr != nil {
					return queueErr
				}
				planRevisionID, planItemID := activeExecutionPlanBinding(ctx, tx, run.RunID)
				if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, planRevisionID, planItemID, nextStepID); err != nil {
					return err
				}
				if err := s.appendEvent(ctx, q, eventInput{
					OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
					EventType: "completion.validation_failed", ActorType: "validator", ActorID: "runtime",
					CorrelationID: correlationID, Visibility: run.Visibility,
					Payload: map[string]any{"source_step_id": step.ID, "recovery_step_id": nextStepID, "reason": reason},
				}); err != nil {
					return err
				}
				taskStatus, taskSummary, taskErrorCode = core.StatusRunning, "", ""
			}
		}
	}
	if err := s.syncRunPlanState(ctx, tx, q, run, taskStatus, taskSummary, now, correlationID); err != nil {
		return err
	}

	if err := q.UpdateTaskResult(ctx, sqlitegen.UpdateTaskResultParams{Status: taskStatus, ResultSummary: taskSummary, ErrorCode: taskErrorCode, UpdatedAt: now, ID: run.TaskID}); err != nil {
		return err
	}
	if err := q.UpdateRunStatus(ctx, sqlitegen.UpdateRunStatusParams{Status: taskStatus, UpdatedAt: now, ID: run.RunID}); err != nil {
		return err
	}

	if taskStatus == core.StatusSucceeded || taskStatus == core.StatusFailed || taskStatus == core.StatusBlocked {
		messageContent := strings.TrimSpace(taskSummary)
		if messageContent == "" && taskErrorCode != "" {
			messageContent = "任务未完成，错误代码：" + taskErrorCode
		}
		if messageContent != "" {
			if err := s.appendConversationMessage(ctx, tx, q, run, "assistant", messageContent, now); err != nil {
				return err
			}
		}
	}
	eventType := "step." + outcome.Status
	eventPayload := map[string]any{"step_id": step.ID, "attempt_id": step.AttemptID, "attempt_number": step.AttemptNumber, "status": outcome.Status, "error_code": outcome.ErrorCode, "result_summary": outcome.ResultSummary}
	if outcome.Model != nil {
		eventPayload["model"] = outcome.Model.Model
		eventPayload["provider"] = outcome.Model.Provider
		eventPayload["routing_reason"] = outcome.Model.RoutingReason
		eventPayload["input_tokens"] = outcome.Model.Usage.InputTokens
		eventPayload["output_tokens"] = outcome.Model.Usage.OutputTokens
		eventPayload["usage_estimated"] = outcome.Model.Usage.Estimated
		eventPayload["tool_argument_repairs"] = outcome.Model.ToolArgumentRepairs
	}
	if contextPlan, ok := outcome.Output["context_plan"].(map[string]any); ok {
		// Keep the deterministic context decision visible in the committed event
		// so UI/audit consumers do not need to read the full Step output blob.
		eventPayload["context_plan"] = contextPlan
		if skills, ok := contextPlan["skills"].(map[string]any); ok {
			if strategy, _ := skills["strategy"].(string); strategy == "explicit_skill_invocation_v1" {
				if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "skill.loaded", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": step.ID, "skill_ids": skills["loaded_ids"], "skill_names": skills["loaded_names"], "allowed_tools": skills["allowed_tools"], "status": "loaded"}}); err != nil {
					return err
				}
			}
		}
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: eventType, ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: eventPayload}); err != nil {
		return err
	}
	if modelRetryStepID != "" {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.retry_queued", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": modelRetryStepID, "source_step_id": step.ID, "retry_count": modelRetryCount, "max_retries": core.DefaultModelRetryLimit, "error_code": outcome.ErrorCode}}); err != nil {
			return err
		}
	}
	if toolRecoveryStepID != "" {
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.recovery_queued", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": toolRecoveryStepID, "source_step_id": step.ID, "tool_call_id": toolRecoveryCallID, "retry_count": toolRecoveryCount, "max_retries": core.DefaultToolRetryLimit, "error_code": outcome.ErrorCode}}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) applyAutoReviewOutcome(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, now, correlationID string) (string, string, string, error) {
	var input struct {
		ActionID   string `json:"human_action_id"`
		ToolStepID string `json:"tool_step_id"`
		ToolCallID string `json:"tool_call_id"`
		PlanItemID string `json:"run_plan_item_id"`
	}
	if json.Unmarshal([]byte(step.InputJSON), &input) != nil || input.ActionID == "" {
		return core.StatusBlocked, "自动审批检查点无效。", "REVIEW_INPUT_INVALID", nil
	}
	decision, _ := outcome.Output["decision"].(string)
	reason, _ := outcome.Output["reason"].(string)
	if decision != "approve" && decision != "reject" {
		decision = "escalate_to_user"
	}
	if strings.TrimSpace(reason) == "" {
		reason = outcome.ResultSummary
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.reviewed", ActorType: "reviewer", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": input.ActionID, "decision": decision, "reason": reason, "review_step_id": step.ID}}); err != nil {
		return "", "", "", err
	}
	responseJSON, _ := json.Marshal(map[string]any{"decision": decision, "reason": reason, "reviewer": "auto"})
	switch decision {
	case "approve":
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'approved', decided_by = 'auto-reviewer', decision_note = ?, decided_at = ? WHERE id = ? AND status = 'pending'`, reason, now, input.ActionID); err != nil {
			return "", "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE human_action_requests SET status = 'approved', response_json = ?, decided_by = 'auto-reviewer', decided_at = ?, version = version + 1 WHERE id = ? AND status = 'reviewing'`, string(responseJSON), now, input.ActionID); err != nil {
			return "", "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'queued', error_code = '', updated_at = ? WHERE id = ? AND status = 'waiting_approval'`, now, input.ToolCallID); err != nil {
			return "", "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'queued', error_code = '', owner_id = '', lease_until = '', updated_at = ? WHERE id = ? AND status = 'waiting_approval'`, now, input.ToolStepID); err != nil {
			return "", "", "", err
		}
		if input.PlanItemID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'running', updated_at = ? WHERE id = ?`, now, input.PlanItemID); err != nil {
				return "", "", "", err
			}
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.resolved", ActorType: "reviewer", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": input.ActionID, "status": core.HumanActionApproved, "decision": decision}}); err != nil {
			return "", "", "", err
		}
		return core.StatusQueued, "自动审查已批准冻结动作，等待执行。", "", nil
	case "reject":
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = 'auto-reviewer', decision_note = ?, decided_at = ? WHERE id = ? AND status = 'pending'`, reason, now, input.ActionID); err != nil {
			return "", "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE human_action_requests SET status = 'rejected', response_json = ?, decided_by = 'auto-reviewer', decided_at = ?, version = version + 1 WHERE id = ? AND status = 'reviewing'`, string(responseJSON), now, input.ActionID); err != nil {
			return "", "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'rejected', error_code = 'AUTO_REVIEW_REJECTED', updated_at = ? WHERE id = ?`, now, input.ToolCallID); err != nil {
			return "", "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'canceled', error_code = 'AUTO_REVIEW_REJECTED', updated_at = ? WHERE id = ?`, now, input.ToolStepID); err != nil {
			return "", "", "", err
		}
		if input.PlanItemID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'blocked', result_summary = ?, completed_at = ?, updated_at = ? WHERE id = ?`, reason, now, now, input.PlanItemID); err != nil {
				return "", "", "", err
			}
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.resolved", ActorType: "reviewer", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": input.ActionID, "status": core.HumanActionRejected, "decision": decision, "reason": reason}}); err != nil {
			return "", "", "", err
		}
		return core.StatusBlocked, "自动审查拒绝了风险动作。", "AUTO_REVIEW_REJECTED", nil
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE human_action_requests SET status = 'waiting_user', reviewer = 'user', response_json = ?, version = version + 1 WHERE id = ? AND status = 'reviewing'`, string(responseJSON), input.ActionID); err != nil {
			return "", "", "", err
		}
		if input.PlanItemID != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'waiting_user', updated_at = ? WHERE id = ?`, now, input.PlanItemID); err != nil {
				return "", "", "", err
			}
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.requested", ActorType: "reviewer", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": input.ActionID, "status": core.HumanActionWaitingUser, "reason": reason, "fallback": "auto_review_unavailable_or_escalated"}}); err != nil {
			return "", "", "", err
		}
		return core.StatusWaitingApproval, "自动审查转为人工审批。", "", nil
	}
}

func (s *Store) createBudgetHumanAction(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, now, correlationID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_action_requests WHERE run_id = ? AND step_id = ? AND kind = 'budget_request' AND status IN ('waiting_user', 'reviewing')`, run.RunID, step.ID).Scan(&exists); err != nil || exists > 0 {
		return err
	}
	var maxCalls, maxTokens int64
	if err := tx.QueryRowContext(ctx, `SELECT max_model_calls, max_tokens FROM run_budgets WHERE run_id = ?`, run.RunID).Scan(&maxCalls, &maxTokens); err != nil {
		return err
	}
	estimated, _ := numericToolValue(outcome.Output["estimated_tokens"])
	if estimated < 1 {
		estimated = int64(core.DefaultModelOutputTokens)
	}
	request := map[string]any{
		"current_max_model_calls":  maxCalls,
		"current_max_tokens":       maxTokens,
		"proposed_max_model_calls": maxCalls + 1,
		"proposed_max_tokens":      maxTokens + estimated,
		"estimated_tokens":         estimated,
		"impact_scope":             "仅当前 Run",
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(requestJSON)
	actionID, err := newID()
	if err != nil {
		return err
	}
	planRevisionID, planItemID := activeExecutionPlanBinding(ctx, tx, run.RunID)
	expiresAt := formatTime(time.Now().UTC().Add(core.DefaultApprovalTTL))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO human_action_requests (
			id, organization_id, project_id, conversation_id, task_id, run_id,
			step_id, plan_revision_id, run_plan_item_id, kind, status, reviewer,
			risk_level, title, reason, request_json, request_hash, requested_by,
			requested_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'budget_request', 'waiting_user', 'user', 'L1', ?, ?, ?, ?, ?, ?, ?)`,
		actionID, run.OrganizationID, run.ProjectID, run.ConversationID, run.TaskID, run.RunID,
		step.ID, planRevisionID, planItemID, "确认扩展本次运行预算",
		outcome.ResultSummary, string(requestJSON), "sha256:"+hex.EncodeToString(sum[:]), step.OwnerID, now, expiresAt,
	); err != nil {
		return err
	}
	if planItemID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'waiting_user', updated_at = ? WHERE id = ?`, now, planItemID); err != nil {
			return err
		}
	}
	return s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "human_action.requested", ActorType: "system", ActorID: step.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"human_action_id": actionID, "kind": core.HumanActionBudgetRequest, "status": core.HumanActionWaitingUser, "plan_revision_id": planRevisionID, "run_plan_item_id": planItemID},
	})
}

func (s *Store) createToolRecoveryHumanAction(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, reason, now, correlationID string) error {
	var existing int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_action_requests WHERE run_id = ? AND kind = 'input_request' AND status IN ('waiting_user', 'reviewing')`, run.RunID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	actionID, err := newID()
	if err != nil {
		return err
	}
	planRevisionID, planItemID := activeExecutionPlanBinding(ctx, tx, run.RunID)
	request := map[string]any{
		"prompt":            "自动恢复和可用替代路径均已尝试，但当前能力仍无法继续。请选择下一步。",
		"choices":           []string{"基于已有证据继续并明确缺口", "我补充新的要求或资源", "停止本次任务"},
		"failure_code":      reason,
		"recovery_attempts": core.DefaultToolRetryLimit,
	}
	requestJSON, _ := json.Marshal(request)
	sum := sha256.Sum256(requestJSON)
	requestHash := "sha256:" + hex.EncodeToString(sum[:])
	expiresAt := formatTime(time.Now().UTC().Add(core.DefaultApprovalTTL))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO human_action_requests (
			id, organization_id, project_id, conversation_id, task_id, run_id,
			step_id, plan_revision_id, run_plan_item_id, kind, status, reviewer,
			risk_level, title, reason, request_json, request_hash, requested_by,
			requested_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'input_request', 'waiting_user', 'user',
		          'L0', ?, ?, ?, ?, ?, ?, ?)`,
		actionID, run.OrganizationID, run.ProjectID, run.ConversationID, run.TaskID, run.RunID,
		step.ID, planRevisionID, planItemID, "需要你决定恢复方式",
		"系统已完成受控重试、替代工具评估和已有证据综合，仍缺少可自动获得的条件。",
		string(requestJSON), requestHash, step.OwnerID, now, expiresAt,
	); err != nil {
		return err
	}
	if planItemID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'waiting_user', updated_at = ? WHERE id = ?`, now, planItemID); err != nil {
			return err
		}
	}
	return s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "human_action.requested", ActorType: "system", ActorID: step.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"human_action_id": actionID, "kind": core.HumanActionInputRequest, "status": core.HumanActionWaitingUser, "plan_revision_id": planRevisionID, "run_plan_item_id": planItemID, "reason": reason},
	})
}

func completionInvariantFailure(ctx context.Context, tx *sql.Tx, run core.RunContext) (string, error) {
	goal := strings.ToLower(strings.TrimSpace(run.Goal))
	var successfulSearches, failedSearches int64
	if err := tx.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM tool_calls WHERE run_id = ? AND tool_name = ?`, run.RunID, core.ToolWebSearch,
	).Scan(&successfulSearches, &failedSearches); err != nil {
		return "", err
	}
	requiresWeb := strings.Contains(goal, "检索") || strings.Contains(goal, "调研") ||
		strings.Contains(goal, "最新") || strings.Contains(goal, "互联网") ||
		strings.Contains(goal, "research") || strings.Contains(goal, "search")
	if requiresWeb && successfulSearches == 0 && failedSearches > 0 {
		return "WEB_EVIDENCE_MISSING", nil
	}
	requiresDocument := strings.Contains(goal, "md文档") || strings.Contains(goal, "markdown") ||
		strings.Contains(goal, "形成文档") || strings.Contains(goal, "生成文档") ||
		strings.Contains(goal, "写入工作区")
	if requiresDocument {
		var successfulWrites int64
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM tool_calls
			WHERE run_id = ? AND tool_name = ? AND status = 'succeeded'`,
			run.RunID, core.ToolWorkspaceWriteFile,
		).Scan(&successfulWrites); err != nil {
			return "", err
		}
		if successfulWrites == 0 {
			return "DELIVERABLE_MISSING", nil
		}
	}
	return "", nil
}

func (s *Store) syncRunPlanState(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, run core.RunContext, taskStatus, summary, now, correlationID string) error {
	var revisionID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM run_plan_revisions WHERE run_id = ? AND status = 'active' ORDER BY revision DESC LIMIT 1`, run.RunID).Scan(&revisionID)
	if errors.Is(err, sql.ErrNoRows) && (taskStatus == core.StatusSucceeded || taskStatus == core.StatusFailed || taskStatus == core.StatusBlocked) {
		// A retried legacy Run may still carry a blocked plan revision even
		// after its denied tool batch has been superseded. Terminal task state
		// must reconcile that latest durable plan instead of leaving stale
		// pending items such as 0/3 beside a succeeded task.
		err = tx.QueryRowContext(ctx, `
			SELECT id FROM run_plan_revisions
			WHERE run_id = ? AND status = 'blocked'
			ORDER BY revision DESC LIMIT 1`, run.RunID).Scan(&revisionID)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	switch taskStatus {
	case core.StatusQueued, core.StatusRunning:
		// Running progress is owned by the model's complete update_plan payload.
		// Tool batches only bind evidence to the current semantic item; they must
		// never invent or mechanically advance user-visible plan steps.
	case core.StatusWaitingApproval, core.StatusWaitingInput:
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_plan_items SET status = 'waiting_user', updated_at = ?
			WHERE id = (
				SELECT id FROM run_plan_items WHERE plan_revision_id = ? AND status IN ('pending', 'running')
				ORDER BY position LIMIT 1
			)`, now, revisionID); err != nil {
			return err
		}
	case core.StatusSucceeded:
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_plan_items SET status = 'completed',
			       started_at = CASE WHEN started_at = '' THEN ? ELSE started_at END,
			       completed_at = CASE WHEN completed_at = '' THEN ? ELSE completed_at END,
			       result_summary = CASE WHEN position = (SELECT MAX(position) FROM run_plan_items WHERE plan_revision_id = ?) THEN ? ELSE result_summary END,
			       updated_at = ?
			WHERE plan_revision_id = ? AND status <> 'completed'`, now, now, revisionID, truncateText(summary, 1000), now, revisionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'completed' WHERE id = ?`, revisionID); err != nil {
			return err
		}
	case core.StatusFailed, core.StatusBlocked:
		if _, err := tx.ExecContext(ctx, `
			UPDATE run_plan_items SET status = 'blocked', result_summary = ?, completed_at = ?, updated_at = ?
			WHERE id = (
				SELECT id FROM run_plan_items WHERE plan_revision_id = ? AND status IN ('running', 'waiting_user', 'pending')
				ORDER BY CASE status WHEN 'running' THEN 0 WHEN 'waiting_user' THEN 1 ELSE 2 END, position LIMIT 1
			)`, truncateText(summary, 1000), now, now, revisionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'blocked' WHERE id = ?`, revisionID); err != nil {
			return err
		}
	case core.StatusCanceled:
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'canceled', completed_at = ?, updated_at = ? WHERE plan_revision_id = ? AND status NOT IN ('completed', 'canceled')`, now, now, revisionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'canceled' WHERE id = ?`, revisionID); err != nil {
			return err
		}
	}
	return s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "plan.state_changed", ActorType: "system", ActorID: "runtime",
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"plan_revision_id": revisionID, "task_status": taskStatus},
	})
}

func (s *Store) advanceAgentTeam(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, now, correlationID string) (string, string, string, error) {
	if step.AgentID == "" {
		return core.StatusBlocked, "", "AGENT_TEAM_STATE_INVALID", fmt.Errorf("agent team step %s has no agent id", step.ID)
	}
	memberStatus := "failed"
	if outcome.Status == core.StatusSucceeded {
		memberStatus = "done"
		if step.StepType == core.StepAgentCoordinatorPlan {
			memberStatus = "waiting"
		}
	}
	model := ""
	if outcome.Model != nil {
		model = outcome.Model.Model
	}
	if _, err := q.UpdateAgentTeamMemberStatus(ctx, sqlitegen.UpdateAgentTeamMemberStatusParams{Status: memberStatus, CurrentStepID: step.ID, Model: model, UpdatedAt: now, ID: step.AgentID, RunID: run.RunID}); err != nil {
		return "", "", "", err
	}
	messageType := "result"
	if step.StepType == core.StepAgentCoordinatorPlan {
		messageType = "plan"
	} else if step.StepType == core.StepAgentCoordinatorSynthesis {
		messageType = "synthesis"
	}
	if strings.TrimSpace(outcome.ResultSummary) != "" {
		message, err := s.appendAgentTeamMessage(ctx, q, run, step.AgentID, messageType, outcome.ResultSummary, now)
		if err != nil {
			return "", "", "", err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "agent.message", ActorType: "agent", ActorID: step.AgentID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"message_id": message.ID, "message_type": message.MessageType, "server_seq": message.ServerSeq, "content_hash": message.ContentHash, "step_id": step.ID}}); err != nil {
			return "", "", "", err
		}
	}
	if outcome.Status != core.StatusSucceeded {
		// A transient Worker/Provider failure is recoverable once without
		// re-running successful siblings. The retry is a new persisted Step with
		// the same Agent member and an explicit retry counter in its frozen input.
		if step.StepType == core.StepAgentWorker {
			var input map[string]any
			_ = json.Unmarshal([]byte(step.InputJSON), &input)
			retryCount := 0
			if value, ok := input["retry_count"].(float64); ok {
				retryCount = int(value)
			}
			if retryCount < 1 {
				input["retry_count"] = retryCount + 1
				input["retry_reason"] = outcome.ErrorCode
				retryInput, err := json.Marshal(input)
				if err != nil {
					return "", "", "", err
				}
				retryStepID, err := newID()
				if err != nil {
					return "", "", "", err
				}
				var nextPosition int64
				if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, run.RunID).Scan(&nextPosition); err != nil {
					return "", "", "", err
				}
				if _, err := q.CreateAgentStep(ctx, sqlitegen.CreateAgentStepParams{ID: retryStepID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, StepType: core.StepAgentWorker, Position: nextPosition, InputJson: string(retryInput), AgentID: step.AgentID, CreatedAt: now, UpdatedAt: now}); err != nil {
					return "", "", "", err
				}
				if _, err := q.UpdateAgentTeamMemberStatus(ctx, sqlitegen.UpdateAgentTeamMemberStatusParams{Status: "planned", CurrentStepID: retryStepID, Model: model, UpdatedAt: now, ID: step.AgentID, RunID: run.RunID}); err != nil {
					return "", "", "", err
				}
				message, err := s.appendAgentTeamMessage(ctx, q, run, step.AgentID, "review", fmt.Sprintf("%s 执行失败（%s），已排队第 %d 次受控重试。", inputString(input, "role"), outcome.ErrorCode, retryCount+1), now)
				if err != nil {
					return "", "", "", err
				}
				if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "agent.worker_retry_queued", ActorType: "coordinator", ActorID: step.AgentID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"failed_step_id": step.ID, "retry_step_id": retryStepID, "retry_count": retryCount + 1, "error_code": outcome.ErrorCode, "message_id": message.ID}}); err != nil {
					return "", "", "", err
				}
				return core.StatusRunning, "", "", nil
			}
		}
		return outcome.Status, outcome.ResultSummary, outcome.ErrorCode, nil
	}
	switch step.StepType {
	case core.StepAgentCoordinatorPlan:
		roles := teamRolesFromSnapshot(run.ConfigSnapshotJSON)
		for index, role := range roles {
			agentID, err := newID()
			if err != nil {
				return "", "", "", err
			}
			workerStepID, err := newID()
			if err != nil {
				return "", "", "", err
			}
			input, err := json.Marshal(map[string]any{"goal": run.Goal, "acceptance": run.Acceptance, "mode": run.Mode, "stage": "work", "role": role.Role, "depends_on": []string{step.ID}, "coordinator_plan": truncateText(outcome.ResultSummary, 2000), "retry_count": 0})
			if err != nil {
				return "", "", "", err
			}
			if _, err := q.CreateAgentTeamMember(ctx, sqlitegen.CreateAgentTeamMemberParams{ID: agentID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, TaskID: run.TaskID, RunID: run.RunID, Role: role.Role, DisplayName: role.Name, Status: "planned", CurrentStepID: workerStepID, CreatedAt: now, UpdatedAt: now}); err != nil {
				return "", "", "", err
			}
			if _, err := q.CreateAgentStep(ctx, sqlitegen.CreateAgentStepParams{ID: workerStepID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, StepType: core.StepAgentWorker, Position: int64(index + 2), InputJson: string(input), AgentID: agentID, CreatedAt: now, UpdatedAt: now}); err != nil {
				return "", "", "", err
			}
			handoff, err := s.appendAgentTeamMessage(ctx, q, run, step.AgentID, "handoff", fmt.Sprintf("已将 %s 子任务交给 %s；依赖计划步骤 %s。", role.Role, role.Name, step.ID), now)
			if err != nil {
				return "", "", "", err
			}
			if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "agent.handoff", ActorType: "agent", ActorID: step.AgentID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"from_agent_id": step.AgentID, "to_agent_id": agentID, "role": role.Role, "step_id": workerStepID, "depends_on_step_id": step.ID, "message_id": handoff.ID}}); err != nil {
				return "", "", "", err
			}
		}
		return core.StatusRunning, "", "", nil
	case core.StepAgentWorker:
		if err := s.persistAgentTeamWorkerArtifact(ctx, tx, q, step, run, outcome.ResultSummary, now, correlationID); err != nil {
			return "", "", "", err
		}
		var doneWorkers int64
		var failedWorkers int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_team_members WHERE run_id = ? AND role <> 'coordinator' AND status = 'done'`, run.RunID).Scan(&doneWorkers); err != nil {
			return "", "", "", err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_team_members WHERE run_id = ? AND role <> 'coordinator' AND status = 'failed'`, run.RunID).Scan(&failedWorkers); err != nil {
			return "", "", "", err
		}
		if failedWorkers > 0 {
			return core.StatusFailed, "Agent Team 有 Worker 失败，未进入最终合成。", "AGENT_WORKER_FAILED", nil
		}
		if doneWorkers < int64(len(teamRolesFromSnapshot(run.ConfigSnapshotJSON))) {
			return core.StatusRunning, "", "", nil
		}
		var coordinatorID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_team_members WHERE run_id = ? AND role = 'coordinator'`, run.RunID).Scan(&coordinatorID); err != nil {
			return "", "", "", err
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT m.role, msg.content
			FROM agent_team_messages msg
			JOIN agent_team_members m ON m.id = msg.sender_agent_id
			WHERE msg.run_id = ? AND msg.message_type = 'result'
			ORDER BY CASE m.role WHEN 'researcher' THEN 1 WHEN 'builder' THEN 2 ELSE 3 END`, run.RunID)
		if err != nil {
			return "", "", "", err
		}
		workerResults := make([]string, 0, len(teamRolesFromSnapshot(run.ConfigSnapshotJSON)))
		for rows.Next() {
			var role, content string
			if err := rows.Scan(&role, &content); err != nil {
				_ = rows.Close()
				return "", "", "", err
			}
			workerResults = append(workerResults, role+": "+truncateText(content, 2500))
		}
		if err := rows.Close(); err != nil {
			return "", "", "", err
		}
		synthesisStepID, err := newID()
		if err != nil {
			return "", "", "", err
		}
		dependsOnRoles := make([]string, 0, len(teamRolesFromSnapshot(run.ConfigSnapshotJSON)))
		for _, role := range teamRolesFromSnapshot(run.ConfigSnapshotJSON) {
			dependsOnRoles = append(dependsOnRoles, role.Role)
		}
		input, err := json.Marshal(map[string]any{"goal": run.Goal, "acceptance": run.Acceptance, "mode": run.Mode, "stage": "synthesis", "role": "coordinator", "depends_on_roles": dependsOnRoles, "worker_results": workerResults})
		if err != nil {
			return "", "", "", err
		}
		if _, err := q.CreateAgentStep(ctx, sqlitegen.CreateAgentStepParams{ID: synthesisStepID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, StepType: core.StepAgentCoordinatorSynthesis, Position: 5, InputJson: string(input), AgentID: coordinatorID, CreatedAt: now, UpdatedAt: now}); err != nil {
			return "", "", "", err
		}
		if _, err := q.UpdateAgentTeamMemberStatus(ctx, sqlitegen.UpdateAgentTeamMemberStatusParams{Status: "planned", CurrentStepID: synthesisStepID, Model: "", UpdatedAt: now, ID: coordinatorID, RunID: run.RunID}); err != nil {
			return "", "", "", err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "agent_team.synthesis_queued", ActorType: "agent", ActorID: coordinatorID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": synthesisStepID, "worker_count": len(teamRolesFromSnapshot(run.ConfigSnapshotJSON)), "dependency_state": "satisfied"}}); err != nil {
			return "", "", "", err
		}
		return core.StatusRunning, "", "", nil
	case core.StepAgentCoordinatorSynthesis:
		var input struct {
			WorkerResults []string `json:"worker_results"`
		}
		if err := json.Unmarshal([]byte(step.InputJSON), &input); err != nil || len(input.WorkerResults) < len(teamRolesFromSnapshot(run.ConfigSnapshotJSON)) || strings.TrimSpace(outcome.ResultSummary) == "" {
			return core.StatusFailed, "Agent Team 终止门未通过：缺少完整 Worker 证据或最终合成。", "AGENT_TERMINATION_GATE_FAILED", nil
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "agent_team.termination_gate", ActorType: "coordinator", ActorID: step.AgentID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"worker_count": len(input.WorkerResults), "evidence_complete": true, "synthesis_non_empty": true, "status": "passed"}}); err != nil {
			return "", "", "", err
		}
		return core.StatusSucceeded, outcome.ResultSummary, "", nil
	default:
		return core.StatusBlocked, "", "AGENT_TEAM_STATE_INVALID", fmt.Errorf("unknown agent team step type %s", step.StepType)
	}
}

func teamRolesFromSnapshot(snapshot string) []core.TeamRole {
	roles := []core.TeamRole{}
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) == nil {
		if raw, ok := config["team_roles"]; ok {
			if encoded, err := json.Marshal(raw); err == nil {
				_ = json.Unmarshal(encoded, &roles)
			}
		}
	}
	if len(roles) == 0 {
		return []core.TeamRole{{Role: "researcher", Name: "Researcher"}, {Role: "builder", Name: "Builder"}, {Role: "reviewer", Name: "Reviewer"}}
	}
	return roles
}

func inputString(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

// persistAgentTeamWorkerArtifact records each worker handoff as a verified,
// content-addressed artifact. This keeps the evidence chain inspectable before
// coordinator synthesis and makes duplicate model responses idempotent at the
// blob layer.
func (s *Store) persistAgentTeamWorkerArtifact(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, summary, now, correlationID string) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("agent team worker %s returned empty result", step.ID)
	}
	persisted, err := s.persistBlob(ctx, []byte(summary), "text/markdown; charset=utf-8")
	if err != nil {
		return fmt.Errorf("persist agent team worker blob: %w", err)
	}
	if err := ensureBlob(ctx, tx, persisted, now); err != nil {
		return err
	}
	artifactID, err := newID()
	if err != nil {
		return err
	}
	role := "worker"
	var input struct {
		Role string `json:"role"`
	}
	if json.Unmarshal([]byte(step.InputJSON), &input) == nil && strings.TrimSpace(input.Role) != "" {
		role = strings.ToLower(strings.TrimSpace(input.Role))
	}
	row, err := q.CreateArtifact(ctx, sqlitegen.CreateArtifactParams{
		ID: artifactID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID,
		TaskID: run.TaskID, RunID: run.RunID, StepID: step.ID,
		Name: role + "-result.md", Kind: "evidence", MediaType: persisted.MediaType,
		ContentHash: persisted.ContentHash, SizeBytes: persisted.SizeBytes,
		Visibility: run.Visibility, SourceType: "agent_team_worker", CreatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("create agent team worker artifact: %w", err)
	}
	return s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "artifact.created", ActorType: "agent", ActorID: step.AgentID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"artifact_id": row.ID, "name": row.Name, "kind": row.Kind, "source_type": row.SourceType, "content_hash": row.ContentHash, "size_bytes": row.SizeBytes, "role": role},
	})
}

func (s *Store) appendAgentTeamMessage(ctx context.Context, q *sqlitegen.Queries, run core.RunContext, senderAgentID, messageType, content, now string) (sqlitegen.AgentTeamMessage, error) {
	sequence, err := q.AllocateAgentTeamMessageSequence(ctx, run.RunID)
	if err != nil {
		return sqlitegen.AgentTeamMessage{}, err
	}
	id, err := newID()
	if err != nil {
		return sqlitegen.AgentTeamMessage{}, err
	}
	sum := sha256.Sum256([]byte(content))
	return q.CreateAgentTeamMessage(ctx, sqlitegen.CreateAgentTeamMessageParams{ID: id, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, TaskID: run.TaskID, RunID: run.RunID, SenderAgentID: senderAgentID, ServerSeq: sequence, MessageType: messageType, Content: content, ContentHash: fmt.Sprintf("sha256:%x", sum[:]), Visibility: run.Visibility, CreatedAt: now})
}

func truncateText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func persistModelUsage(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, model core.ModelResult, now string) error {
	modelStatus := "succeeded"
	if model.ErrorCode != "" {
		modelStatus = "failed"
	}
	estimated := int64(0)
	confirmed := int64(1)
	if model.Usage.Estimated {
		estimated = 1
		confirmed = 0
	}
	pricingSnapshot, costDecimal := resolveModelPricing(model.Model, model.Usage)
	if err := q.CreateModelCall(ctx, sqlitegen.CreateModelCallParams{ID: model.CallID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, TaskID: run.TaskID, RunID: run.RunID, StepID: step.ID, Provider: model.Provider, Endpoint: model.Endpoint, Model: model.Model, RoutingReason: model.RoutingReason, Status: modelStatus, ProviderRequestID: model.ProviderRequestID, InputTokens: model.Usage.InputTokens, OutputTokens: model.Usage.OutputTokens, CachedTokens: model.Usage.CachedTokens, ReasoningTokens: model.Usage.ReasoningTokens, UsageEstimated: estimated, LatencyMs: model.Latency.Milliseconds(), ErrorCode: model.ErrorCode, CreatedAt: now, PricingSnapshotJson: pricingSnapshot}); err != nil {
		return err
	}
	requested, effective := persistedModelRefs(model)
	if _, err := tx.ExecContext(ctx, `UPDATE model_calls SET
		purpose = ?, requested_provider_id = ?, requested_model_id = ?,
		effective_provider_id = ?, effective_model_id = ?, stop_reason = ?,
		context_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
		usage_semantics_version = ?
		WHERE id = ?`,
		step.StepType, requested.ProviderID, requested.ModelID,
		effective.ProviderID, effective.ModelID, model.StopReason,
		model.Usage.ContextTokens, persistedCacheReadTokens(model.Usage), model.Usage.CacheWriteTokens,
		persistedUsageVersion(model.Usage), model.CallID); err != nil {
		return err
	}
	fallbackReason := ""
	if model.RoutingReason == "fallback" {
		fallbackReason = "primary_model_unavailable"
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE runs SET
    effective_provider_id = ?,
    effective_model_id = ?,
    model_fallback_reason = CASE WHEN ? <> '' THEN ? ELSE model_fallback_reason END,
    updated_at = ?
WHERE id = ?`,
		effective.ProviderID, effective.ModelID, fallbackReason, fallbackReason, now, run.RunID); err != nil {
		return err
	}
	ledgerID, err := newID()
	if err != nil {
		return err
	}
	return q.CreateUsageLedger(ctx, sqlitegen.CreateUsageLedgerParams{ID: ledgerID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, TaskID: run.TaskID, RunID: run.RunID, StepID: step.ID, CallID: model.CallID, InputTokens: model.Usage.InputTokens, OutputTokens: model.Usage.OutputTokens, CachedTokens: model.Usage.CachedTokens, CostDecimal: costDecimal, Currency: "USD", Confirmed: confirmed, CreatedAt: now, PricingSnapshotJson: pricingSnapshot})
}

func persistedModelRefs(model core.ModelResult) (core.ModelRef, core.ModelRef) {
	requested := model.RequestedModel
	if requested.ProviderID == "" {
		requested.ProviderID = model.Provider
	}
	if requested.ModelID == "" {
		requested.ModelID = model.Model
	}
	effective := model.EffectiveModel
	if effective.ProviderID == "" {
		effective.ProviderID = model.Provider
	}
	if effective.ModelID == "" {
		effective.ModelID = model.Model
	}
	return requested, effective
}

func persistedCacheReadTokens(usage core.ModelUsage) int64 {
	if usage.CacheReadTokens > 0 {
		return usage.CacheReadTokens
	}
	return usage.CachedTokens
}

func persistedUsageVersion(usage core.ModelUsage) string {
	if strings.TrimSpace(usage.UsageSemanticsVersion) != "" {
		return strings.TrimSpace(usage.UsageSemanticsVersion)
	}
	return "v1"
}

type modelPricing struct {
	InputPer1K  float64 `json:"input_per_1k"`
	OutputPer1K float64 `json:"output_per_1k"`
}

func resolveModelPricing(model string, usage core.ModelUsage) (string, string) {
	config := strings.TrimSpace(os.Getenv("OKTAPOD_MODEL_PRICING_JSON"))
	if config == "" {
		return pricingSnapshotJSON(model, "unpriced", 0, 0, ""), "0"
	}
	var prices map[string]modelPricing
	if json.Unmarshal([]byte(config), &prices) != nil {
		return pricingSnapshotJSON(model, "invalid", 0, 0, ""), "0"
	}
	rate, ok := prices[model]
	if !ok {
		rate, ok = prices["default"]
	}
	if !ok {
		return pricingSnapshotJSON(model, "unpriced", 0, 0, ""), "0"
	}
	cost := (float64(usage.InputTokens)/1000)*rate.InputPer1K + (float64(usage.OutputTokens)/1000)*rate.OutputPer1K
	return pricingSnapshotJSON(model, "priced", rate.InputPer1K, rate.OutputPer1K, pricingVersion(config)), strconv.FormatFloat(cost, 'f', 8, 64)
}

func estimateModelCost(model string, usage core.ModelUsage) string {
	_, cost := resolveModelPricing(model, usage)
	return cost
}

func pricingSnapshotJSON(model, status string, inputPer1K, outputPer1K float64, version string) string {
	payload := map[string]any{"model": model, "status": status, "currency": "USD", "input_per_1k": inputPer1K, "output_per_1k": outputPer1K, "version": version}
	data, err := json.Marshal(payload)
	if err != nil {
		return `{"model":"","status":"invalid","currency":"USD"}`
	}
	return string(data)
}

func pricingVersion(config string) string {
	sum := sha256.Sum256([]byte(config))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func modelStepSummary(ctx context.Context, tx *sql.Tx, stepID string) string {
	var outputJSON string
	if err := tx.QueryRowContext(ctx, `SELECT output_json FROM steps WHERE id = ?`, stepID).Scan(&outputJSON); err != nil {
		return ""
	}
	var output map[string]any
	if json.Unmarshal([]byte(outputJSON), &output) != nil {
		return ""
	}
	content, _ := output["content"].(string)
	return truncateText(strings.TrimSpace(content), 500)
}

func (s *Store) planHumanInteraction(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, modelStep core.ClaimedStep, run core.RunContext, call core.ToolCall, now, correlationID string) (string, string, error) {
	if call.Name == core.ToolRequestInput {
		prompt, _ := call.Arguments["prompt"].(string)
		if strings.TrimSpace(prompt) == "" || len([]rune(prompt)) > 1000 {
			return core.StatusBlocked, "HUMAN_ACTION_INPUT_INVALID", nil
		}
	} else {
		capability, _ := call.Arguments["capability"].(string)
		reason, _ := call.Arguments["reason"].(string)
		if capability != "workspace_command" || strings.TrimSpace(reason) == "" || len([]rune(reason)) > 1000 {
			return core.StatusBlocked, "HUMAN_ACTION_INPUT_INVALID", nil
		}
	}
	planned := core.PlanTool(call)
	if planned.InputHash == "" {
		return core.StatusBlocked, "HUMAN_ACTION_INPUT_INVALID", nil
	}
	stepID, err := newID()
	if err != nil {
		return "", "", err
	}
	actionID, err := newID()
	if err != nil {
		return "", "", err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, run.RunID).Scan(&position); err != nil {
		return "", "", err
	}
	planRevisionID, planItemID := activeExecutionPlanBinding(ctx, tx, run.RunID)
	inputJSON, _ := json.Marshal(map[string]any{
		"human_action_id": actionID, "name": call.Name, "arguments": call.Arguments,
		"input_hash": planned.InputHash, "model_source_step_id": modelStep.ID,
		"plan_revision_id": planRevisionID, "run_plan_item_id": planItemID,
	})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO steps (
			id, organization_id, project_id, run_id, step_type, position, status,
			input_json, plan_revision_id, run_plan_item_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, 'waiting_input', ?, ?, ?, ?, ?)`,
		stepID, run.OrganizationID, run.ProjectID, run.RunID, call.Name, position,
		string(inputJSON), planRevisionID, planItemID, now, now,
	); err != nil {
		return "", "", err
	}
	kind, title, reason := core.HumanActionInputRequest, "需要你补充信息", ""
	if call.Name == core.ToolRequestInput {
		reason, _ = call.Arguments["prompt"].(string)
	} else {
		kind, title = core.HumanActionPermissionEscalation, "需要临时启用命令能力"
		reason, _ = call.Arguments["reason"].(string)
	}
	expiresAt := formatTime(time.Now().UTC().Add(core.DefaultApprovalTTL))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO human_action_requests (
			id, organization_id, project_id, conversation_id, task_id, run_id,
			step_id, plan_revision_id, run_plan_item_id, kind, status, reviewer,
			risk_level, title, reason, request_json, request_hash, requested_by,
			requested_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'waiting_user', 'user', 'L0', ?, ?, ?, ?, ?, ?, ?)`,
		actionID, run.OrganizationID, run.ProjectID, run.ConversationID, run.TaskID, run.RunID,
		stepID, planRevisionID, planItemID, kind, title, reason, planned.ArgumentsJSON,
		planned.InputHash, modelStep.OwnerID, now, expiresAt,
	); err != nil {
		return "", "", err
	}
	if planItemID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'waiting_user', updated_at = ? WHERE id = ?`, now, planItemID); err != nil {
			return "", "", err
		}
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "human_action.requested", ActorType: "agent", ActorID: modelStep.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"human_action_id": actionID, "kind": kind, "status": core.HumanActionWaitingUser, "step_id": stepID, "plan_revision_id": planRevisionID, "run_plan_item_id": planItemID, "title": title, "reason": reason},
	}); err != nil {
		return "", "", err
	}
	return core.StatusWaitingInput, "", nil
}

func (s *Store) planToolSteps(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, modelStep core.ClaimedStep, run core.RunContext, calls []core.ToolCall, now, correlationID string) (string, string, error) {
	if len(calls) == 1 && calls[0].Name == core.ToolUpdatePlan {
		status, errorCode, err := s.publishModelRunPlan(ctx, tx, q, modelStep, run, calls[0], now, correlationID)
		if err != nil || status != core.StatusBlocked || !isRepairableRunPlanError(errorCode) {
			return status, errorCode, err
		}
		return s.queueRunPlanModelRepair(ctx, tx, q, modelStep, run, errorCode, now, correlationID)
	}
	for _, call := range calls {
		if call.Name == core.ToolUpdatePlan {
			return s.queueRunPlanModelRepair(ctx, tx, q, modelStep, run, "UPDATE_PLAN_MUST_BE_EXCLUSIVE", now, correlationID)
		}
	}
	provisionalPlan, err := activeRunPlanIsProvisional(ctx, tx, run.RunID)
	if err != nil {
		return "", "", err
	}
	if provisionalPlan {
		if err := s.materializeGoalFallbackPlan(ctx, tx, q, modelStep, run, now, correlationID); err != nil {
			return "", "", err
		}
	}
	if len(calls) == 1 && (calls[0].Name == core.ToolRequestInput || calls[0].Name == core.ToolRequestPermission) {
		return s.planHumanInteraction(ctx, tx, q, modelStep, run, calls[0], now, correlationID)
	}
	plans := make([]core.PlannedTool, 0, len(calls))
	batchID, err := newID()
	if err != nil {
		return "", "", err
	}
	executionMode := "parallel"
	for _, call := range calls {
		if call.Sequential {
			executionMode = "sequential"
			break
		}
	}
	hasDeny := false
	invokedSkillIDs, skillArgs := stepSkillInvocation(modelStep.InputJSON)
	hasSkillInvoke := false
	for _, call := range calls {
		if call.Name == core.ToolSkillInvoke {
			hasSkillInvoke = true
		}
	}
	violationCode := ""
	violationReason := ""
	if hasSkillInvoke && len(calls) != 1 {
		violationCode = "SKILL_INVOKE_MUST_BE_EXCLUSIVE"
		violationReason = "skill_invoke must be the only tool call in a model response"
	} else if hasSkillInvoke && len(invokedSkillIDs) > 0 {
		violationCode = "SKILL_RECURSIVE_INVOCATION_DENIED"
		violationReason = "a resumed or explicitly invoked Skill cannot invoke another Skill"
	}
	for _, call := range calls {
		plan, policyErr := s.executionPolicy.EvaluateExecution(ctx, application.Principal{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID}, application.ExecutionRequest{ProjectID: run.ProjectID, Call: call, ConfigSnapshot: run.ConfigSnapshotJSON})
		if policyErr != nil {
			return "", "", policyErr
		}
		if strings.HasPrefix(call.Name, "mcp__") {
			risk, idempotent, reason := mcpPolicyFromSnapshot(run.ConfigSnapshotJSON, call.Name)
			plan.RiskLevel = risk
			plan.Idempotent = idempotent
			if reason != "" {
				plan.Reason = reason
			}
		}
		if violationCode != "" {
			plan.Decision = core.PolicyDeny
			plan.Reason = violationReason
		}
		plans = append(plans, plan)
		if plan.Decision == core.PolicyDeny {
			hasDeny = true
		}
	}
	var maxPosition int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) FROM steps WHERE run_id = ?`, run.RunID).Scan(&maxPosition); err != nil {
		return "", "", err
	}
	toolCallIDs := make([]string, len(plans))
	stepIDs := make([]string, len(plans))
	planItems := make([]map[string]any, 0, len(plans))
	for index := range plans {
		toolCallID, err := newID()
		if err != nil {
			return "", "", err
		}
		stepID, err := newID()
		if err != nil {
			return "", "", err
		}
		toolCallIDs[index] = toolCallID
		stepIDs[index] = stepID
		plans[index].Call.ID = toolCallID
		planItems = append(planItems, map[string]any{
			"index":         index + 1,
			"step_id":       stepID,
			"tool_call_id":  toolCallID,
			"tool_name":     plans[index].Call.Name,
			"input_summary": toolInputSummary(plans[index].Call.Name, plans[index].Call.Arguments),
			"position":      maxPosition + int64(index) + 1,
		})
	}
	var planIteration int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) + 1 FROM run_events WHERE run_id = ? AND event_type = 'execution.plan_created'`, run.RunID).Scan(&planIteration); err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO tool_batches (
			id, organization_id, project_id, task_id, run_id, source_step_id,
			plan_iteration, execution_mode, status, tool_count, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'planned', ?, ?, ?)`,
		batchID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, modelStep.ID,
		planIteration, executionMode, len(calls), now, now,
	); err != nil {
		return "", "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "execution.plan_created", ActorType: "agent", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"plan_id": modelStep.ID, "source_step_id": modelStep.ID, "iteration": planIteration, "summary": strings.TrimSpace(modelStepSummary(ctx, tx, modelStep.ID)), "items": planItems}}); err != nil {
		return "", "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.batch_planned", ActorType: "agent", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"batch_id": batchID, "source_step_id": modelStep.ID, "iteration": planIteration, "execution_mode": executionMode, "items": planItems}}); err != nil {
		return "", "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.batch_created", ActorType: "agent", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"batch_id": batchID, "source_step_id": modelStep.ID, "plan_iteration": planIteration, "execution_mode": executionMode, "tool_count": len(calls)}}); err != nil {
		return "", "", err
	}
	hasQueued := false
	hasApproval := false
	planningTime, err := time.Parse(time.RFC3339Nano, now)
	if err != nil {
		planningTime = time.Now().UTC()
	}
	fullAccess := core.FullAccessActive(run.ConfigSnapshotJSON, planningTime)
	autoReview := permissionModeFromSnapshot(run.ConfigSnapshotJSON) == core.PermissionModeAutoReview
	planRevisionID, executionPlanItemID := activeExecutionPlanBinding(ctx, tx, run.RunID)
	for index, plan := range plans {
		// A full-access Run still goes through PlanTool's deterministic hard
		// denies, but its explicit 30-minute window suppresses per-command
		// approval creation. Applying this at planning time is essential: a
		// waiting-approval step is not claimable by the Worker.
		if plan.Decision == core.PolicyAsk && fullAccess && plan.Call.Name == core.ToolWorkspaceRunCommand {
			plan.Decision = core.PolicyAllow
		} else if plan.Decision == core.PolicyAsk {
			matched, err := approvalRuleMatches(ctx, tx, run, plan.Call, plan.RiskLevel, now)
			if err != nil {
				return "", "", err
			}
			if matched {
				plan.Decision = core.PolicyAllow
				plan.Reason = "本会话已批准同类动作，继续使用冻结参数执行"
			}
		}
		toolCallID := toolCallIDs[index]
		stepID := stepIDs[index]
		plan.Call.ID = toolCallID
		position := maxPosition + int64(index) + 1
		toolStatus := "queued"
		stepStatus := core.StatusQueued
		if hasDeny || plan.Decision == core.PolicyDeny {
			toolStatus = "blocked"
			stepStatus = core.StatusBlocked
		} else if plan.Decision == core.PolicyAsk {
			toolStatus = "waiting_approval"
			stepStatus = core.StatusWaitingApproval
			hasApproval = true
		} else {
			hasQueued = true
		}
		stepInputValue := map[string]any{"tool_call_id": toolCallID, "name": plan.Call.Name, "arguments": plan.Call.Arguments, "input_hash": plan.InputHash, "idempotent": plan.Idempotent, "model_source_step_id": modelStep.ID, "plan_iteration": planIteration, "tool_batch_id": batchID, "batch_index": index + 1, "batch_execution_mode": executionMode, "plan_revision_id": planRevisionID, "run_plan_item_id": executionPlanItemID}
		if len(invokedSkillIDs) > 0 {
			stepInputValue["invoked_skill_ids"] = invokedSkillIDs
			stepInputValue["skill_args"] = skillArgs
		}
		stepInput, err := json.Marshal(stepInputValue)
		if err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, tool_batch_id, batch_index, plan_revision_id, run_plan_item_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			stepID, run.OrganizationID, run.ProjectID, run.RunID, plan.Call.Name, position, stepStatus, string(stepInput), batchID, index+1, planRevisionID, executionPlanItemID, now, now,
		); err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, error_code, tool_batch_id, batch_index, plan_revision_id, run_plan_item_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			toolCallID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, stepID, position, plan.Call.Name, plan.ArgumentsJSON, plan.InputHash, plan.RiskLevel, plan.Decision, toolStatus, policyErrorCode(plan.Decision), batchID, index+1, planRevisionID, executionPlanItemID, now, now,
		); err != nil {
			return "", "", err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.planned", ActorType: "agent", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"tool_call_id": toolCallID, "step_id": stepID, "tool_name": plan.Call.Name, "position": position, "risk_level": plan.RiskLevel, "policy_decision": plan.Decision, "input_hash": plan.InputHash, "reason": plan.Reason}}); err != nil {
			return "", "", err
		}
		if !hasDeny && plan.Decision == core.PolicyAsk {
			approvalID, err := newID()
			if err != nil {
				return "", "", err
			}
			expiresAt := formatTime(time.Now().UTC().Add(core.DefaultApprovalTTL))
			actionStatus := core.HumanActionWaitingUser
			actionReviewer := core.HumanActionReviewerUser
			if autoReview {
				actionStatus = core.HumanActionReviewing
				actionReviewer = core.HumanActionReviewerAuto
				hasQueued = true
			}
			actionRequest, err := json.Marshal(map[string]any{
				"tool_name":       plan.Call.Name,
				"frozen_params":   plan.Call.Arguments,
				"impact_scope":    "当前计划项",
				"policy_reason":   plan.Reason,
				"policy_decision": plan.Decision,
			})
			if err != nil {
				return "", "", err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO approval_requests (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, tool_name, risk_level, status, reason, frozen_params_json, input_hash, requested_by, requested_at, expires_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?)`,
				approvalID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, stepID, toolCallID, plan.Call.Name, plan.RiskLevel, plan.Reason, plan.ArgumentsJSON, plan.InputHash, modelStep.OwnerID, now, expiresAt,
			); err != nil {
				return "", "", err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO human_action_requests (
					id, organization_id, project_id, conversation_id, task_id, run_id,
					step_id, plan_revision_id, run_plan_item_id, kind, status, reviewer,
					risk_level, title, reason, request_json, request_hash, requested_by,
					requested_at, expires_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'tool_approval', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				approvalID, run.OrganizationID, run.ProjectID, run.ConversationID, run.TaskID, run.RunID,
				stepID, planRevisionID, executionPlanItemID, actionStatus, actionReviewer, plan.RiskLevel, plan.Call.Name,
				plan.Reason, string(actionRequest), plan.InputHash, modelStep.OwnerID, now, expiresAt,
			); err != nil {
				return "", "", err
			}
			if executionPlanItemID != "" {
				planStatus := core.RunPlanItemWaitingUser
				if autoReview {
					planStatus = core.RunPlanItemRunning
				}
				if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = ?, updated_at = ? WHERE id = ? AND status IN ('pending', 'running')`, planStatus, now, executionPlanItemID); err != nil {
					return "", "", err
				}
			}
			if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "approval.requested", ActorType: "agent", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"approval_id": approvalID, "tool_call_id": toolCallID, "step_id": stepID, "tool_name": plan.Call.Name, "risk_level": plan.RiskLevel, "approval_scope": core.ApprovalScopeOnce, "input_hash": plan.InputHash, "reason": plan.Reason}}); err != nil {
				return "", "", err
			}
			if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.requested", ActorType: "agent", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": approvalID, "kind": core.HumanActionToolApproval, "status": actionStatus, "reviewer": actionReviewer, "plan_revision_id": planRevisionID, "run_plan_item_id": executionPlanItemID, "risk_level": plan.RiskLevel, "title": plan.Call.Name, "reason": plan.Reason}}); err != nil {
				return "", "", err
			}
			if autoReview {
				reviewStepID, err := newID()
				if err != nil {
					return "", "", err
				}
				reviewInput, err := json.Marshal(map[string]any{
					"human_action_id": approvalID, "approval_id": approvalID, "tool_step_id": stepID,
					"tool_call_id": toolCallID, "title": plan.Call.Name, "reason": plan.Reason,
					"risk_level": plan.RiskLevel, "tool_name": plan.Call.Name, "frozen_params": plan.Call.Arguments,
					"plan_revision_id": planRevisionID, "run_plan_item_id": executionPlanItemID,
				})
				if err != nil {
					return "", "", err
				}
				reviewPosition := maxPosition + int64(len(plans)) + int64(index) + 1
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, plan_revision_id, run_plan_item_id, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)`,
					reviewStepID, run.OrganizationID, run.ProjectID, run.RunID, core.StepHumanActionReview,
					reviewPosition, string(reviewInput), planRevisionID, executionPlanItemID, now, now,
				); err != nil {
					return "", "", err
				}
				if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "human_action.review_started", ActorType: "system", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"human_action_id": approvalID, "review_step_id": reviewStepID, "reviewer": core.HumanActionReviewerAuto}}); err != nil {
					return "", "", err
				}
			}
		}
	}
	batchStatus := "succeeded"
	if hasDeny {
		batchStatus = "blocked"
		if _, err := tx.ExecContext(ctx, `UPDATE tool_batches SET status = ?, updated_at = ? WHERE id = ?`, batchStatus, now, batchID); err != nil {
			return "", "", err
		}
		denialCode := violationCode
		if denialCode == "" {
			denialCode = "TOOL_POLICY_DENIED"
		}
		recoveryStepID, retryCount, queued, err := s.queueModelInteractionRetry(ctx, tx, modelStep, run, denialCode, now)
		if err != nil {
			return "", "", err
		}
		if !queued {
			return core.StatusBlocked, denialCode, nil
		}
		denials := make([]map[string]any, 0, len(plans))
		for _, plan := range plans {
			if plan.Decision != core.PolicyDeny {
				continue
			}
			denials = append(denials, map[string]any{
				"tool_name": plan.Call.Name,
				"reason":    plan.Reason,
			})
		}
		var recoveryInputJSON string
		if err := tx.QueryRowContext(ctx, `SELECT input_json FROM steps WHERE id = ?`, recoveryStepID).Scan(&recoveryInputJSON); err != nil {
			return "", "", err
		}
		var recoveryInput map[string]any
		if json.Unmarshal([]byte(recoveryInputJSON), &recoveryInput) != nil {
			return "", "", fmt.Errorf("MODEL_RETRY_INPUT_INVALID: recovery step input is not JSON")
		}
		recoveryInput["model_policy_denials"] = denials
		encodedRecoveryInput, err := json.Marshal(recoveryInput)
		if err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET input_json = ?, plan_revision_id = ?, run_plan_item_id = ?, updated_at = ? WHERE id = ?`, string(encodedRecoveryInput), planRevisionID, executionPlanItemID, now, recoveryStepID); err != nil {
			return "", "", err
		}
		for _, deniedStepID := range stepIDs {
			if _, err := tx.ExecContext(ctx, `
				UPDATE steps
				SET status = 'canceled', error_code = 'POLICY_REPAIR_QUEUED', updated_at = ?
				WHERE id = ? AND status = 'blocked'`, now, deniedStepID); err != nil {
				return "", "", err
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE tool_calls
				SET status = 'rejected',
				    error_code = CASE WHEN policy_decision = 'deny' THEN 'POLICY_REPAIR_QUEUED' ELSE 'PLAN_SUPERSEDED' END,
				    updated_at = ?
				WHERE step_id = ? AND status = 'blocked'`, now, deniedStepID); err != nil {
				return "", "", err
			}
		}
		if err := s.appendEvent(ctx, q, eventInput{
			OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
			EventType: "model.retry_queued", ActorType: "system", ActorID: modelStep.OwnerID,
			CorrelationID: correlationID, Visibility: run.Visibility,
			Payload: map[string]any{
				"step_id": recoveryStepID, "source_step_id": modelStep.ID,
				"retry_count": retryCount, "max_retries": core.DefaultModelRetryLimit,
				"error_code": denialCode, "policy_denials": denials,
			},
		}); err != nil {
			return "", "", err
		}
		return core.StatusRunning, "", nil
	}
	if hasQueued {
		batchStatus = "running"
		if _, err := tx.ExecContext(ctx, `UPDATE tool_batches SET status = ?, updated_at = ? WHERE id = ?`, batchStatus, now, batchID); err != nil {
			return "", "", err
		}
		return core.StatusQueued, "", nil
	}
	if hasApproval {
		batchStatus = "waiting_approval"
		if _, err := tx.ExecContext(ctx, `UPDATE tool_batches SET status = ?, updated_at = ? WHERE id = ?`, batchStatus, now, batchID); err != nil {
			return "", "", err
		}
		return core.StatusWaitingApproval, "", nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_batches SET status = ?, updated_at = ? WHERE id = ?`, batchStatus, now, batchID); err != nil {
		return "", "", err
	}
	return core.StatusSucceeded, "", nil
}

func isRepairableRunPlanError(errorCode string) bool {
	switch errorCode {
	case "RUN_PLAN_INVALID", "RUN_PLAN_PLACEHOLDER_DENIED", "RUN_PLAN_PREMATURE_COMPLETION", "RUN_PLAN_REQUIRED", "UPDATE_PLAN_MUST_BE_EXCLUSIVE":
		return true
	default:
		return false
	}
}

func activeRunPlanIsProvisional(ctx context.Context, tx *sql.Tx, runID string) (bool, error) {
	var itemCount int
	var title string
	err := tx.QueryRowContext(ctx, `
		SELECT COUNT(i.id), COALESCE(MAX(i.title), '')
		FROM run_plan_revisions p
		LEFT JOIN run_plan_items i ON i.plan_revision_id = p.id
		WHERE p.run_id = ? AND p.status = 'active'
		  AND p.revision = (
			SELECT MAX(revision) FROM run_plan_revisions
			WHERE run_id = ? AND status = 'active'
		  )`, runID, runID).Scan(&itemCount, &title)
	if err != nil {
		return false, err
	}
	if itemCount != 1 {
		return false, nil
	}
	switch strings.TrimSpace(title) {
	case "正在制定执行计划", "正在根据追加要求重新规划", "正在基于已批准权限更新执行计划":
		return true, nil
	default:
		return false, nil
	}
}

func (s *Store) materializeGoalFallbackPlan(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, modelStep core.ClaimedStep, run core.RunContext, now, correlationID string) error {
	var revisionID, itemID string
	if err := tx.QueryRowContext(ctx, `
		SELECT p.id, i.id
		FROM run_plan_revisions p
		JOIN run_plan_items i ON i.plan_revision_id = p.id
		WHERE p.run_id = ? AND p.status = 'active'
		ORDER BY p.revision DESC, i.position
		LIMIT 1`, run.RunID).Scan(&revisionID, &itemID); err != nil {
		return err
	}
	title := truncateText(strings.TrimSpace(run.Goal), 160)
	if title == "" {
		title = truncateText(strings.TrimSpace(run.Title), 160)
	}
	if title == "" {
		title = "完成当前任务目标"
	}
	description := truncateText("按当前目标执行必要动作并保留可验证结果："+strings.TrimSpace(run.Goal), 500)
	if _, err := tx.ExecContext(ctx, `
		UPDATE run_plan_items
		SET title = ?, description = ?, status = 'running',
		    started_at = CASE WHEN started_at = '' THEN ? ELSE started_at END,
		    completed_at = '', result_summary = '', updated_at = ?
		WHERE id = ?`, title, description, now, now, itemID); err != nil {
		return err
	}
	return s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "plan.fallback_materialized", ActorType: "system", ActorID: modelStep.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{
			"plan_revision_id": revisionID, "run_plan_item_id": itemID,
			"title": title, "reason": "model emitted executable tools before update_plan",
		},
	})
}

func (s *Store) queueRunPlanModelRepair(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, modelStep core.ClaimedStep, run core.RunContext, errorCode, now, correlationID string) (string, string, error) {
	recoveryStepID, retryCount, queued, err := s.queueModelInteractionRetry(ctx, tx, modelStep, run, errorCode, now)
	if err != nil {
		return "", "", err
	}
	if !queued {
		return core.StatusBlocked, errorCode, nil
	}
	planRevisionID, planItemID := activeExecutionPlanBinding(ctx, tx, run.RunID)
	if _, err := tx.ExecContext(ctx, `
		UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ?, updated_at = ?
		WHERE id = ?`, planRevisionID, planItemID, now, recoveryStepID); err != nil {
		return "", "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "model.retry_queued", ActorType: "system", ActorID: modelStep.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{
			"step_id": recoveryStepID, "source_step_id": modelStep.ID,
			"retry_count": retryCount, "max_retries": core.DefaultModelRetryLimit,
			"error_code": errorCode,
		},
	}); err != nil {
		return "", "", err
	}
	return core.StatusRunning, "", nil
}

func (s *Store) publishModelRunPlan(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, modelStep core.ClaimedStep, run core.RunContext, call core.ToolCall, now, correlationID string) (string, string, error) {
	rawItems, ok := call.Arguments["items"].([]any)
	if !ok || len(rawItems) < 1 || len(rawItems) > 12 {
		return core.StatusBlocked, "RUN_PLAN_INVALID", nil
	}
	type semanticItem struct {
		Title       string
		Description string
		Status      string
	}
	items := make([]semanticItem, 0, len(rawItems))
	runningCount := 0
	completedCount := 0
	for _, raw := range rawItems {
		value, ok := raw.(map[string]any)
		if !ok {
			return core.StatusBlocked, "RUN_PLAN_INVALID", nil
		}
		title, _ := value["title"].(string)
		description, _ := value["description"].(string)
		status, _ := value["status"].(string)
		title, description = strings.TrimSpace(title), strings.TrimSpace(description)
		status = strings.TrimSpace(status)
		if status == "in_progress" {
			runningCount++
		} else if status == "completed" {
			completedCount++
		}
		if title == "" || description == "" || (status != "pending" && status != "in_progress" && status != "completed") || len([]rune(title)) > 160 || len([]rune(description)) > 500 {
			return core.StatusBlocked, "RUN_PLAN_INVALID", nil
		}
		lower := strings.ToLower(title)
		for _, placeholder := range []string{"理解目标", "完成目标", "生成下一步", "验证并交付", "read_file", "web_search", "读取文件", "执行工具"} {
			if strings.Contains(lower, strings.ToLower(placeholder)) {
				return core.StatusBlocked, "RUN_PLAN_PLACEHOLDER_DENIED", nil
			}
		}
		items = append(items, semanticItem{Title: title, Description: description, Status: status})
	}
	if runningCount > 1 {
		return core.StatusBlocked, "RUN_PLAN_INVALID", nil
	}
	stage := modelStageFromInput(modelStep.InputJSON)
	if stage == "plan" && (runningCount != 1 || completedCount != 0) {
		return core.StatusBlocked, "RUN_PLAN_PREMATURE_COMPLETION", nil
	}
	var previousID string
	var previousRevision int64
	err := tx.QueryRowContext(ctx, `SELECT id, revision FROM run_plan_revisions WHERE run_id = ? AND status = 'active' ORDER BY revision DESC LIMIT 1`, run.RunID).Scan(&previousID, &previousRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	supersedesRevisionID := previousID
	if errors.Is(err, sql.ErrNoRows) {
		// A completed/blocked historical plan is no longer active, but its
		// revision number still belongs to this Run. Starting again at revision
		// 1 violates the unique (run_id, revision) contract and leaves the model
		// step running until lease recovery. Allocate from the latest durable
		// revision while keeping same-plan updates limited to the active plan.
		latestErr := tx.QueryRowContext(ctx, `
			SELECT id, revision FROM run_plan_revisions
			WHERE run_id = ? ORDER BY revision DESC LIMIT 1`,
			run.RunID).Scan(&supersedesRevisionID, &previousRevision)
		if latestErr != nil && !errors.Is(latestErr, sql.ErrNoRows) {
			return "", "", latestErr
		}
	}
	samePlan := previousID != ""
	existingItemIDs := make([]string, 0, len(items))
	existingTitles := make([]string, 0, len(items))
	existingDescriptions := make([]string, 0, len(items))
	if samePlan {
		rows, queryErr := tx.QueryContext(ctx, `SELECT id, title, description, status FROM run_plan_items WHERE plan_revision_id = ? ORDER BY position`, previousID)
		if queryErr != nil {
			return "", "", queryErr
		}
		for rows.Next() {
			var itemID, title, description, status string
			if err := rows.Scan(&itemID, &title, &description, &status); err != nil {
				_ = rows.Close()
				return "", "", err
			}
			existingItemIDs = append(existingItemIDs, itemID)
			existingTitles = append(existingTitles, title)
			existingDescriptions = append(existingDescriptions, description)
		}
		if err := rows.Close(); err != nil {
			return "", "", err
		}
		provisionalPlan := len(existingTitles) == 1 && (stage == "plan" && existingTitles[0] == "正在制定执行计划" ||
			stage == "steering_replan" && existingTitles[0] == "正在根据追加要求重新规划" ||
			stage == "permission_replan" && existingTitles[0] == "正在基于已批准权限更新执行计划")
		if provisionalPlan {
			firstItemID := ""
			for index, item := range items {
				itemID := existingItemIDs[0]
				if index > 0 {
					var idErr error
					itemID, idErr = newID()
					if idErr != nil {
						return "", "", idErr
					}
				}
				dbStatus := map[string]string{"pending": core.RunPlanItemPending, "in_progress": core.RunPlanItemRunning, "completed": core.RunPlanItemCompleted}[item.Status]
				startedAt, completedAt := "", ""
				if dbStatus == core.RunPlanItemRunning {
					firstItemID, startedAt = itemID, now
				}
				if dbStatus == core.RunPlanItemCompleted {
					startedAt, completedAt = now, now
				}
				if index == 0 {
					if _, err := tx.ExecContext(ctx, `
						UPDATE run_plan_items
						SET position = 1, title = ?, description = ?, status = ?,
						    started_at = ?, completed_at = ?, result_summary = '', updated_at = ?
						WHERE id = ?`,
						item.Title, item.Description, dbStatus, startedAt, completedAt, now, itemID); err != nil {
						return "", "", err
					}
					continue
				}
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO run_plan_items (
						id, organization_id, project_id, task_id, run_id, plan_revision_id,
						position, title, description, status, started_at, completed_at, created_at, updated_at
					) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					itemID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, previousID,
					index+1, item.Title, item.Description, dbStatus, startedAt, completedAt, now, now,
				); err != nil {
					return "", "", err
				}
			}
			history, historyErr := s.collectContinuationHistory(ctx, tx, run)
			if historyErr != nil {
				return "", "", historyErr
			}
			nextStepID, nextErr := insertQueuedModelStep(ctx, tx, run, map[string]any{
				"history": history, "stage": "plan_execute", "source_step_id": modelStep.ID,
				"plan_revision_id": previousID,
			}, now)
			if nextErr != nil {
				return "", "", nextErr
			}
			if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, previousID, firstItemID, nextStepID); err != nil {
				return "", "", err
			}
			explanation, _ := call.Arguments["explanation"].(string)
			if err := s.appendEvent(ctx, q, eventInput{
				OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
				EventType: "plan.state_changed", ActorType: "agent", ActorID: modelStep.OwnerID,
				CorrelationID: correlationID, Visibility: run.Visibility,
				Payload: map[string]any{
					"plan_revision_id": previousID, "source": "model", "initial_plan_published": true,
					"item_count": len(items), "explanation": truncateText(strings.TrimSpace(explanation), 1000),
				},
			}); err != nil {
				return "", "", err
			}
			return core.StatusRunning, "", nil
		}
		if len(existingTitles) != len(items) {
			samePlan = false
		} else {
			for index := range items {
				// Status updates address stable semantic items by position. Model
				// paraphrasing must not create a new plan revision mid-run.
				items[index].Title = existingTitles[index]
			}
		}
	}
	if previousID != "" && !samePlan && !modelStageAllowsPlanReplacement(stage) {
		// Mid-run plan structure is immutable. Providers occasionally add,
		// remove, or paraphrase delivery steps while reporting progress. Keep
		// the durable list stable and project the submitted ordinal progress
		// back onto it; a genuine structure change requires Steering.
		completedCount := 0
		hasRunning := false
		for _, item := range items {
			if item.Status == "completed" {
				completedCount++
			} else if item.Status == "in_progress" {
				hasRunning = true
			}
		}
		projected := make([]semanticItem, 0, len(existingTitles))
		for index := range existingTitles {
			status := "pending"
			if index < completedCount {
				status = "completed"
			} else if index == completedCount && hasRunning {
				status = "in_progress"
			}
			projected = append(projected, semanticItem{
				Title:       existingTitles[index],
				Description: existingDescriptions[index],
				Status:      status,
			})
		}
		items = projected
		samePlan = true
		if err := s.appendEvent(ctx, q, eventInput{
			OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
			EventType: "plan.structure_change_ignored", ActorType: "system", ActorID: modelStep.OwnerID,
			CorrelationID: correlationID, Visibility: run.Visibility,
			Payload: map[string]any{
				"plan_revision_id": previousID,
				"submitted_count":  len(rawItems),
				"stable_count":     len(existingTitles),
				"reason":           "runtime progress updates cannot replace the semantic plan without Steering",
			},
		}); err != nil {
			return "", "", err
		}
	}
	if samePlan {
		firstItemID := ""
		changed := false
		currentItemIDs := make([]string, len(items))
		currentStatuses := make([]string, len(items))
		for index, item := range items {
			dbStatus := map[string]string{"pending": core.RunPlanItemPending, "in_progress": core.RunPlanItemRunning, "completed": core.RunPlanItemCompleted}[item.Status]
			if err := tx.QueryRowContext(ctx, `SELECT id, status FROM run_plan_items WHERE plan_revision_id = ? AND position = ?`, previousID, index+1).Scan(&currentItemIDs[index], &currentStatuses[index]); err != nil {
				return "", "", err
			}
			if currentStatuses[index] == core.RunPlanItemCompleted && dbStatus != core.RunPlanItemCompleted {
				recoveryStepID, retryCount, queued, retryErr := s.queueModelInteractionRetry(ctx, tx, modelStep, run, "RUN_PLAN_STATUS_REGRESSION", now)
				if retryErr != nil {
					return "", "", retryErr
				}
				if !queued {
					if err := s.appendEvent(ctx, q, eventInput{
						OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
						EventType: "plan.status_regression_exhausted", ActorType: "system", ActorID: modelStep.OwnerID,
						CorrelationID: correlationID, Visibility: run.Visibility,
						Payload: map[string]any{"plan_revision_id": previousID, "position": index + 1, "retry_count": retryCount, "max_retries": core.DefaultModelRetryLimit},
					}); err != nil {
						return "", "", err
					}
					return core.StatusBlocked, "RUN_PLAN_STATUS_REGRESSION", nil
				}
				var activeItemID string
				_ = tx.QueryRowContext(ctx, `
					SELECT id FROM run_plan_items
					WHERE plan_revision_id = ? AND status IN ('running', 'blocked', 'pending')
					ORDER BY position LIMIT 1`, previousID).Scan(&activeItemID)
				if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, previousID, activeItemID, recoveryStepID); err != nil {
					return "", "", err
				}
				if err := s.appendEvent(ctx, q, eventInput{
					OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
					EventType: "plan.status_regression_ignored", ActorType: "system", ActorID: modelStep.OwnerID,
					CorrelationID: correlationID, Visibility: run.Visibility,
					Payload: map[string]any{
						"plan_revision_id": previousID, "position": index + 1,
						"preserved_status": currentStatuses[index], "submitted_status": dbStatus,
						"recovery_step_id": recoveryStepID, "retry_count": retryCount, "max_retries": core.DefaultModelRetryLimit,
					},
				}); err != nil {
					return "", "", err
				}
				if err := s.appendEvent(ctx, q, eventInput{
					OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
					EventType: "model.retry_queued", ActorType: "system", ActorID: modelStep.OwnerID,
					CorrelationID: correlationID, Visibility: run.Visibility,
					Payload: map[string]any{
						"step_id": recoveryStepID, "source_step_id": modelStep.ID,
						"retry_count": retryCount, "max_retries": core.DefaultModelRetryLimit,
						"error_code": "RUN_PLAN_STATUS_REGRESSION",
					},
				}); err != nil {
					return "", "", err
				}
				return core.StatusRunning, "", nil
			}
			if currentStatuses[index] != dbStatus {
				changed = true
			}
			if firstItemID == "" && dbStatus == core.RunPlanItemRunning {
				firstItemID = currentItemIDs[index]
			}
		}
		for index, item := range items {
			dbStatus := map[string]string{"pending": core.RunPlanItemPending, "in_progress": core.RunPlanItemRunning, "completed": core.RunPlanItemCompleted}[item.Status]
			if _, err := tx.ExecContext(ctx, `
				UPDATE run_plan_items SET description = ?, status = ?,
					started_at = CASE WHEN ? = 'running' AND started_at = '' THEN ? ELSE started_at END,
					completed_at = CASE WHEN ? = 'completed' AND completed_at = '' THEN ? WHEN ? <> 'completed' THEN '' ELSE completed_at END,
					updated_at = ? WHERE id = ?`,
				item.Description, dbStatus, dbStatus, now, dbStatus, now, dbStatus, now, currentItemIDs[index]); err != nil {
				return "", "", err
			}
		}
		if !changed {
			history, err := s.collectContinuationHistory(ctx, tx, run)
			if err != nil {
				return "", "", err
			}
			if firstItemID == "" {
				_ = tx.QueryRowContext(ctx, `
					SELECT id FROM run_plan_items
					WHERE plan_revision_id = ? AND status IN ('pending', 'running')
					ORDER BY position LIMIT 1`, previousID).Scan(&firstItemID)
			}
			nextStepID, err := insertQueuedModelStep(ctx, tx, run, map[string]any{
				"history": history, "stage": "plan_execute", "source_step_id": modelStep.ID,
				"plan_revision_id": previousID, "plan_no_progress_recovered": true,
			}, now)
			if err != nil {
				return "", "", err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, previousID, firstItemID, nextStepID); err != nil {
				return "", "", err
			}
			if err := s.appendEvent(ctx, q, eventInput{
				OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
				EventType: "plan.no_progress_ignored", ActorType: "system", ActorID: modelStep.OwnerID,
				CorrelationID: correlationID, Visibility: run.Visibility,
				Payload: map[string]any{
					"plan_revision_id": previousID,
					"source_step_id":   modelStep.ID,
					"recovery_step_id": nextStepID,
					"reason":           "unchanged plan update cannot replace execution",
				},
			}); err != nil {
				return "", "", err
			}
			return core.StatusRunning, "", nil
		}
		history, err := s.collectContinuationHistory(ctx, tx, run)
		if err != nil {
			return "", "", err
		}
		nextStepID, err := insertQueuedModelStep(ctx, tx, run, map[string]any{"history": history, "stage": "plan_execute", "source_step_id": modelStep.ID, "plan_revision_id": previousID}, now)
		if err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, previousID, firstItemID, nextStepID); err != nil {
			return "", "", err
		}
		if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "plan.state_changed", ActorType: "agent", ActorID: modelStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"plan_revision_id": previousID, "source": "model"}}); err != nil {
			return "", "", err
		}
		return core.StatusRunning, "", nil
	}
	if previousID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'superseded', superseded_at = ? WHERE id = ?`, now, previousID); err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'canceled', completed_at = ?, updated_at = ? WHERE plan_revision_id = ? AND status NOT IN ('completed', 'canceled')`, now, now, previousID); err != nil {
			return "", "", err
		}
	}
	revisionID, err := newID()
	if err != nil {
		return "", "", err
	}
	trigger := "initial"
	if stage == "steering_replan" {
		trigger = "steering"
	} else if stage == "permission_replan" {
		trigger = "permission_change"
	}
	explanation, _ := call.Arguments["explanation"].(string)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_plan_revisions (
			id, organization_id, project_id, task_id, run_id, revision, trigger,
			goal_summary, status, supersedes_revision_id, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
		revisionID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID,
		previousRevision+1, trigger, truncateText(run.Goal, 1000), supersedesRevisionID, modelStep.OwnerID, now,
	); err != nil {
		return "", "", err
	}
	firstItemID := ""
	for index, item := range items {
		itemID, idErr := newID()
		if idErr != nil {
			return "", "", idErr
		}
		status := map[string]string{"pending": core.RunPlanItemPending, "in_progress": core.RunPlanItemRunning, "completed": core.RunPlanItemCompleted}[item.Status]
		startedAt, completedAt := "", ""
		if status == core.RunPlanItemRunning {
			firstItemID, startedAt = itemID, now
		}
		if status == core.RunPlanItemCompleted {
			startedAt, completedAt = now, now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO run_plan_items (
				id, organization_id, project_id, task_id, run_id, plan_revision_id,
				position, title, description, status, started_at, completed_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, revisionID,
			index+1, item.Title, item.Description, status, startedAt, completedAt, now, now,
		); err != nil {
			return "", "", err
		}
	}
	history, err := s.collectContinuationHistory(ctx, tx, run)
	if err != nil {
		return "", "", err
	}
	nextStepID, err := insertQueuedModelStep(ctx, tx, run, map[string]any{
		"history": history, "stage": "plan_execute", "source_step_id": modelStep.ID,
		"plan_revision_id": revisionID,
	}, now)
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, revisionID, firstItemID, nextStepID); err != nil {
		return "", "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "plan.published", ActorType: "agent", ActorID: modelStep.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"plan_revision_id": revisionID, "trigger": trigger, "item_count": len(items), "explanation": truncateText(strings.TrimSpace(explanation), 1000)},
	}); err != nil {
		return "", "", err
	}
	return core.StatusRunning, "", nil
}

func modelStageFromInput(inputJSON string) string {
	var input struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal([]byte(inputJSON), &input)
	return strings.TrimSpace(input.Stage)
}

func modelStageAllowsPlanReplacement(stage string) bool {
	return stage == "plan" || stage == "steering_replan" || stage == "permission_replan"
}

func lengthTruncatedToolCalls(output map[string]any) []core.ToolCall {
	raw, ok := output["length_truncated_tool_calls"]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var calls []core.ToolCall
	if json.Unmarshal(encoded, &calls) != nil {
		return nil
	}
	return calls
}

func permissionModeFromSnapshot(snapshot string) string {
	var config map[string]any
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return core.PermissionModeAsk
	}
	mode, _ := config["permission_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == core.PermissionModeDefault {
		return core.PermissionModeAsk
	}
	return mode
}

func mcpPolicyFromSnapshot(snapshot, fullToolName string) (string, bool, string) {
	var config struct {
		Capabilities []struct {
			ID    string `json:"id"`
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnlyHint    *bool `json:"readOnlyHint"`
					DestructiveHint *bool `json:"destructiveHint"`
					IdempotentHint  *bool `json:"idempotentHint"`
					OpenWorldHint   *bool `json:"openWorldHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"mcp_capabilities"`
	}
	if json.Unmarshal([]byte(snapshot), &config) != nil {
		return "L3", false, "MCP tool metadata is missing; high-risk manual approval is required"
	}
	for _, capability := range config.Capabilities {
		for _, tool := range capability.Tools {
			if "mcp__"+capability.ID+"__"+tool.Name != fullToolName {
				continue
			}
			annotations := tool.Annotations
			idempotent := annotations.IdempotentHint != nil && *annotations.IdempotentHint
			if annotations.ReadOnlyHint == nil || annotations.DestructiveHint == nil || annotations.IdempotentHint == nil || annotations.OpenWorldHint == nil {
				return "L3", false, "MCP annotations are incomplete; high-risk manual approval is required"
			}
			if *annotations.DestructiveHint || *annotations.OpenWorldHint {
				return "L3", idempotent, "Destructive or open-world MCP execution always requires one-time manual approval"
			}
			return "L2", idempotent, "Bounded MCP execution requires one-time approval and a durable side-effect receipt"
		}
	}
	return "L3", false, "MCP tool is absent from the frozen capability snapshot; high-risk manual approval is required"
}

func (s *Store) recordLengthTruncatedToolCalls(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, modelStep core.ClaimedStep, run core.RunContext, calls []core.ToolCall, now, correlationID string) error {
	var maxPosition int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) FROM steps WHERE run_id = ?`, run.RunID).Scan(&maxPosition); err != nil {
		return err
	}
	for index, call := range calls {
		toolCallID, err := newID()
		if err != nil {
			return err
		}
		stepID, err := newID()
		if err != nil {
			return err
		}
		position := maxPosition + int64(index) + 1
		argumentsJSON := mustToolArgumentsJSON(call.Arguments)
		sum := sha256.Sum256([]byte(argumentsJSON))
		inputHash := hex.EncodeToString(sum[:])
		stepInput, err := json.Marshal(map[string]any{
			"tool_call_id":          toolCallID,
			"provider_tool_call_id": call.ID,
			"name":                  call.Name,
			"arguments":             call.Arguments,
			"model_source_step_id":  modelStep.ID,
			"not_executed":          true,
		})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, output_json, error_code, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'blocked', ?, ?, 'MODEL_OUTPUT_TRUNCATED', ?, ?)`,
			stepID, run.OrganizationID, run.ProjectID, run.RunID, call.Name, position, string(stepInput),
			`{"status":"blocked","error_code":"MODEL_OUTPUT_TRUNCATED","executed":false}`, now, now,
		); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tool_calls (id, organization_id, project_id, task_id, run_id, step_id, position, tool_name, arguments_json, arguments_hash, risk_level, policy_decision, status, error_code, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'L0', 'deny', 'blocked', 'MODEL_OUTPUT_TRUNCATED', ?, ?)`,
			toolCallID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, stepID, position, call.Name, argumentsJSON, inputHash, now, now,
		); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, q, eventInput{
			OrganizationID: run.OrganizationID,
			ProjectID:      run.ProjectID,
			RunID:          run.RunID,
			EventType:      "tool.skipped",
			ActorType:      "system",
			ActorID:        modelStep.OwnerID,
			CorrelationID:  correlationID,
			Visibility:     run.Visibility,
			Payload: map[string]any{
				"tool_call_id":          toolCallID,
				"provider_tool_call_id": call.ID,
				"step_id":               stepID,
				"tool_name":             call.Name,
				"position":              position,
				"status":                core.StatusBlocked,
				"error_code":            "MODEL_OUTPUT_TRUNCATED",
				"executed":              false,
				"message":               "模型输出因长度限制被截断，工具调用未执行。",
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

// approvalRuleSignature is intentionally narrower than the legacy executable-
// only key. The exact normalized argument structure is part of the signature,
// so changing a target, flag, path or precondition requires a new approval.
// Destructive Git and MCP calls are never persistently authorized.
func approvalRuleSignature(toolName, argumentsJSON string) (string, string, bool) {
	if strings.HasPrefix(toolName, "mcp__") || toolName == core.ToolGitRestore || toolName == core.ToolGitRestoreHunk {
		return "", "", false
	}
	switch toolName {
	case core.ToolWorkspaceRunCommand, core.ToolGitBranch, core.ToolGitCommit, core.ToolGitCommitPaths:
	default:
		return "", "", false
	}
	var arguments map[string]any
	if json.Unmarshal([]byte(argumentsJSON), &arguments) != nil {
		return "", "", false
	}
	if toolName == core.ToolWorkspaceRunCommand {
		program, _ := arguments["program"].(string)
		program = strings.ToLower(strings.TrimSpace(program))
		if program == "" {
			return "", "", false
		}
		arguments["program"] = program
	}
	normalized, err := json.Marshal(map[string]any{"tool_name": toolName, "arguments": arguments, "rule_version": 2})
	if err != nil {
		return "", "", false
	}
	sum := sha256.Sum256(normalized)
	return "sha256:" + hex.EncodeToString(sum[:]), string(normalized), true
}

func conversationApprovalRuleKey(toolName, argumentsJSON string) (string, bool) {
	key, _, ok := approvalRuleSignature(toolName, argumentsJSON)
	return key, ok
}

func activeExecutionPlanBinding(ctx context.Context, tx *sql.Tx, runID string) (string, string) {
	var revisionID, itemID string
	_ = tx.QueryRowContext(ctx, `
		SELECT p.id, i.id
		FROM run_plan_revisions p
		JOIN run_plan_items i ON i.plan_revision_id = p.id
		WHERE p.run_id = ? AND p.status = 'active'
		ORDER BY p.revision DESC,
		         CASE i.status WHEN 'running' THEN 0 WHEN 'waiting_user' THEN 1 WHEN 'pending' THEN 2 ELSE 3 END,
		         i.position
		LIMIT 1`, runID).Scan(&revisionID, &itemID)
	return revisionID, itemID
}

func approvalRuleMatches(ctx context.Context, tx *sql.Tx, run core.RunContext, call core.ToolCall, riskLevel, now string) (bool, error) {
	ruleKey, _, ok := approvalRuleSignature(call.Name, mustToolArgumentsJSON(call.Arguments))
	if !ok || strings.TrimSpace(run.ConversationID) == "" {
		return false, nil
	}
	var marker int
	err := tx.QueryRowContext(ctx, `
		SELECT 1
		FROM conversation_approval_rules
		WHERE organization_id = ? AND project_id = ? AND conversation_id = ?
		  AND tool_name = ? AND rule_version = 2 AND match_hash = ? AND risk_level = ? AND expires_at > ?
		LIMIT 1`, run.OrganizationID, run.ProjectID, run.ConversationID, call.Name, ruleKey, riskLevel, now).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `
			SELECT 1
			FROM project_approval_rules
			WHERE organization_id = ? AND project_id = ?
			  AND tool_name = ? AND rule_version = 2 AND match_hash = ? AND risk_level = ? AND expires_at > ? AND revoked_at = ''
			LIMIT 1`, run.OrganizationID, run.ProjectID, call.Name, ruleKey, riskLevel, now).Scan(&marker)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
	}
	return err == nil && marker == 1, err
}

func (s *Store) ListProjectApprovalRules(ctx context.Context, scope core.Scope, projectID string) ([]core.ApprovalRule, error) {
	membership, err := s.queries.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return nil, err
	}
	if membership.Role != core.ProjectRoleOwner {
		return nil, &core.ForbiddenError{Message: "only a project owner can manage approval rules"}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.organization_id, r.project_id, r.tool_name, r.rule_key,
		       r.created_from_approval_id, r.created_by, r.created_at, r.expires_at,
		       r.revoked_at, r.revoked_by
		FROM project_approval_rules r
		JOIN project_memberships pm ON pm.project_id = r.project_id
		  AND pm.user_id = ? AND pm.status = 'active'
		WHERE r.organization_id = ? AND r.project_id = ?
		ORDER BY r.created_at DESC, r.id DESC`, scope.UserID, scope.OrganizationID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]core.ApprovalRule, 0)
	for rows.Next() {
		var rule core.ApprovalRule
		var createdAt, expiresAt, revokedAt string
		if err := rows.Scan(&rule.ID, &rule.OrganizationID, &rule.ProjectID, &rule.ToolName, &rule.RuleKey,
			&rule.CreatedFromApprovalID, &rule.CreatedBy, &createdAt, &expiresAt, &revokedAt, &rule.RevokedBy); err != nil {
			return nil, err
		}
		rule.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		rule.ExpiresAt, err = parseTime(expiresAt)
		if err != nil {
			return nil, err
		}
		if revokedAt != "" {
			value, parseErr := parseTime(revokedAt)
			if parseErr != nil {
				return nil, parseErr
			}
			rule.RevokedAt = &value
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (s *Store) RevokeProjectApprovalRule(ctx context.Context, scope core.Scope, projectID, ruleID, correlationID string) error {
	membership, err := s.queries.GetProjectMembershipForUser(ctx, sqlitegen.GetProjectMembershipForUserParams{ProjectID: projectID, UserID: scope.UserID, OrganizationID: scope.OrganizationID})
	if err != nil {
		return err
	}
	if membership.Role != core.ProjectRoleOwner {
		return &core.ForbiddenError{Message: "only a project owner can manage approval rules"}
	}
	now := formatTime(time.Now().UTC())
	result, err := s.db.ExecContext(ctx, `
		UPDATE project_approval_rules
		SET revoked_at = ?, revoked_by = ?
		WHERE id = ? AND organization_id = ? AND project_id = ? AND revoked_at = ''
		  AND EXISTS (
		    SELECT 1 FROM project_memberships pm
		    WHERE pm.project_id = project_approval_rules.project_id
		      AND pm.user_id = ? AND pm.status = 'active'
		  )`, now, scope.UserID, ruleID, scope.OrganizationID, projectID, scope.UserID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func mustToolArgumentsJSON(arguments map[string]any) string {
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func stepSkillInvocation(inputJSON string) ([]string, string) {
	var input struct {
		InvokedSkillIDs []string `json:"invoked_skill_ids"`
		SkillArgs       string   `json:"skill_args"`
	}
	if json.Unmarshal([]byte(inputJSON), &input) != nil {
		return nil, ""
	}
	return input.InvokedSkillIDs, input.SkillArgs
}

func policyErrorCode(decision string) string {
	if decision == core.PolicyDeny {
		return "TOOL_POLICY_DENIED"
	}
	return ""
}

func (s *Store) finishToolExecution(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, execution core.ToolExecution, now, correlationID string) error {
	receiptStatus := "failed"
	toolStatus := "failed"
	validationStatus := "failed"
	if execution.Status == core.StatusSucceeded {
		receiptStatus = "succeeded"
		toolStatus = "succeeded"
		validationStatus = "passed"
	}
	if step.StepType == core.ToolWorkspaceRunCommand && execution.Output != nil {
		if content, _ := execution.Output["artifact_content"].(string); content != "" {
			delete(execution.Output, "artifact_content")
			artifact, persistErr := s.persistCommandOutputArtifact(ctx, tx, q, step, run, []byte(content), now, correlationID)
			if persistErr != nil {
				return persistErr
			}
			execution.Output["artifact_id"] = artifact.ID
			execution.Output["artifact_name"] = artifact.Name
			execution.Output["artifact_size_bytes"] = artifact.SizeBytes
		}
	}
	outputJSON, err := json.Marshal(execution.Output)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE side_effect_receipts
		SET status = ?, exit_code = ?, output_json = ?, error_code = ?, finished_at = ?
		WHERE tool_call_id = ? AND invocation_id = ? AND status = 'started'`,
		receiptStatus, execution.ExitCode, string(outputJSON), execution.ErrorCode, now, execution.ToolCallID, execution.InvocationID,
	)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("RECEIPT_LOST: tool call %s invocation %s", execution.ToolCallID, execution.InvocationID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = ?, error_code = ?, completed_at = ?, updated_at = ? WHERE id = ? AND step_id = ?`, toolStatus, execution.ErrorCode, now, now, execution.ToolCallID, step.ID); err != nil {
		return err
	}
	validationID, err := newID()
	if err != nil {
		return err
	}
	validator := "command_exit_code"
	if step.StepType == core.ToolWorkspaceWriteFile {
		validator = "workspace_scope_and_content_hash"
	} else if step.StepType == core.ToolWorkspaceEditFile {
		validator = "workspace_scope_preimage_hash_and_atomic_replace"
	} else if step.StepType == core.ToolWorkspaceReadFile || step.StepType == core.ToolWorkspaceListFiles ||
		step.StepType == core.ToolWorkspaceFindFiles || step.StepType == core.ToolWorkspaceGrep {
		validator = "workspace_scope_pagination_binary_and_redaction"
	} else if step.StepType == core.ToolGitStatus || step.StepType == core.ToolGitDiff || step.StepType == core.ToolGitLog {
		validator = "git_read_only_scope_and_output_bound"
	} else if step.StepType == core.ToolGitBranch || step.StepType == core.ToolGitCommit || step.StepType == core.ToolGitRestore {
		validator = "git_precondition_hash_policy_and_receipt"
	} else if step.StepType == core.ToolWebSearch {
		validator = "web_search_result_schema_and_receipt"
	} else if step.StepType == core.ToolSkillInvoke {
		validator = "skill_snapshot_and_tool_scope"
	} else if strings.HasPrefix(step.StepType, "mcp__") {
		validator = "mcp_response_size_and_receipt"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO validation_results (id, organization_id, project_id, task_id, run_id, step_id, tool_call_id, validator, status, input_hash, details_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		validationID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, step.ID, execution.ToolCallID, validator, validationStatus, execution.InputHash, string(outputJSON), now,
	); err != nil {
		return err
	}
	var toolName, argumentsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT tool_name, arguments_json FROM tool_calls WHERE id = ? AND run_id = ?`, execution.ToolCallID, run.RunID).Scan(&toolName, &argumentsJSON); err != nil {
		return err
	}
	arguments := map[string]any{}
	_ = json.Unmarshal([]byte(argumentsJSON), &arguments)
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.completed", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"tool_call_id": execution.ToolCallID, "step_id": step.ID, "tool_name": toolName, "input_summary": toolInputSummary(toolName, arguments), "output_summary": toolOutputSummary(toolName, receiptStatus, execution.ErrorCode, int64(execution.ExitCode), execution.Output), "output_excerpt": toolOutputExcerpt(toolName, execution.Output), "invocation_id": execution.InvocationID, "status": receiptStatus, "error_code": execution.ErrorCode, "exit_code": execution.ExitCode, "input_hash": execution.InputHash}}); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "validation.completed", ActorType: "validator", ActorID: validator, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"validation_id": validationID, "tool_call_id": execution.ToolCallID, "validator": validator, "status": validationStatus, "input_hash": execution.InputHash}}); err != nil {
		return err
	}
	if step.StepType == core.ToolWorkspaceWriteFile && validationStatus == "passed" {
		return s.persistWorkspaceFileArtifact(ctx, tx, q, step, run, execution, now, correlationID)
	}
	return nil
}

func (s *Store) persistCommandOutputArtifact(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, content []byte, now, correlationID string) (core.Artifact, error) {
	persisted, err := s.persistBlob(ctx, content, "text/plain; charset=utf-8")
	if err != nil {
		return core.Artifact{}, fmt.Errorf("persist command output artifact: %w", err)
	}
	if err := ensureBlob(ctx, tx, persisted, now); err != nil {
		return core.Artifact{}, err
	}
	artifactID, err := newID()
	if err != nil {
		return core.Artifact{}, err
	}
	name := "command-output-" + step.ID + ".log"
	row, err := q.CreateArtifact(ctx, sqlitegen.CreateArtifactParams{
		ID: artifactID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID,
		TaskID: run.TaskID, RunID: run.RunID, StepID: step.ID, Name: name,
		Kind: "evidence", MediaType: persisted.MediaType, ContentHash: persisted.ContentHash,
		SizeBytes: persisted.SizeBytes, Visibility: run.Visibility, SourceType: "command_output",
		CreatedAt: now,
	})
	if err != nil {
		return core.Artifact{}, err
	}
	artifact, err := artifactFromRow(row)
	if err != nil {
		return core.Artifact{}, err
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID,
		EventType: "artifact.created", ActorType: "worker", ActorID: step.OwnerID,
		CorrelationID: correlationID, Visibility: run.Visibility,
		Payload: map[string]any{"artifact_id": row.ID, "step_id": step.ID, "name": row.Name, "kind": row.Kind, "source_type": row.SourceType, "content_hash": row.ContentHash, "size_bytes": row.SizeBytes, "media_type": row.MediaType},
	}); err != nil {
		return core.Artifact{}, err
	}
	return artifact, nil
}

func (s *Store) persistWorkspaceFileArtifact(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, execution core.ToolExecution, now, correlationID string) error {
	var argumentsJSON string
	if err := tx.QueryRowContext(ctx, `SELECT arguments_json FROM tool_calls WHERE id = ? AND run_id = ?`, execution.ToolCallID, run.RunID).Scan(&argumentsJSON); err != nil {
		return err
	}
	var arguments struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if json.Unmarshal([]byte(argumentsJSON), &arguments) != nil || strings.TrimSpace(arguments.Path) == "" {
		return fmt.Errorf("WORKSPACE_ARTIFACT_INPUT_INVALID: verified file call has no path")
	}
	path := filepath.ToSlash(filepath.Clean(arguments.Path))
	if path == "." || strings.HasPrefix(path, ".oktapod/") {
		return nil
	}
	persisted, err := s.persistBlob(ctx, []byte(arguments.Content), workspaceArtifactMediaType(path))
	if err != nil {
		return fmt.Errorf("persist workspace artifact: %w", err)
	}
	if outputHash, _ := execution.Output["content_hash"].(string); outputHash == "" || outputHash != persisted.ContentHash {
		return fmt.Errorf("WORKSPACE_ARTIFACT_HASH_MISMATCH: receipt and persisted content differ")
	}
	if err := ensureBlob(ctx, tx, persisted, now); err != nil {
		return err
	}
	artifactID, err := newID()
	if err != nil {
		return err
	}
	kind := "deliverable"
	if strings.HasPrefix(path, ".research/") {
		kind = "evidence"
	}
	row, err := q.CreateArtifact(ctx, sqlitegen.CreateArtifactParams{ID: artifactID, OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, TaskID: run.TaskID, RunID: run.RunID, StepID: step.ID, Name: path, Kind: kind, MediaType: persisted.MediaType, ContentHash: persisted.ContentHash, SizeBytes: persisted.SizeBytes, Visibility: run.Visibility, SourceType: "workspace_file", CreatedAt: now})
	if err != nil {
		return err
	}
	return s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "artifact.created", ActorType: "worker", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"artifact_id": row.ID, "step_id": step.ID, "name": row.Name, "path": path, "kind": row.Kind, "source_type": row.SourceType, "content_hash": row.ContentHash, "size_bytes": row.SizeBytes, "media_type": row.MediaType}})
}

func workspaceArtifactMediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".txt", ".log":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func deriveRunStatus(ctx context.Context, tx *sql.Tx, runID string) (string, string, error) {
	var queued, waiting, blocked, failed int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN status IN ('planned', 'queued', 'running') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'waiting_approval' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'blocked' OR (status = 'rejected' AND error_code <> 'PLAN_SUPERSEDED') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'failed' AND NOT EXISTS (
		           SELECT 1
		           FROM run_events recovery
		           LEFT JOIN tool_calls recovered_call
		             ON recovered_call.id = json_extract(recovery.payload_json, '$.tool_call_id')
		           WHERE recovery.run_id = tool_calls.run_id
		             AND recovery.event_type = 'tool.recovery_queued'
		             AND (
		                 json_extract(recovery.payload_json, '$.tool_call_id') = tool_calls.id
		                 OR (
		                     tool_calls.tool_batch_id <> ''
		                     AND recovered_call.tool_batch_id = tool_calls.tool_batch_id
		                 )
		             )
		       ) THEN 1 ELSE 0 END), 0)
		FROM tool_calls WHERE run_id = ?`, runID,
	).Scan(&queued, &waiting, &blocked, &failed); err != nil {
		return "", "", err
	}
	if failed > 0 {
		return core.StatusFailed, "TOOL_EXECUTION_FAILED", nil
	}
	if blocked > 0 {
		return core.StatusBlocked, "TOOL_POLICY_DENIED", nil
	}
	if queued > 0 {
		return core.StatusRunning, "", nil
	}
	if waiting > 0 {
		return core.StatusWaitingApproval, "", nil
	}
	return core.StatusSucceeded, "", nil
}

func (s *Store) completeToolBatchState(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, now, correlationID string) (ready, continuationClaimed bool, batchID string, err error) {
	if err = tx.QueryRowContext(ctx, `SELECT tool_batch_id FROM steps WHERE id = ?`, step.ID).Scan(&batchID); err != nil {
		return false, false, "", err
	}
	if batchID == "" {
		return true, true, "", nil
	}
	var total, succeeded, failed, blocked, active int64
	if err = tx.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status IN ('blocked', 'rejected') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status IN ('planned', 'queued', 'running', 'waiting_approval') THEN 1 ELSE 0 END), 0)
		FROM tool_calls WHERE tool_batch_id = ?`, batchID,
	).Scan(&total, &succeeded, &failed, &blocked, &active); err != nil {
		return false, false, batchID, err
	}
	if total == 0 {
		return false, false, batchID, fmt.Errorf("TOOL_BATCH_EMPTY: batch %s has no tool calls", batchID)
	}
	if active > 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE tool_batches SET status = 'running', updated_at = ? WHERE id = ? AND status <> 'waiting_approval'`, now, batchID); err != nil {
			return false, false, batchID, err
		}
		return false, false, batchID, nil
	}
	targetStatus := "succeeded"
	if failed > 0 {
		targetStatus = "failed"
	} else if blocked > 0 {
		targetStatus = "blocked"
	} else if succeeded != total {
		return false, false, batchID, fmt.Errorf("TOOL_BATCH_STATE_INVALID: batch %s has %d/%d classified terminal calls", batchID, succeeded+failed+blocked, total)
	}
	if targetStatus != "succeeded" {
		result, updateErr := tx.ExecContext(ctx, `UPDATE tool_batches SET status = ?, continuation_claimed = 1, updated_at = ? WHERE id = ? AND continuation_claimed = 0`, targetStatus, now, batchID)
		if updateErr != nil {
			return false, false, batchID, updateErr
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			if err = s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.batch_completed", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"batch_id": batchID, "status": targetStatus, "tool_count": total, "succeeded": succeeded, "failed": failed, "blocked": blocked}}); err != nil {
				return false, false, batchID, err
			}
		}
		return true, changed == 1, batchID, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tool_batches
		SET status = 'succeeded', continuation_claimed = 1, updated_at = ?
		WHERE id = ? AND continuation_claimed = 0`, now, batchID)
	if err != nil {
		return false, false, batchID, err
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return false, false, batchID, err
	}
	if claimed == 1 {
		if err = s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "tool.batch_completed", ActorType: "system", ActorID: step.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"batch_id": batchID, "status": "succeeded", "tool_count": total, "succeeded": succeeded, "failed": failed, "blocked": blocked}}); err != nil {
			return false, false, batchID, err
		}
	}
	return true, claimed == 1, batchID, nil
}

func (s *Store) queueSkillResume(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, sourceStep core.ClaimedStep, run core.RunContext, output map[string]any, now, correlationID string) (bool, error) {
	skillID, _ := output["skill_id"].(string)
	skillName, _ := output["skill_name"].(string)
	skillArgs, _ := output["skill_args"].(string)
	if strings.TrimSpace(skillID) == "" {
		return false, fmt.Errorf("SKILL_RESUME_INPUT_INVALID: completed skill invocation has no skill id")
	}
	var snapshot struct {
		Skills []struct {
			ID string `json:"id"`
		} `json:"skill_capabilities"`
	}
	if json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snapshot) != nil {
		return false, fmt.Errorf("SKILL_RESUME_SNAPSHOT_INVALID: Run snapshot cannot be decoded")
	}
	found := false
	for _, skill := range snapshot.Skills {
		if skill.ID == skillID {
			found = true
			break
		}
	}
	if !found {
		return false, fmt.Errorf("SKILL_NOT_SNAPSHOTTED: %s", skillID)
	}
	var existing int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE run_id = ? AND step_type = ?`, run.RunID, core.StepModelSkillResume).Scan(&existing); err != nil {
		return false, err
	}
	if existing > 0 {
		return false, fmt.Errorf("SKILL_RECURSIVE_INVOCATION_DENIED: Run already contains a Skill resume step")
	}
	inputJSON, err := json.Marshal(map[string]any{"invoked_skill_ids": []string{skillID}, "skill_args": skillArgs, "skill_source": "model", "source_step_id": sourceStep.ID})
	if err != nil {
		return false, err
	}
	stepID, err := newID()
	if err != nil {
		return false, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, run.RunID).Scan(&position); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stepID, run.OrganizationID, run.ProjectID, run.RunID, core.StepModelSkillResume, position, core.StatusQueued, string(inputJSON), now, now,
	); err != nil {
		return false, err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "skill.invoked", ActorType: "agent", ActorID: sourceStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"skill_id": skillID, "skill_name": skillName, "source": "model", "status": "loaded", "source_step_id": sourceStep.ID, "has_args": strings.TrimSpace(skillArgs) != ""}}); err != nil {
		return false, err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.skill_resume_queued", ActorType: "system", ActorID: sourceStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": stepID, "source_step_id": sourceStep.ID, "skill_id": skillID, "skill_name": skillName}}); err != nil {
		return false, err
	}
	return true, nil
}

// collectContinuationHistory gathers succeeded model outputs and tool
// results for a run in execution order. Shared by the Agent Loop
// continuation and the steering replan path.
func (s *Store) collectContinuationHistory(ctx context.Context, tx *sql.Tx, run core.RunContext) ([]map[string]any, error) {
	history := make([]map[string]any, 0, 16)

	modelRows, err := tx.QueryContext(ctx, `
		SELECT id, position, output_json
		FROM steps
		WHERE run_id = ? AND step_type IN ('model_response', 'model_skill_resume', 'model_continue', 'model_synthesis')
		  AND status = 'succeeded'
		ORDER BY position`, run.RunID)
	if err != nil {
		return nil, err
	}
	for modelRows.Next() {
		var stepID, outputJSON string
		var position int64
		if err := modelRows.Scan(&stepID, &position, &outputJSON); err != nil {
			_ = modelRows.Close()
			return nil, err
		}
		var output map[string]any
		if json.Unmarshal([]byte(outputJSON), &output) != nil {
			continue
		}
		content, _ := output["content"].(string)
		history = append(history, map[string]any{"kind": "assistant", "step_id": stepID, "group_id": stepID, "position": position, "content": content})
	}
	if err := modelRows.Close(); err != nil {
		return nil, err
	}

	toolRows, err := tx.QueryContext(ctx, `
		SELECT tc.id, tc.step_id, tc.position, tc.tool_name, tc.arguments_json, tc.status, tc.error_code,
		       COALESCE(r.output_json, '{}'), COALESCE(tb.source_step_id, '')
		FROM tool_calls tc
		LEFT JOIN side_effect_receipts r ON r.tool_call_id = tc.id
		LEFT JOIN tool_batches tb ON tb.id = tc.tool_batch_id
		WHERE tc.run_id = ?
		ORDER BY tc.position`, run.RunID)
	if err != nil {
		return nil, err
	}
	for toolRows.Next() {
		var callID, toolStepID, toolName, argumentsJSON, status, errorCode, outputJSON, sourceStepID string
		var position int64
		if err := toolRows.Scan(&callID, &toolStepID, &position, &toolName, &argumentsJSON, &status, &errorCode, &outputJSON, &sourceStepID); err != nil {
			_ = toolRows.Close()
			return nil, err
		}
		var arguments, output any
		if json.Unmarshal([]byte(argumentsJSON), &arguments) != nil {
			arguments = map[string]any{}
		}
		if json.Unmarshal([]byte(outputJSON), &output) != nil {
			output = map[string]any{}
		}
		if sourceStepID == "" {
			sourceStepID = fmt.Sprintf("legacy-tool:%020d", position)
		}
		history = append(history, map[string]any{"kind": "tool_result", "tool_call_id": callID, "step_id": toolStepID, "group_id": sourceStepID, "position": position, "tool_name": toolName, "status": status, "error_code": errorCode, "arguments": arguments, "output": output})
	}
	if err := toolRows.Close(); err != nil {
		return nil, err
	}
	sort.SliceStable(history, func(i, j int) bool {
		leftPosition, _ := history[i]["position"].(int64)
		rightPosition, _ := history[j]["position"].(int64)
		return leftPosition < rightPosition
	})
	history, err = s.artifactizeLargeToolResults(ctx, tx, run, history, 32*1024)
	if err != nil {
		return nil, err
	}
	history = boundedContinuationHistory(history, 96*1024)
	return history, nil
}

// insertQueuedModelStep appends a queued model_continue step carrying input to
// the run. Shared by the Agent Loop continuation and the steering replan path.
func insertQueuedModelStep(ctx context.Context, tx *sql.Tx, run core.RunContext, input map[string]any, now string) (string, error) {
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	stepID, err := newID()
	if err != nil {
		return "", err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, run.RunID).Scan(&position); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stepID, run.OrganizationID, run.ProjectID, run.RunID, core.StepModelContinue, position, core.StatusQueued, string(inputJSON), now, now,
	); err != nil {
		return "", err
	}
	return stepID, nil
}

// queueModelContinuation is the durable Agent Loop boundary. A completed tool
// batch is not a completed user goal: verified tool results are snapshotted into
// a new model step so a restarted Worker can continue from the same state.
func (s *Store) queueModelContinuation(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, sourceStep core.ClaimedStep, run core.RunContext, now, correlationID string) (string, error) {
	invokedSkillIDs, skillArgs := stepSkillInvocation(sourceStep.InputJSON)
	history, err := s.collectContinuationHistory(ctx, tx, run)
	if err != nil {
		return "", err
	}
	if len(history) == 0 {
		return "", nil
	}

	input := map[string]any{"history": history, "stage": "continue", "source_step_id": sourceStep.ID}
	if len(invokedSkillIDs) > 0 {
		input["invoked_skill_ids"] = invokedSkillIDs
		input["skill_args"] = skillArgs
	}
	stepID, err := insertQueuedModelStep(ctx, tx, run, input, now)
	if err != nil {
		return "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.continuation_queued", ActorType: "system", ActorID: sourceStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": stepID, "source_step_id": sourceStep.ID, "history_items": len(history)}}); err != nil {
		return "", err
	}
	return stepID, nil
}

func (s *Store) queueLengthTruncatedModelContinuation(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, sourceStep core.ClaimedStep, run core.RunContext, now, correlationID string) (string, error) {
	history, err := s.collectContinuationHistory(ctx, tx, run)
	if err != nil {
		return "", err
	}
	if len(history) == 0 {
		return "", nil
	}
	stepID, err := insertQueuedModelStep(ctx, tx, run, map[string]any{
		"history":                     history,
		"stage":                       "continue_after_length_truncation",
		"source_step_id":              sourceStep.ID,
		"length_truncated_tool_calls": true,
	}, now)
	if err != nil {
		return "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{
		OrganizationID: run.OrganizationID,
		ProjectID:      run.ProjectID,
		RunID:          run.RunID,
		EventType:      "model.continuation_queued",
		ActorType:      "system",
		ActorID:        sourceStep.OwnerID,
		CorrelationID:  correlationID,
		Visibility:     run.Visibility,
		Payload: map[string]any{
			"step_id":        stepID,
			"source_step_id": sourceStep.ID,
			"history_items":  len(history),
			"reason":         "length_truncated_tool_calls",
		},
	}); err != nil {
		return "", err
	}
	return stepID, nil
}

func isRecoverableToolExecutionFailure(code string) bool {
	if strings.TrimSpace(code) == "" {
		return false
	}
	switch code {
	case "SIDE_EFFECT_STATUS_UNKNOWN", "TOOL_POLICY_DENIED", "APPROVAL_REJECTED", "APPROVAL_EXPIRED":
		return false
	default:
		return true
	}
}

func (s *Store) queueToolFailureRecovery(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, sourceStep core.ClaimedStep, run core.RunContext, reason, now, correlationID string) (string, int, string, bool, error) {
	var recoveryCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id = ? AND event_type = 'tool.recovery_queued'`, run.RunID).Scan(&recoveryCount); err != nil {
		return "", 0, "", false, err
	}
	if recoveryCount >= core.DefaultToolRetryLimit {
		return "", recoveryCount, "", false, nil
	}
	var sourceInput map[string]any
	if json.Unmarshal([]byte(sourceStep.InputJSON), &sourceInput) != nil {
		return "", recoveryCount, "", false, fmt.Errorf("TOOL_RECOVERY_INPUT_INVALID: source step input is not JSON")
	}
	toolCallID, _ := sourceInput["tool_call_id"].(string)
	toolName, _ := sourceInput["name"].(string)
	modelSourceStepID, _ := sourceInput["model_source_step_id"].(string)

	var sourcePosition int64
	if err := tx.QueryRowContext(ctx, `SELECT position FROM steps WHERE id = ? AND run_id = ?`, sourceStep.ID, run.RunID).Scan(&sourcePosition); err != nil {
		return "", recoveryCount, toolCallID, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, input_json FROM steps WHERE run_id = ? AND position > ? AND status IN ('queued', 'waiting_approval') ORDER BY position`, run.RunID, sourcePosition)
	if err != nil {
		return "", recoveryCount, toolCallID, false, err
	}
	deferredStepIDs := make([]string, 0)
	for rows.Next() {
		var stepID, inputJSON string
		if err := rows.Scan(&stepID, &inputJSON); err != nil {
			_ = rows.Close()
			return "", recoveryCount, toolCallID, false, err
		}
		var input map[string]any
		if json.Unmarshal([]byte(inputJSON), &input) == nil && input["model_source_step_id"] == modelSourceStepID {
			deferredStepIDs = append(deferredStepIDs, stepID)
		}
	}
	if err := rows.Close(); err != nil {
		return "", recoveryCount, toolCallID, false, err
	}
	for _, stepID := range deferredStepIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'canceled', error_code = 'PLAN_SUPERSEDED', updated_at = ? WHERE id = ?`, now, stepID); err != nil {
			return "", recoveryCount, toolCallID, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'rejected', error_code = 'PLAN_SUPERSEDED', updated_at = ? WHERE step_id = ?`, now, stepID); err != nil {
			return "", recoveryCount, toolCallID, false, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = 'system', decision_note = '计划已由自动恢复流程替换', decided_at = ? WHERE step_id = ? AND status = 'pending'`, now, stepID); err != nil {
			return "", recoveryCount, toolCallID, false, err
		}
	}

	recoveryStepID, err := s.queueModelContinuation(ctx, tx, q, sourceStep, run, now, correlationID)
	if err != nil || recoveryStepID == "" {
		return "", recoveryCount, toolCallID, false, err
	}
	recoveryCount++
	var recoveryInputJSON string
	if err := tx.QueryRowContext(ctx, `SELECT input_json FROM steps WHERE id = ?`, recoveryStepID).Scan(&recoveryInputJSON); err != nil {
		return "", recoveryCount, toolCallID, false, err
	}
	var recoveryInput map[string]any
	if json.Unmarshal([]byte(recoveryInputJSON), &recoveryInput) != nil {
		return "", recoveryCount, toolCallID, false, fmt.Errorf("TOOL_RECOVERY_INPUT_INVALID: continuation input is not JSON")
	}
	recoveryInput["tool_recovery_count"] = recoveryCount
	recoveryInput["tool_recovery_reason"] = reason
	recoveryInput["tool_recovery_name"] = toolName
	recoveryInput["tool_recovery_source_step_id"] = sourceStep.ID
	encoded, err := json.Marshal(recoveryInput)
	if err != nil {
		return "", recoveryCount, toolCallID, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET input_json = ?, updated_at = ? WHERE id = ?`, string(encoded), now, recoveryStepID); err != nil {
		return "", recoveryCount, toolCallID, false, err
	}
	return recoveryStepID, recoveryCount, toolCallID, true, nil
}

func isRetryableModelInteractionError(code string) bool {
	switch code {
	case "PROVIDER_UNAVAILABLE", "PROVIDER_RATE_LIMIT", "PROVIDER_PROTOCOL_ERROR", "PROVIDER_EMPTY_RESPONSE", "PROVIDER_TOOL_ARGUMENTS_INVALID", "PROVIDER_TOOL_ARGUMENTS_TRUNCATED":
		return true
	default:
		return false
	}
}

// queueModelInteractionRetry persists a fresh model Step instead of hiding
// retries inside the provider adapter. Each provider call therefore remains
// visible in the Run ledger, budget, timeline, and checkpoints.
func (s *Store) queueModelInteractionRetry(ctx context.Context, tx *sql.Tx, sourceStep core.ClaimedStep, run core.RunContext, reason, now string) (string, int, bool, error) {
	input := map[string]any{}
	if strings.TrimSpace(sourceStep.InputJSON) != "" && json.Unmarshal([]byte(sourceStep.InputJSON), &input) != nil {
		return "", 0, false, fmt.Errorf("MODEL_RETRY_INPUT_INVALID: source step input is not JSON")
	}
	if input == nil {
		input = map[string]any{}
	}
	retryCount := 0
	if value, ok := numericToolValue(input["model_retry_count"]); ok {
		retryCount = int(value)
	} else if value, ok := numericToolValue(input["model_compat_retry_count"]); ok {
		retryCount = int(value)
	}
	if retryCount >= core.DefaultModelRetryLimit {
		return "", retryCount, false, nil
	}
	retryCount++
	input["model_retry_count"] = retryCount
	input["model_retry_reason"] = reason
	input["model_retry_source_step_id"] = sourceStep.ID
	delete(input, "model_compat_retry_count")
	delete(input, "model_compat_retry_reason")
	delete(input, "model_compat_retry_source_step_id")
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return "", 0, false, err
	}
	stepID, err := newID()
	if err != nil {
		return "", 0, false, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, run.RunID).Scan(&position); err != nil {
		return "", 0, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stepID, run.OrganizationID, run.ProjectID, run.RunID, sourceStep.StepType, position, core.StatusQueued, string(inputJSON), now, now,
	); err != nil {
		return "", 0, false, err
	}
	return stepID, retryCount, true, nil
}

func boundedContinuationHistory(history []map[string]any, maxBytes int) []map[string]any {
	for len(history) > 1 {
		encoded, err := json.Marshal(history)
		if err == nil && len(encoded) <= maxBytes {
			break
		}
		groupID, _ := history[0]["group_id"].(string)
		cut := 1
		if groupID != "" {
			for cut < len(history) {
				nextGroupID, _ := history[cut]["group_id"].(string)
				if nextGroupID != groupID {
					break
				}
				cut++
			}
		}
		history = history[cut:]
	}
	return history
}

func (s *Store) artifactizeLargeToolResults(ctx context.Context, tx *sql.Tx, run core.RunContext, history []map[string]any, maxInlineBytes int) ([]map[string]any, error) {
	q := s.queries.WithTx(tx)
	now := formatTime(time.Now())
	for _, item := range history {
		if item["kind"] != "tool_result" {
			continue
		}
		output, exists := item["output"]
		if !exists {
			continue
		}
		body, err := json.Marshal(output)
		if err != nil || len(body) <= maxInlineBytes {
			continue
		}
		stepID, _ := item["step_id"].(string)
		callID, _ := item["tool_call_id"].(string)
		if stepID == "" || callID == "" {
			continue
		}
		artifactName := "tool-result-" + callID + ".json"
		var existingID, existingHash, existingMediaType string
		var existingSize int64
		existingErr := tx.QueryRowContext(ctx, `
			SELECT id, content_hash, size_bytes, media_type
			FROM artifacts
			WHERE run_id = ? AND step_id = ? AND name = ? AND source_type = 'tool_result_truncation'
			ORDER BY created_at, id LIMIT 1`,
			run.RunID, stepID, artifactName,
		).Scan(&existingID, &existingHash, &existingSize, &existingMediaType)
		if existingErr == nil {
			item["output"] = map[string]any{
				"truncated":    true,
				"artifact_id":  existingID,
				"content_hash": existingHash,
				"size_bytes":   existingSize,
				"media_type":   existingMediaType,
				"preview":      truncateText(string(body), 2_000),
			}
			continue
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return nil, existingErr
		}
		persisted, err := s.persistBlob(ctx, body, "application/json")
		if err != nil {
			return nil, fmt.Errorf("persist large tool result: %w", err)
		}
		if err := ensureBlob(ctx, tx, persisted, now); err != nil {
			return nil, err
		}
		artifactID, err := newID()
		if err != nil {
			return nil, err
		}
		row, err := q.CreateArtifact(ctx, sqlitegen.CreateArtifactParams{
			ID:             artifactID,
			OrganizationID: run.OrganizationID,
			ProjectID:      run.ProjectID,
			TaskID:         run.TaskID,
			RunID:          run.RunID,
			StepID:         stepID,
			Name:           artifactName,
			Kind:           "tool_result",
			MediaType:      persisted.MediaType,
			ContentHash:    persisted.ContentHash,
			SizeBytes:      persisted.SizeBytes,
			Visibility:     run.Visibility,
			SourceType:     "tool_result_truncation",
			CreatedAt:      now,
		})
		if err != nil {
			return nil, err
		}
		preview := truncateText(string(body), 2_000)
		item["output"] = map[string]any{
			"truncated":    true,
			"artifact_id":  row.ID,
			"content_hash": row.ContentHash,
			"size_bytes":   row.SizeBytes,
			"media_type":   row.MediaType,
			"preview":      preview,
		}
		if err := s.appendEvent(ctx, q, eventInput{
			OrganizationID: run.OrganizationID,
			ProjectID:      run.ProjectID,
			RunID:          run.RunID,
			EventType:      "artifact.created",
			ActorType:      "system",
			ActorID:        "",
			CorrelationID:  callID,
			Visibility:     run.Visibility,
			Payload: map[string]any{
				"artifact_id":  row.ID,
				"step_id":      stepID,
				"tool_call_id": callID,
				"name":         row.Name,
				"kind":         row.Kind,
				"source_type":  row.SourceType,
				"content_hash": row.ContentHash,
				"size_bytes":   row.SizeBytes,
			},
		}); err != nil {
			return nil, err
		}
	}
	return history, nil
}

func finalToolRunSummary(ctx context.Context, tx *sql.Tx, runID string) (string, error) {
	var outputJSON string
	if err := tx.QueryRowContext(ctx, `SELECT output_json FROM steps WHERE run_id = ? AND step_type IN ('model_response', 'model_skill_resume') ORDER BY position DESC LIMIT 1`, runID).Scan(&outputJSON); err != nil {
		return "", err
	}
	var output map[string]any
	if err := json.Unmarshal([]byte(outputJSON), &output); err != nil {
		return "", err
	}
	summary, _ := output["content"].(string)
	var toolCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND status = 'succeeded'`, runID).Scan(&toolCount); err != nil {
		return "", err
	}
	if strings.TrimSpace(summary) == "" {
		summary = "任务已完成。"
	}
	return fmt.Sprintf("%s\n\n---\n\n已执行并通过确定性验收的受控工具动作：%d 个。", summary, toolCount), nil
}

func (s *Store) queueWebSearchSynthesis(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, sourceStep core.ClaimedStep, run core.RunContext, now, correlationID string) (bool, error) {
	var searchCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_calls WHERE run_id = ? AND tool_name = ? AND status = 'succeeded'`, run.RunID, core.ToolWebSearch).Scan(&searchCount); err != nil {
		return false, err
	}
	if searchCount == 0 {
		return false, nil
	}
	var existing int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM steps WHERE run_id = ? AND step_type = ?`, run.RunID, core.StepModelSynthesis).Scan(&existing); err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT tc.arguments_json, receipt.output_json
		FROM tool_calls tc
		JOIN side_effect_receipts receipt ON receipt.tool_call_id = tc.id
		WHERE tc.run_id = ? AND tc.tool_name = ? AND tc.status = 'succeeded' AND receipt.status = 'succeeded'
		ORDER BY tc.position, receipt.started_at`, run.RunID, core.ToolWebSearch)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	evidence := make([]map[string]any, 0, searchCount)
	totalBytes := 0
	const maxEvidenceBytes = 64 * 1024
	for rows.Next() {
		var argumentsJSON, outputJSON string
		if err := rows.Scan(&argumentsJSON, &outputJSON); err != nil {
			return false, err
		}
		if totalBytes+len(argumentsJSON)+len(outputJSON) > maxEvidenceBytes {
			break
		}
		var arguments, output any
		if json.Unmarshal([]byte(argumentsJSON), &arguments) != nil || json.Unmarshal([]byte(outputJSON), &output) != nil {
			continue
		}
		evidence = append(evidence, map[string]any{"arguments": arguments, "output": output})
		totalBytes += len(argumentsJSON) + len(outputJSON)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(evidence) == 0 {
		return false, fmt.Errorf("WEB_SEARCH_EVIDENCE_MISSING: successful search receipts contain no bounded output")
	}
	inputValue := map[string]any{"search_results": evidence}
	if invokedSkillIDs, skillArgs := stepSkillInvocation(sourceStep.InputJSON); len(invokedSkillIDs) > 0 {
		inputValue["invoked_skill_ids"] = invokedSkillIDs
		inputValue["skill_args"] = skillArgs
	}
	inputJSON, err := json.Marshal(inputValue)
	if err != nil {
		return false, err
	}
	stepID, err := newID()
	if err != nil {
		return false, err
	}
	var position int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position), 0) + 1 FROM steps WHERE run_id = ?`, run.RunID).Scan(&position); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO steps (id, organization_id, project_id, run_id, step_type, position, status, input_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		stepID, run.OrganizationID, run.ProjectID, run.RunID, core.StepModelSynthesis, position, core.StatusQueued, string(inputJSON), now, now,
	); err != nil {
		return false, err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.synthesis_queued", ActorType: "system", ActorID: sourceStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": stepID, "source_step_id": sourceStep.ID, "search_call_count": len(evidence), "evidence_bytes": totalBytes}}); err != nil {
		return false, err
	}
	return true, nil
}

func webSearchSynthesisThresholdReached(ctx context.Context, tx *sql.Tx, runID string) (bool, error) {
	var successfulCalls, resultCount int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE
			WHEN json_valid(receipt.output_json) THEN json_array_length(receipt.output_json, '$.results')
			ELSE 0 END), 0)
		FROM tool_calls tc
		JOIN side_effect_receipts receipt ON receipt.tool_call_id = tc.id
		WHERE tc.run_id = ? AND tc.tool_name = ? AND tc.status = 'succeeded' AND receipt.status = 'succeeded'`,
		runID, core.ToolWebSearch,
	).Scan(&successfulCalls, &resultCount); err != nil {
		return false, err
	}
	// Six successful calls normally represent two independent three-way search
	// batches. Once they yield at least three candidate results, more search is
	// lower value than a no-tools synthesis pass. A hard ceiling of twelve calls
	// also guarantees convergence when providers return sparse results.
	return (successfulCalls >= 6 && resultCount >= 3) || successfulCalls >= 12, nil
}

type persistedBlob = application.BlobDescriptor

func ensureBlob(ctx context.Context, tx *sql.Tx, persisted persistedBlob, now string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO blobs (content_hash, storage_key, size_bytes, media_type, created_at)
		VALUES (?, ?, ?, ?, ?)`, persisted.ContentHash, persisted.StorageKey, persisted.SizeBytes, persisted.MediaType, now)
	return err
}

func (s *Store) persistBlob(ctx context.Context, content []byte, mediaType string) (persistedBlob, error) {
	return s.persistBlobReader(ctx, bytes.NewReader(content), mediaType, int64(len(content)))
}

func (s *Store) persistBlobReader(ctx context.Context, content io.Reader, mediaType string, maxBytes int64) (persistedBlob, error) {
	return s.blobStore.Put(ctx, content, mediaType, maxBytes)
}

func createCheckpoint(ctx context.Context, q *sqlitegen.Queries, step core.ClaimedStep, run core.RunContext, kind string, state map[string]any) (string, string, error) {
	body, err := json.Marshal(state)
	if err != nil {
		return "", "", err
	}
	checkpointID, err := newID()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(body)
	contentHash := fmt.Sprintf("sha256:%x", sum[:])
	if err := q.CreateCheckpoint(ctx, sqlitegen.CreateCheckpointParams{
		ID:             checkpointID,
		OrganizationID: run.OrganizationID,
		ProjectID:      run.ProjectID,
		RunID:          run.RunID,
		StepID:         step.ID,
		AttemptID:      step.AttemptID,
		CheckpointKind: kind,
		StateJson:      string(body),
		ContentHash:    contentHash,
		CreatedAt:      formatTime(time.Now()),
	}); err != nil {
		return "", "", err
	}
	return checkpointID, contentHash, nil
}

type eventInput struct {
	OrganizationID string
	ProjectID      string
	RunID          string
	EventType      string
	ActorType      string
	ActorID        string
	CorrelationID  string
	CausationID    string
	Visibility     string
	Payload        map[string]any
}

func (s *Store) appendEvent(ctx context.Context, q *sqlitegen.Queries, input eventInput) error {
	sequence, err := q.AllocateRunEventSequence(ctx, input.RunID)
	if err != nil {
		return fmt.Errorf("allocate event sequence: %w", err)
	}
	payload, err := json.Marshal(input.Payload)
	if err != nil {
		return err
	}
	eventID, err := newID()
	if err != nil {
		return err
	}
	now := formatTime(time.Now())
	row, err := q.CreateRunEvent(ctx, sqlitegen.CreateRunEventParams{ID: eventID, OrganizationID: input.OrganizationID, ProjectID: input.ProjectID, RunID: input.RunID, StreamSeq: sequence, EventType: input.EventType, ActorType: input.ActorType, ActorID: input.ActorID, CorrelationID: input.CorrelationID, CausationID: input.CausationID, Visibility: input.Visibility, PayloadJson: string(payload), OccurredAt: now})
	if err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	outboxID, err := newID()
	if err != nil {
		return err
	}
	outboxPayload, _ := json.Marshal(map[string]any{"event_id": row.ID, "run_id": row.RunID, "stream_seq": row.StreamSeq})
	return q.CreateOutboxEvent(ctx, sqlitegen.CreateOutboxEventParams{ID: outboxID, OrganizationID: input.OrganizationID, ProjectID: input.ProjectID, Topic: "run.events", AggregateID: input.RunID, PayloadJson: string(outboxPayload), CreatedAt: now})
}

type sessionMessageInput struct {
	MessageID      string
	OrganizationID string
	ProjectID      string
	ConversationID string
	OwnerUserID    string
	TaskID         string
	Role           string
	Visibility     string
	Content        string
	Now            string
}

type sessionEntryInput struct {
	OrganizationID      string
	ProjectID           string
	ConversationID      string
	Kind                string
	RefID               string
	Label               string
	Summary             string
	Namespace           string
	PayloadJSON         string
	ContextInjection    string
	ExpectedLeafEntryID string
	Now                 string
}

func (s *Store) appendSessionEntryTx(ctx context.Context, tx *sql.Tx, input sessionEntryInput) (core.SessionEntry, error) {
	entryID, err := newID()
	if err != nil {
		return core.SessionEntry{}, err
	}
	var activeLeaf sql.NullString
	var nextEntrySeq int64
	if err := tx.QueryRowContext(ctx, `SELECT active_leaf_entry_id, next_entry_seq FROM conversations WHERE id = ?`, input.ConversationID).Scan(&activeLeaf, &nextEntrySeq); err != nil {
		return core.SessionEntry{}, err
	}
	if input.ExpectedLeafEntryID != "" && input.ExpectedLeafEntryID != activeLeaf.String {
		return core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "append_session_entry"}
	}
	entrySeq := nextEntrySeq
	if entrySeq < 1 {
		entrySeq = 1
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE conversations SET next_entry_seq = ?
		WHERE id = ? AND next_entry_seq = ? AND COALESCE(active_leaf_entry_id, '') = ?`,
		entrySeq+1, input.ConversationID, nextEntrySeq, activeLeaf.String,
	)
	if err != nil {
		return core.SessionEntry{}, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return core.SessionEntry{}, changedErr
		}
		return core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "append_session_entry"}
	}
	var parent any
	if activeLeaf.Valid && activeLeaf.String != "" {
		parent = activeLeaf.String
	}
	payload := strings.TrimSpace(input.PayloadJSON)
	if payload == "" {
		payload = "{}"
	}
	contextInjection := strings.TrimSpace(input.ContextInjection)
	if contextInjection == "" {
		contextInjection = "default"
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_entries (
			id, organization_id, project_id, conversation_id, parent_entry_id,
			seq, entry_seq, kind, ref_id, label, summary, namespace,
			payload_json, schema_version, context_injection, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 2, ?, ?)`,
		entryID, input.OrganizationID, input.ProjectID, input.ConversationID, parent,
		entrySeq, entrySeq, input.Kind, input.RefID, input.Label, input.Summary,
		input.Namespace, payload, contextInjection, input.Now,
	); err != nil {
		return core.SessionEntry{}, err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE conversations SET active_leaf_entry_id = ?, updated_at = ?
		WHERE id = ? AND COALESCE(active_leaf_entry_id, '') = ?`,
		entryID, input.Now, input.ConversationID, activeLeaf.String,
	)
	if err != nil {
		return core.SessionEntry{}, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return core.SessionEntry{}, changedErr
		}
		return core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "append_session_entry"}
	}
	createdAt, err := parseTime(input.Now)
	if err != nil {
		return core.SessionEntry{}, err
	}
	return core.SessionEntry{
		ID:               entryID,
		OrganizationID:   input.OrganizationID,
		ProjectID:        input.ProjectID,
		ConversationID:   input.ConversationID,
		ParentEntryID:    activeLeaf.String,
		Seq:              entrySeq,
		EntrySeq:         entrySeq,
		Kind:             input.Kind,
		RefID:            input.RefID,
		Label:            input.Label,
		Summary:          input.Summary,
		Namespace:        input.Namespace,
		PayloadJSON:      payload,
		SchemaVersion:    2,
		ContextInjection: contextInjection,
		CreatedAt:        createdAt,
	}, nil
}

func (s *Store) appendSessionMessageTx(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, input sessionMessageInput) (core.Message, core.SessionEntry, error) {
	messageID := strings.TrimSpace(input.MessageID)
	var err error
	if messageID == "" {
		messageID, err = newID()
		if err != nil {
			return core.Message{}, core.SessionEntry{}, err
		}
	}
	entryID, err := newID()
	if err != nil {
		return core.Message{}, core.SessionEntry{}, err
	}
	messageSeq, err := q.AllocateMessageSequence(ctx, sqlitegen.AllocateMessageSequenceParams{UpdatedAt: input.Now, ID: input.ConversationID})
	if err != nil {
		return core.Message{}, core.SessionEntry{}, fmt.Errorf("allocate message sequence: %w", err)
	}
	var activeLeaf sql.NullString
	var nextEntrySeq int64
	if err := tx.QueryRowContext(ctx, `SELECT active_leaf_entry_id, next_entry_seq FROM conversations WHERE id = ?`, input.ConversationID).Scan(&activeLeaf, &nextEntrySeq); err != nil {
		return core.Message{}, core.SessionEntry{}, err
	}
	entrySeq := nextEntrySeq
	if entrySeq < 1 {
		entrySeq = 1
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE conversations
		SET next_entry_seq = ?
		WHERE id = ? AND next_entry_seq = ? AND COALESCE(active_leaf_entry_id, '') = ?`,
		entrySeq+1, input.ConversationID, nextEntrySeq, activeLeaf.String,
	)
	if err != nil {
		return core.Message{}, core.SessionEntry{}, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return core.Message{}, core.SessionEntry{}, changedErr
		}
		return core.Message{}, core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "append_session_message"}
	}
	if _, err := q.CreateMessage(ctx, sqlitegen.CreateMessageParams{
		ID:             messageID,
		OrganizationID: input.OrganizationID,
		ProjectID:      input.ProjectID,
		ConversationID: input.ConversationID,
		OwnerUserID:    input.OwnerUserID,
		ServerSeq:      messageSeq,
		Role:           input.Role,
		Visibility:     input.Visibility,
		Content:        input.Content,
		CreatedAt:      input.Now,
		TaskID:         input.TaskID,
	}); err != nil {
		return core.Message{}, core.SessionEntry{}, err
	}
	var parent any
	if activeLeaf.Valid && activeLeaf.String != "" {
		parent = activeLeaf.String
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_entries (
			id, organization_id, project_id, conversation_id, parent_entry_id,
			seq, entry_seq, kind, ref_id, label, summary, namespace,
			payload_json, schema_version, context_injection, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'message', ?, '', '', '', '{}', 2, 'default', ?)`,
		entryID, input.OrganizationID, input.ProjectID, input.ConversationID, parent,
		entrySeq, entrySeq, messageID, input.Now,
	); err != nil {
		return core.Message{}, core.SessionEntry{}, err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE conversations
		SET active_leaf_entry_id = ?, updated_at = ?
		WHERE id = ? AND COALESCE(active_leaf_entry_id, '') = ?`,
		entryID, input.Now, input.ConversationID, activeLeaf.String,
	)
	if err != nil {
		return core.Message{}, core.SessionEntry{}, err
	}
	if changed, changedErr := result.RowsAffected(); changedErr != nil || changed != 1 {
		if changedErr != nil {
			return core.Message{}, core.SessionEntry{}, changedErr
		}
		return core.Message{}, core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "append_session_message"}
	}
	createdAt, err := parseTime(input.Now)
	if err != nil {
		return core.Message{}, core.SessionEntry{}, err
	}
	message := core.Message{
		ID:             messageID,
		OrganizationID: input.OrganizationID,
		ProjectID:      input.ProjectID,
		ConversationID: input.ConversationID,
		OwnerUserID:    input.OwnerUserID,
		TaskID:         input.TaskID,
		ServerSeq:      messageSeq,
		Role:           input.Role,
		Visibility:     input.Visibility,
		Content:        input.Content,
		CreatedAt:      createdAt,
	}
	entry := core.SessionEntry{
		ID:               entryID,
		OrganizationID:   input.OrganizationID,
		ProjectID:        input.ProjectID,
		ConversationID:   input.ConversationID,
		ParentEntryID:    activeLeaf.String,
		Seq:              entrySeq,
		EntrySeq:         entrySeq,
		Kind:             core.SessionEntryMessage,
		RefID:            messageID,
		PayloadJSON:      "{}",
		SchemaVersion:    2,
		ContextInjection: "default",
		CreatedAt:        createdAt,
	}
	return message, entry, nil
}

func (s *Store) appendConversationMessage(ctx context.Context, tx *sql.Tx, q *sqlitegen.Queries, run core.RunContext, role, content, now string) error {
	if strings.TrimSpace(run.ConversationID) == "" {
		return nil
	}
	if _, _, err := s.appendSessionMessageTx(ctx, tx, q, sessionMessageInput{
		OrganizationID: run.OrganizationID,
		ProjectID:      run.ProjectID,
		ConversationID: run.ConversationID,
		OwnerUserID:    run.OwnerUserID,
		TaskID:         run.TaskID,
		Role:           role,
		Visibility:     run.Visibility,
		Content:        content,
		Now:            now,
	}); err != nil {
		return fmt.Errorf("append conversation message: %w", err)
	}
	return nil
}

func projectFromRow(row sqlitegen.Project) (core.Project, error) {
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.Project{}, err
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return core.Project{}, err
	}
	return core.Project{ID: row.ID, OrganizationID: row.OrganizationID, OwnerUserID: row.OwnerUserID, Name: row.Name, Description: row.Description, Kind: row.Kind, Scene: row.Scene, DefaultModel: row.DefaultModel, FallbackModel: row.FallbackModel, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

type projectRuntimeSettingsQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func projectWithRuntimeSettings(ctx context.Context, queryer projectRuntimeSettingsQuerier, project core.Project) (core.Project, error) {
	project.MaxModelCalls = core.DefaultTaskMaxModelCalls
	project.MaxTokens = core.DefaultTaskMaxTokens
	err := queryer.QueryRowContext(ctx, `SELECT max_model_calls, max_tokens FROM project_runtime_settings WHERE project_id = ?`, project.ID).Scan(&project.MaxModelCalls, &project.MaxTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return project, nil
	}
	return project, err
}

func projectMemberFromFields(userID, displayName, email, role, status, created, updated string) (core.ProjectMember, error) {
	createdAt, err := parseTime(created)
	if err != nil {
		return core.ProjectMember{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return core.ProjectMember{}, err
	}
	return core.ProjectMember{UserID: userID, DisplayName: displayName, Email: email, Role: role, Status: status, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func planItemFromRow(row sqlitegen.PlanItem) (core.PlanItem, error) {
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.PlanItem{}, err
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return core.PlanItem{}, err
	}
	return core.PlanItem{
		ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID,
		OwnerUserID: row.OwnerUserID, Title: row.Title, Description: row.Description,
		Status: row.Status, Source: row.Source, LinkedTaskID: row.LinkedTaskID,
		Visibility: row.Visibility, Rank: row.Rank, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func taskFromRow(row sqlitegen.Task, runID string) (core.Task, error) {
	return taskFromFields(row.ID, row.OrganizationID, row.ProjectID, row.OwnerUserID, runID, row.ConversationID, row.Title, row.Goal, row.Acceptance, row.Mode, row.Visibility, row.Status, row.ErrorCode, row.ResultSummary, "", row.CreatedAt, row.UpdatedAt)
}

func taskFromGetRow(row sqlitegen.GetTaskForUserRow) (core.Task, error) {
	return taskFromFields(row.ID, row.OrganizationID, row.ProjectID, row.OwnerUserID, row.RunID, row.ConversationID, row.Title, row.Goal, row.Acceptance, row.Mode, row.Visibility, row.Status, row.ErrorCode, row.ResultSummary, stringValue(row.AcceptedOutcomeID), row.CreatedAt, row.UpdatedAt)
}

func taskFromListRow(row sqlitegen.ListTasksForUserRow) (core.Task, error) {
	return taskFromFields(row.ID, row.OrganizationID, row.ProjectID, row.OwnerUserID, row.RunID, row.ConversationID, row.Title, row.Goal, row.Acceptance, row.Mode, row.Visibility, row.Status, row.ErrorCode, row.ResultSummary, stringValue(row.AcceptedOutcomeID), row.CreatedAt, row.UpdatedAt)
}

func acceptedOutcomeFromRow(row sqlitegen.AcceptedOutcome) (core.AcceptedOutcome, error) {
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	return core.AcceptedOutcome{ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, TaskID: row.TaskID, RunID: row.RunID, AcceptedBy: row.AcceptedBy, Note: row.Note, CreatedAt: createdAt}, nil
}

func artifactFromRow(row sqlitegen.Artifact) (core.Artifact, error) {
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.Artifact{}, err
	}
	return core.Artifact{ID: row.ID, ProjectID: row.ProjectID, TaskID: row.TaskID, RunID: row.RunID, StepID: row.StepID, Name: row.Name, Kind: row.Kind, MediaType: row.MediaType, ContentHash: row.ContentHash, SizeBytes: row.SizeBytes, Visibility: row.Visibility, SourceType: row.SourceType, CreatedAt: createdAt}, nil
}

func assetFromRow(row sqlitegen.Asset) (core.Asset, error) {
	return assetFromFields(row.ID, row.OrganizationID, row.ProjectID, row.OwnerUserID, row.ParentID, row.Name, row.Kind, row.MediaType, row.ContentHash, row.SizeBytes, row.Visibility, row.SourceType, row.SourceArtifactID, row.CreatedAt, row.UpdatedAt)
}

func assetFromFields(id, organizationID, projectID, ownerUserID, parentID, name, kind, mediaType, contentHash string, sizeBytes int64, visibility, sourceType, sourceArtifactID, created, updated string) (core.Asset, error) {
	createdAt, err := parseTime(created)
	if err != nil {
		return core.Asset{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return core.Asset{}, err
	}
	return core.Asset{
		ID: id, OrganizationID: organizationID, ProjectID: projectID, OwnerUserID: ownerUserID,
		ParentID: parentID, Name: name, Kind: kind, MediaType: mediaType, ContentHash: contentHash,
		SizeBytes: sizeBytes, Visibility: visibility, SourceType: sourceType, SourceArtifactID: sourceArtifactID,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func scheduleFromRow(row sqlitegen.Schedule) (core.Schedule, error) {
	nextRunAt, err := parseTime(row.NextRunAt)
	if err != nil {
		return core.Schedule{}, err
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.Schedule{}, err
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return core.Schedule{}, err
	}
	return core.Schedule{ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, OwnerUserID: row.OwnerUserID, Name: row.Name, CronExpression: row.CronExpression, Timezone: row.Timezone, Goal: row.Goal, Acceptance: row.Acceptance, Mode: row.Mode, Visibility: row.Visibility, Status: row.Status, MisfirePolicy: row.MisfirePolicy, OverlapPolicy: row.OverlapPolicy, MisfireGraceSeconds: row.MisfireGraceSeconds, NextRunAt: nextRunAt, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func scheduleFireFromRow(row sqlitegen.ScheduleFire) (core.ScheduleFire, error) {
	plannedAt, err := parseTime(row.PlannedAt)
	if err != nil {
		return core.ScheduleFire{}, err
	}
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return core.ScheduleFire{}, err
	}
	updatedAt, err := parseTime(row.UpdatedAt)
	if err != nil {
		return core.ScheduleFire{}, err
	}
	return core.ScheduleFire{ID: row.ID, ScheduleID: row.ScheduleID, PlannedAt: plannedAt, Generation: row.Generation, Status: row.Status, TaskID: row.TaskID.String, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func capabilityFromRow(row sqlitegen.Capability) core.Capability {
	return core.Capability{ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, OwnerUserID: row.OwnerUserID, Kind: row.Kind, Name: row.Name, Version: row.Version, ManifestJSON: row.ManifestJson, ContentHash: row.ContentHash, Status: row.Status, RiskLevel: row.RiskLevel, PermissionDiff: core.CapabilityPermissionDiff(row.ManifestJson), CreatedAt: mustParseTime(row.CreatedAt), UpdatedAt: mustParseTime(row.UpdatedAt)}
}

func (s *Store) getCapabilityByIDTx(ctx context.Context, tx *sql.Tx, scope core.Scope, capabilityID string) (core.Capability, error) {
	var item core.Capability
	var createdAt, updatedAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT id, organization_id, project_id, owner_user_id, kind, name, version,
		       manifest_json, content_hash, status, risk_level, scope_type, scope_id,
		       source_filename, install_job_id, created_at, updated_at
		FROM capabilities WHERE id = ? AND organization_id = ?`, capabilityID, scope.OrganizationID).Scan(
		&item.ID, &item.OrganizationID, &item.ProjectID, &item.OwnerUserID, &item.Kind, &item.Name, &item.Version,
		&item.ManifestJSON, &item.ContentHash, &item.Status, &item.RiskLevel, &item.ScopeType, &item.ScopeID,
		&item.SourceFilename, &item.InstallJobID, &createdAt, &updatedAt,
	); err != nil {
		return core.Capability{}, err
	}
	item.BundleHash = item.ContentHash
	item.PermissionDiff = core.CapabilityPermissionDiff(item.ManifestJSON)
	item.CreatedAt = mustParseTime(createdAt)
	item.UpdatedAt = mustParseTime(updatedAt)
	return item, nil
}

func (s *Store) getCapabilityByID(ctx context.Context, scope core.Scope, capabilityID string) (core.Capability, error) {
	var item core.Capability
	var createdAt, updatedAt string
	if err := s.db.QueryRowContext(ctx, `
		SELECT id, organization_id, project_id, owner_user_id, kind, name, version,
		       manifest_json, content_hash, status, risk_level, scope_type, scope_id,
		       source_filename, install_job_id, created_at, updated_at
		FROM capabilities WHERE id = ? AND organization_id = ?`, capabilityID, scope.OrganizationID).Scan(
		&item.ID, &item.OrganizationID, &item.ProjectID, &item.OwnerUserID, &item.Kind, &item.Name, &item.Version,
		&item.ManifestJSON, &item.ContentHash, &item.Status, &item.RiskLevel, &item.ScopeType, &item.ScopeID,
		&item.SourceFilename, &item.InstallJobID, &createdAt, &updatedAt,
	); err != nil {
		return core.Capability{}, err
	}
	item.BundleHash = item.ContentHash
	item.PermissionDiff = core.CapabilityPermissionDiff(item.ManifestJSON)
	item.CreatedAt = mustParseTime(createdAt)
	item.UpdatedAt = mustParseTime(updatedAt)
	return item, nil
}

func (s *Store) writeCapabilityBundle(ctx context.Context, data []byte, contentHash string) (string, error) {
	digest := strings.TrimPrefix(contentHash, "sha256:")
	if len(digest) != 64 {
		return "", fmt.Errorf("invalid capability bundle hash")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != digest {
		return "", fmt.Errorf("capability bundle hash mismatch")
	}
	persisted, err := s.blobStore.Put(ctx, bytes.NewReader(data), "application/vnd.oktapod.capability+json", int64(len(data)))
	if err != nil {
		return "", err
	}
	if persisted.ContentHash != contentHash {
		return "", fmt.Errorf("capability bundle hash mismatch")
	}
	return persisted.StorageKey, nil
}

func memoryFromRow(row sqlitegen.Memory) core.Memory {
	return core.Memory{ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, OwnerUserID: row.OwnerUserID, Kind: row.Kind, Title: row.Title, Content: row.Content, SourceHash: row.SourceHash, Visibility: row.Visibility, Status: row.Status, SourceRefsJSON: row.SourceRefsJson, CreatedAt: mustParseTime(row.CreatedAt), UpdatedAt: mustParseTime(row.UpdatedAt)}
}

func agentDefinitionFromRow(row sqlitegen.AgentDefinition) core.AgentDefinition {
	return core.AgentDefinition{ID: row.ID, OrganizationID: row.OrganizationID, ProjectID: row.ProjectID, OwnerUserID: row.OwnerUserID, Name: row.Name, Version: row.Version, Instructions: row.Instructions, Model: row.Model, FallbackModel: row.FallbackModel, ConfigJSON: row.ConfigJson, Status: row.Status, CreatedAt: mustParseTime(row.CreatedAt), UpdatedAt: mustParseTime(row.UpdatedAt)}
}

func mustParseTime(value string) time.Time {
	parsed, err := parseTime(value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

type rowScanner interface {
	Scan(dest ...any) error
}

func getHumanActionForUser(ctx context.Context, runner idempotencyRunner, scope core.Scope, actionID string) (core.HumanAction, error) {
	row := runner.QueryRowContext(ctx, `
		SELECT a.id, a.organization_id, a.project_id, a.conversation_id, a.task_id,
		       a.run_id, a.step_id, a.plan_revision_id, a.run_plan_item_id,
		       a.kind, a.status, a.reviewer, a.risk_level, a.title, a.reason,
		       a.request_json, a.request_hash, a.response_json, a.requested_by,
		       a.decided_by, a.requested_at, a.expires_at, a.decided_at, a.version
		FROM human_action_requests a
		JOIN tasks t ON t.id = a.task_id
		JOIN project_memberships pm ON pm.project_id = a.project_id
		  AND pm.user_id = ? AND pm.status = 'active'
		WHERE a.id = ? AND a.organization_id = ?
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)`,
		scope.UserID, actionID, scope.OrganizationID, scope.UserID,
	)
	return scanHumanAction(row)
}

func scanHumanAction(scanner rowScanner) (core.HumanAction, error) {
	var item core.HumanAction
	var requestJSON, responseJSON, requestedAt, expiresAt, decidedAt string
	if err := scanner.Scan(
		&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID,
		&item.TaskID, &item.RunID, &item.StepID, &item.PlanRevisionID,
		&item.RunPlanItemID, &item.Kind, &item.Status, &item.Reviewer,
		&item.RiskLevel, &item.Title, &item.Reason, &requestJSON,
		&item.RequestHash, &responseJSON, &item.RequestedBy, &item.DecidedBy,
		&requestedAt, &expiresAt, &decidedAt, &item.Version,
	); err != nil {
		return core.HumanAction{}, err
	}
	item.Request, item.Response = map[string]any{}, map[string]any{}
	if err := json.Unmarshal([]byte(requestJSON), &item.Request); err != nil {
		return core.HumanAction{}, fmt.Errorf("decode human action request: %w", err)
	}
	if err := json.Unmarshal([]byte(responseJSON), &item.Response); err != nil {
		return core.HumanAction{}, fmt.Errorf("decode human action response: %w", err)
	}
	item.RequestedAt, item.ExpiresAt = mustParseTime(requestedAt), mustParseTime(expiresAt)
	if decidedAt != "" {
		value := mustParseTime(decidedAt)
		item.DecidedAt = &value
	}
	return item, nil
}

func getApprovalForUser(ctx context.Context, runner idempotencyRunner, scope core.Scope, approvalID string) (core.Approval, error) {
	row := runner.QueryRowContext(ctx, `
		SELECT ar.id, ar.organization_id, ar.project_id, ar.task_id, ar.run_id,
		       ar.step_id, ar.tool_call_id, ar.tool_name, ar.risk_level, ar.status,
		       ar.reason, ar.frozen_params_json, ar.input_hash, ar.requested_by,
		       ar.decided_by, ar.requested_at, ar.expires_at, ar.decided_at
		FROM approval_requests ar
		JOIN tasks t ON t.id = ar.task_id
		JOIN memberships m ON m.organization_id = ar.organization_id
		JOIN project_memberships pm ON pm.project_id = ar.project_id AND pm.user_id = m.user_id
		WHERE ar.id = ? AND ar.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)`,
		approvalID, scope.OrganizationID, scope.UserID, scope.UserID,
	)
	return scanApproval(row)
}

func scanApproval(scanner rowScanner) (core.Approval, error) {
	var approval core.Approval
	var frozenJSON string
	var requestedAt string
	var expiresAt string
	var decidedAt string
	if err := scanner.Scan(
		&approval.ID, &approval.OrganizationID, &approval.ProjectID, &approval.TaskID, &approval.RunID,
		&approval.StepID, &approval.ToolCallID, &approval.ToolName, &approval.RiskLevel, &approval.Status,
		&approval.Reason, &frozenJSON, &approval.InputHash, &approval.RequestedBy,
		&approval.DecidedBy, &requestedAt, &expiresAt, &decidedAt,
	); err != nil {
		return core.Approval{}, err
	}
	if err := json.Unmarshal([]byte(frozenJSON), &approval.FrozenParams); err != nil {
		return core.Approval{}, fmt.Errorf("decode frozen approval parameters: %w", err)
	}
	parsedRequested, err := parseTime(requestedAt)
	if err != nil {
		return core.Approval{}, err
	}
	approval.RequestedAt = parsedRequested
	if expiresAt != "" {
		parsedExpires, err := parseTime(expiresAt)
		if err != nil {
			return core.Approval{}, err
		}
		approval.ExpiresAt = parsedExpires
	} else {
		// Schema 34 adds the column without a dialect-specific backfill. This
		// keeps PostgreSQL and SQLite migrations identical while preserving the
		// same deadline for legacy rows.
		approval.ExpiresAt = parsedRequested.Add(core.DefaultApprovalTTL)
	}
	if decidedAt != "" {
		parsedDecided, err := parseTime(decidedAt)
		if err != nil {
			return core.Approval{}, err
		}
		approval.DecidedAt = &parsedDecided
	}
	return approval, nil
}

func getSideEffectReceiptForUserTx(ctx context.Context, runner idempotencyRunner, scope core.Scope, receiptID string) (core.SideEffectReceipt, error) {
	row := runner.QueryRowContext(ctx, `
		SELECT r.id, r.organization_id, r.project_id, r.task_id, r.run_id,
		       r.step_id, r.tool_call_id, tc.tool_name, r.input_hash, r.idempotent,
		       r.status, r.error_code, r.started_at, r.finished_at
		FROM side_effect_receipts r
		JOIN tool_calls tc ON tc.id = r.tool_call_id
		JOIN tasks t ON t.id = r.task_id
		JOIN memberships m ON m.organization_id = r.organization_id
		JOIN project_memberships pm ON pm.project_id = r.project_id AND pm.user_id = m.user_id
		WHERE r.id = ? AND r.organization_id = ?
		  AND m.user_id = ? AND m.status = 'active' AND pm.status = 'active'
		  AND (t.visibility = 'project' OR t.owner_user_id = ?)`,
		receiptID, scope.OrganizationID, scope.UserID, scope.UserID,
	)
	return scanSideEffectReceipt(row)
}

func scanSideEffectReceipt(scanner rowScanner) (core.SideEffectReceipt, error) {
	var receipt core.SideEffectReceipt
	var idempotent int64
	var startedAt, finishedAt string
	if err := scanner.Scan(
		&receipt.ID, &receipt.OrganizationID, &receipt.ProjectID, &receipt.TaskID, &receipt.RunID,
		&receipt.StepID, &receipt.ToolCallID, &receipt.ToolName, &receipt.InputHash, &idempotent,
		&receipt.Status, &receipt.ErrorCode, &startedAt, &finishedAt,
	); err != nil {
		return core.SideEffectReceipt{}, err
	}
	receipt.Idempotent = idempotent == 1
	parsedStarted, err := parseTime(startedAt)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	receipt.StartedAt = parsedStarted
	if finishedAt != "" {
		parsedFinished, err := parseTime(finishedAt)
		if err != nil {
			return core.SideEffectReceipt{}, err
		}
		receipt.FinishedAt = &parsedFinished
	}
	return receipt, nil
}

func getRunContextTx(ctx context.Context, runner idempotencyRunner, runID string) (core.RunContext, error) {
	var run core.RunContext
	if err := runner.QueryRowContext(ctx, `
		SELECT r.id, r.task_id, t.project_id, r.organization_id, t.owner_user_id,
		       t.conversation_id, t.title, t.goal, t.acceptance, t.mode, t.visibility,
		       r.config_snapshot_json
		FROM runs r JOIN tasks t ON t.id = r.task_id
		WHERE r.id = ?`, runID).Scan(
		&run.RunID, &run.TaskID, &run.ProjectID, &run.OrganizationID, &run.OwnerUserID,
		&run.ConversationID, &run.Title, &run.Goal, &run.Acceptance, &run.Mode, &run.Visibility,
		&run.ConfigSnapshotJSON,
	); err != nil {
		return core.RunContext{}, err
	}
	return run, nil
}

func taskFromFields(id, orgID, projectID, ownerID, runID, conversationID, title, goal, acceptance, mode, visibility, status, errorCode, result, acceptedOutcomeID, created, updated string) (core.Task, error) {
	createdAt, err := parseTime(created)
	if err != nil {
		return core.Task{}, err
	}
	updatedAt, err := parseTime(updated)
	if err != nil {
		return core.Task{}, err
	}
	return core.Task{ID: id, OrganizationID: orgID, ProjectID: projectID, OwnerUserID: ownerID, RunID: runID, ConversationID: conversationID, Title: title, Goal: goal, Acceptance: acceptance, Mode: mode, Visibility: visibility, Status: status, ErrorCode: errorCode, ResultSummary: result, AcceptedOutcomeID: acceptedOutcomeID, CreatedAt: createdAt, UpdatedAt: updatedAt}, nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if bytes, ok := value.([]byte); ok {
		return string(bytes)
	}
	return fmt.Sprint(value)
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: strings.TrimSpace(value) != ""}
}

const (
	httpStatusOK        = 200
	httpStatusCreated   = 201
	httpStatusAccepted  = 202
	httpStatusNoContent = 204
)

type idempotencyRunner interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func replayIdempotency(ctx context.Context, runner idempotencyRunner, scope core.Scope, route string, idempotency core.Idempotency, target any) (bool, error) {
	if idempotency.Key == "" {
		return false, nil
	}
	var requestHash string
	var responseJSON string
	var expiresAt string
	err := runner.QueryRowContext(ctx, `
		SELECT request_hash, response_json, expires_at
		FROM idempotency_records
		WHERE organization_id = ? AND actor_id = ? AND route = ? AND idempotency_key = ?`,
		scope.OrganizationID, scope.UserID, route, idempotency.Key,
	).Scan(&requestHash, &responseJSON, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read idempotency record: %w", err)
	}
	expires, err := parseTime(expiresAt)
	if err != nil {
		return false, err
	}
	if !expires.After(time.Now().UTC()) {
		if _, err := runner.ExecContext(ctx, `
			DELETE FROM idempotency_records
			WHERE organization_id = ? AND actor_id = ? AND route = ? AND idempotency_key = ?`,
			scope.OrganizationID, scope.UserID, route, idempotency.Key,
		); err != nil {
			return false, fmt.Errorf("expire idempotency record: %w", err)
		}
		return false, nil
	}
	if requestHash != idempotency.RequestHash {
		return false, &core.IdempotencyConflictError{}
	}
	if err := json.Unmarshal([]byte(responseJSON), target); err != nil {
		return false, fmt.Errorf("decode idempotency response: %w", err)
	}
	return true, nil
}

func saveIdempotency(ctx context.Context, runner idempotencyRunner, scope core.Scope, route string, idempotency core.Idempotency, responseStatus int, response any) error {
	if idempotency.Key == "" {
		return nil
	}
	body, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("encode idempotency response: %w", err)
	}
	now := time.Now().UTC()
	_, err = runner.ExecContext(ctx, `
		INSERT INTO idempotency_records (
			organization_id, actor_id, route, idempotency_key, request_hash,
			response_status, response_json, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scope.OrganizationID, scope.UserID, route, idempotency.Key, idempotency.RequestHash,
		responseStatus, string(body), formatTime(now.Add(24*time.Hour)), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("save idempotency record: %w", err)
	}
	return nil
}

func newID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate uuidv7: %w", err)
	}
	return id.String(), nil
}

func (s *Store) HasActiveTask(ctx context.Context, scope core.Scope, conversationID string) (bool, string, error) {
	var taskID string
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id
		FROM tasks t
		JOIN runs r ON r.id = (
			SELECT latest.id
			FROM runs latest
			WHERE latest.task_id = t.id
			ORDER BY latest.created_at DESC, latest.id DESC
			LIMIT 1
		)
		WHERE t.conversation_id = ? AND t.organization_id = ?
		  AND r.status IN ('queued', 'running', 'waiting_approval', 'waiting_input', 'paused')
		ORDER BY t.created_at DESC, t.id DESC
		LIMIT 1
	`, conversationID, scope.OrganizationID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, taskID, nil
}

func (s *Store) EnqueueConversationTurn(ctx context.Context, scope core.Scope, projectID, conversationID, activeTaskID, content, inputJSON, kind string) (core.ConversationQueueItem, error) {
	id, err := newID()
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	now := formatTime(time.Now())
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_message_queue (id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, '', '')
	`, id, scope.OrganizationID, projectID, conversationID, activeTaskID, content, inputJSON, kind, now); err != nil {
		return core.ConversationQueueItem{}, fmt.Errorf("enqueue conversation turn: %w", err)
	}
	createdAt, _ := parseTime(now)
	return core.ConversationQueueItem{
		ID: id, OrganizationID: scope.OrganizationID, ProjectID: projectID,
		ConversationID: conversationID, ActiveTaskID: activeTaskID,
		Content: content, InputJSON: inputJSON, Kind: kind, Status: "pending",
		CreatedAt: createdAt,
	}, nil
}

func (s *Store) ListConversationQueue(ctx context.Context, scope core.Scope, conversationID string, status string) ([]core.ConversationQueueItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at
		FROM conversation_message_queue
		WHERE conversation_id = ? AND organization_id = ? AND status = ?
		ORDER BY created_at ASC
	`, conversationID, scope.OrganizationID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []core.ConversationQueueItem
	for rows.Next() {
		var item core.ConversationQueueItem
		var createdAt, claimedAt, consumedAt string
		if err := rows.Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt); err != nil {
			return nil, err
		}
		item.CreatedAt, _ = parseTime(createdAt)
		item.ClaimedAt, _ = parseTime(claimedAt)
		item.ConsumedAt, _ = parseTime(consumedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ClaimSteeringAfterStep(ctx context.Context, scope core.Scope, taskID, conversationID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	now := formatTime(time.Now())
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'claimed', claimed_at = ?
		WHERE id = (
			SELECT id FROM conversation_message_queue
			WHERE conversation_id = ? AND active_task_id = ? AND kind = 'steering' AND status = 'pending'
			ORDER BY created_at ASC LIMIT 1
		)
		RETURNING id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at
	`, now, conversationID, taskID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

// ApplyConversationSteering claims the oldest pending steering message for the
// conversation and, in one transaction, replaces the remaining queued plan with
// a steering replan step. The queue item is consumed only after the replan step
// is durably created; any failure rolls the claim back to pending.
func replaceRunPlanForSteering(ctx context.Context, tx *sql.Tx, run core.RunContext, steering, actorID, now string) (string, string, error) {
	var previousID string
	var previousRevision int64
	err := tx.QueryRowContext(ctx, `SELECT id, revision FROM run_plan_revisions WHERE run_id = ? AND status = 'active' ORDER BY revision DESC LIMIT 1`, run.RunID).Scan(&previousID, &previousRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if previousID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_revisions SET status = 'superseded', superseded_at = ? WHERE id = ?`, now, previousID); err != nil {
			return "", "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE run_plan_items SET status = 'canceled', completed_at = ?, updated_at = ? WHERE plan_revision_id = ? AND status NOT IN ('completed', 'canceled')`, now, now, previousID); err != nil {
			return "", "", err
		}
	}
	revisionID, err := newID()
	if err != nil {
		return "", "", err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO run_plan_revisions (
			id, organization_id, project_id, task_id, run_id, revision, trigger,
			goal_summary, status, supersedes_revision_id, created_by, created_at
		) VALUES (?, ?, ?, ?, ?, ?, 'steering', ?, 'active', ?, ?, ?)`,
		revisionID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID,
		previousRevision+1, truncateText(strings.TrimSpace(steering), 1000), previousID, actorID, now,
	); err != nil {
		return "", "", err
	}
	titles := []string{"正在根据追加要求重新规划"}
	firstItemID := ""
	for index, title := range titles {
		itemID, idErr := newID()
		if idErr != nil {
			return "", "", idErr
		}
		if index == 0 {
			firstItemID = itemID
		}
		status, startedAt := core.RunPlanItemPending, ""
		if index == 0 {
			status, startedAt = core.RunPlanItemRunning, now
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO run_plan_items (
				id, organization_id, project_id, task_id, run_id, plan_revision_id,
				position, title, description, status, started_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, run.OrganizationID, run.ProjectID, run.TaskID, run.RunID, revisionID,
			index+1, title, "模型将保留已完成结果，并生成覆盖合并目标的完整新语义计划。", status, startedAt, now, now,
		); err != nil {
			return "", "", err
		}
	}
	return revisionID, firstItemID, nil
}

func (s *Store) ApplyConversationSteering(ctx context.Context, scope core.Scope, sourceStep core.ClaimedStep, run core.RunContext, correlationID string) (core.ConversationQueueItem, string, error) {
	now := formatTime(time.Now())
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	defer func() { _ = tx.Rollback() }()
	q := s.queries.WithTx(tx)

	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	err = tx.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'claimed', claimed_at = ?
		WHERE id = (
			SELECT id FROM conversation_message_queue
			WHERE conversation_id = ? AND active_task_id = ? AND kind = 'steering' AND status = 'pending'
			ORDER BY created_at ASC LIMIT 1
		)
		RETURNING id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at
	`, now, run.ConversationID, run.TaskID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, "", core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)

	history, err := s.collectContinuationHistory(ctx, tx, run)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}

	// Cancel the remainder of the superseded plan. Finished steps stay as
	// context; anything not started is replaced by the steering replan.
	supersededRows, err := tx.QueryContext(ctx, `SELECT id FROM steps WHERE run_id = ? AND status IN ('queued', 'waiting_approval')`, run.RunID)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	supersededStepIDs := make([]string, 0)
	for supersededRows.Next() {
		var stepID string
		if err := supersededRows.Scan(&stepID); err != nil {
			_ = supersededRows.Close()
			return core.ConversationQueueItem{}, "", err
		}
		supersededStepIDs = append(supersededStepIDs, stepID)
	}
	if err := supersededRows.Close(); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	for _, stepID := range supersededStepIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE steps SET status = 'canceled', error_code = 'PLAN_SUPERSEDED', updated_at = ? WHERE id = ?`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE tool_calls SET status = 'rejected', error_code = 'PLAN_SUPERSEDED', updated_at = ? WHERE step_id = ?`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = 'system', decision_note = '计划已由用户引导重新规划', decided_at = ? WHERE step_id = ? AND status = 'pending'`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE human_action_requests
			SET status = 'superseded', response_json = '{"reason":"steering_replan"}',
			    decided_by = 'system', decided_at = ?, version = version + 1
			WHERE step_id = ? AND status IN ('waiting_user', 'reviewing')`, now, stepID); err != nil {
			return core.ConversationQueueItem{}, "", err
		}
	}

	planRevisionID, planItemID, err := replaceRunPlanForSteering(ctx, tx, run, item.Content, scope.UserID, now)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	replanInput := map[string]any{"history": history, "stage": "steering_replan", "steering": item.Content, "source_step_id": sourceStep.ID}
	replanStepID, err := insertQueuedModelStep(ctx, tx, run, replanInput, now)
	if err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE steps SET plan_revision_id = ?, run_plan_item_id = ? WHERE id = ?`, planRevisionID, planItemID, replanStepID); err != nil {
		return core.ConversationQueueItem{}, "", err
	}

	if err := q.UpdateTaskResult(ctx, sqlitegen.UpdateTaskResultParams{Status: core.StatusRunning, ResultSummary: "", ErrorCode: "", UpdatedAt: now, ID: run.TaskID}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := q.UpdateRunStatus(ctx, sqlitegen.UpdateRunStatusParams{Status: core.StatusRunning, UpdatedAt: now, ID: run.RunID}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}

	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "conversation.steered", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"queue_item_id": item.ID, "task_id": run.TaskID, "content": item.Content, "source_step_id": sourceStep.ID, "replan_step_id": replanStepID}}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "model.replan_queued", ActorType: "system", ActorID: sourceStep.OwnerID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"step_id": replanStepID, "source_step_id": sourceStep.ID, "queue_item_id": item.ID, "history_items": len(history)}}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := s.appendEvent(ctx, q, eventInput{OrganizationID: run.OrganizationID, ProjectID: run.ProjectID, RunID: run.RunID, EventType: "plan.published", ActorType: "user", ActorID: scope.UserID, CorrelationID: correlationID, Visibility: run.Visibility, Payload: map[string]any{"plan_revision_id": planRevisionID, "trigger": "steering", "goal_summary": item.Content}}); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversation_message_queue SET status = 'consumed', consumed_at = ? WHERE id = ? AND status = 'claimed'`, now, item.ID); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return core.ConversationQueueItem{}, "", err
	}
	return item, replanStepID, nil
}

func (s *Store) ClaimNextFollowUp(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	now := formatTime(time.Now())
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'claimed', claimed_at = ?
		WHERE id = (
			SELECT id FROM conversation_message_queue
			WHERE conversation_id = ? AND kind IN ('follow_up', 'steering') AND status = 'pending'
			ORDER BY created_at ASC LIMIT 1
		)
		RETURNING id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at
	`, now, conversationID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

// ClaimNextIdleFollowUp lets an idle worker retry queued turns even when no
// step is executing. It first resets items whose claim went stale (worker crash
// between claim and consume), then claims the oldest pending follow-up whose
// conversation has no active task and no other claimed item. Pending items
// claimed since retryBefore are skipped so a repeatedly failing starter backs
// off instead of hot-looping; claimed_at doubles as the last-attempt marker.
func (s *Store) ClaimNextIdleFollowUp(ctx context.Context, retryBefore time.Time) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	now := formatTime(time.Now())
	stale := formatTime(retryBefore)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'pending', claimed_at = ''
		WHERE status = 'claimed' AND claimed_at < ?`, stale); err != nil {
		return core.ConversationQueueItem{}, err
	}
	err = tx.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'claimed', claimed_at = ?
		WHERE id = (
			SELECT q.id FROM conversation_message_queue q
			WHERE q.status = 'pending'
			  AND (q.claimed_at = '' OR q.claimed_at < ?)
			  AND NOT EXISTS (
			      SELECT 1 FROM conversation_message_queue c
			      WHERE c.conversation_id = q.conversation_id AND c.status = 'claimed'
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM tasks t
			      WHERE t.conversation_id = q.conversation_id
			        AND t.status IN ('queued', 'running', 'waiting_approval', 'waiting_input', 'paused')
			  )
			ORDER BY q.created_at ASC LIMIT 1
		)
		RETURNING id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at
	`, now, stale).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

func (s *Store) MarkConversationQueueItemConsumed(ctx context.Context, scope core.Scope, itemID string) (core.ConversationQueueItem, error) {
	now := formatTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'consumed', consumed_at = ?
		WHERE id = ? AND organization_id = ? AND status = 'claimed'
	`, now, itemID, scope.OrganizationID)
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	return core.ConversationQueueItem{ID: itemID, Status: "consumed"}, nil
}

func (s *Store) PromoteConversationQueueItemToSteering(ctx context.Context, scope core.Scope, conversationID, itemID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET kind = 'steering'
		WHERE id = ? AND conversation_id = ? AND organization_id = ? AND status = 'pending' AND kind = 'follow_up'
		RETURNING id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at
	`, itemID, conversationID, scope.OrganizationID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

// RequeueConversationQueueItem restores a claimed item to pending after a
// failed start attempt. claimed_at keeps the attempt timestamp so idle drains
// back off instead of immediately retrying a failing item.
func (s *Store) RequeueConversationQueueItem(ctx context.Context, scope core.Scope, itemID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'pending', claimed_at = ?
		WHERE id = ? AND organization_id = ? AND status = 'claimed'
	`, formatTime(time.Now()), itemID, scope.OrganizationID)
	return err
}

func (s *Store) CancelConversationQueueItems(ctx context.Context, scope core.Scope, conversationID, taskID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE conversation_message_queue SET status = 'canceled'
		WHERE conversation_id = ? AND active_task_id = ? AND organization_id = ? AND status = 'pending'
	`, conversationID, taskID, scope.OrganizationID)
	return err
}

func (s *Store) CancelConversationQueueItem(ctx context.Context, scope core.Scope, conversationID, itemID string) (core.ConversationQueueItem, error) {
	var item core.ConversationQueueItem
	var createdAt, claimedAt, consumedAt string
	err := s.db.QueryRowContext(ctx, `
		UPDATE conversation_message_queue
		SET status = 'canceled'
		WHERE id = ? AND conversation_id = ? AND organization_id = ? AND status = 'pending'
		RETURNING id, organization_id, project_id, conversation_id, active_task_id, content, input_json, kind, status, created_at, claimed_at, consumed_at
	`, itemID, conversationID, scope.OrganizationID).Scan(&item.ID, &item.OrganizationID, &item.ProjectID, &item.ConversationID, &item.ActiveTaskID, &item.Content, &item.InputJSON, &item.Kind, &item.Status, &createdAt, &claimedAt, &consumedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ConversationQueueItem{}, core.ErrNoWork
	}
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	item.CreatedAt, _ = parseTime(createdAt)
	item.ClaimedAt, _ = parseTime(claimedAt)
	item.ConsumedAt, _ = parseTime(consumedAt)
	return item, nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", value, err)
	}
	return parsed, nil
}

func safeJoin(root, relative string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	targetAbs, err := filepath.Abs(filepath.Join(rootAbs, relative))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("workspace path escapes root")
	}
	return targetAbs, nil
}
