package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	sqlitemigrations "github.com/oktapod/oktapod/Src/db/migrations/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// Store reuses the repository implementation used by Personal while owning
// the PostgreSQL connection lifecycle and defense-in-depth RLS installation.
// The SQL source remains shared through the generated query package; the
// adapter performs only placeholder/dialect normalization plus transaction
// context setup.
type Store struct {
	*sqlite.Store
	db            *sql.DB
	dsn           string
	workspaceRoot string
}

func Open(ctx context.Context, dsn, workspaceRoot string) (*Store, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse postgres DSN: %w", err)
	}
	connector := connector{inner: stdlib.GetConnector(*config)}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	db.SetConnMaxLifetime(0)
	sqliteStore, err := sqlite.NewExternalStore(db, workspaceRoot)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = sqliteStore.Close()
		return nil, err
	}
	store := &Store{Store: sqliteStore, db: db, dsn: dsn, workspaceRoot: workspaceRoot}
	if err := installRLS(ctx, db); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.Ping(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// Bootstrap runs before a request scope exists. The adapter marks this one
// idempotent initialization transaction explicitly; RLS policies allow this
// narrow path and remain enforced for normal traffic.
func (s *Store) Bootstrap(ctx context.Context, actor core.Actor, organization core.Organization) error {
	return s.Store.Bootstrap(withBootstrapContext(ctx), actor, organization)
}

func (s *Store) GetModelProviderConfig(ctx context.Context, organizationID string) (core.ModelProviderConfig, error) {
	return s.Store.GetModelProviderConfig(withBootstrapContext(ctx), organizationID)
}

func (s *Store) UpsertModelProviderConfig(ctx context.Context, organizationID string, input core.UpsertModelProviderConfigInput) (core.ModelProviderConfig, error) {
	return s.Store.UpsertModelProviderConfig(withBootstrapContext(ctx), organizationID, input)
}

func (s *Store) DeleteModelProviderConfig(ctx context.Context, organizationID string) error {
	return s.Store.DeleteModelProviderConfig(withBootstrapContext(ctx), organizationID)
}

func (s *Store) ListProviderInstances(ctx context.Context, organizationID string) ([]core.ProviderInstance, error) {
	return s.Store.ListProviderInstances(withBootstrapContext(ctx), organizationID)
}

func (s *Store) GetProviderInstance(ctx context.Context, organizationID, providerID string) (core.ProviderInstance, error) {
	return s.Store.GetProviderInstance(withBootstrapContext(ctx), organizationID, providerID)
}

func (s *Store) GetDefaultProviderInstance(ctx context.Context, organizationID string) (core.ProviderInstance, error) {
	return s.Store.GetDefaultProviderInstance(withBootstrapContext(ctx), organizationID)
}

func (s *Store) UpsertProviderInstance(ctx context.Context, organizationID string, input core.UpsertProviderInstanceInput) (core.ProviderInstance, error) {
	return s.Store.UpsertProviderInstance(withBootstrapContext(ctx), organizationID, input)
}

func (s *Store) DeleteProviderInstance(ctx context.Context, organizationID, providerID string) error {
	return s.Store.DeleteProviderInstance(withBootstrapContext(ctx), organizationID, providerID)
}

func (s *Store) ReplaceModelCatalog(ctx context.Context, organizationID, providerID string, entries []core.ModelCatalogEntry) error {
	return s.Store.ReplaceModelCatalog(withBootstrapContext(ctx), organizationID, providerID, entries)
}

func (s *Store) ListModelCatalogEntries(ctx context.Context, organizationID, providerID string) ([]core.ModelCatalogEntry, error) {
	return s.Store.ListModelCatalogEntries(withBootstrapContext(ctx), organizationID, providerID)
}

// Invitation acceptance is intentionally unauthenticated: the one-time token
// is the credential. The repository constrains the transaction to that token
// and target project; mark it as a privileged bootstrap-like transaction so it
// can create the new membership before the user has an established Scope.
func (s *Store) AcceptProjectInvitation(ctx context.Context, token, correlationID string, idempotency core.Idempotency) (core.ProjectInvitation, error) {
	return s.Store.AcceptProjectInvitation(withBootstrapContext(ctx), token, correlationID, idempotency)
}

func (s *Store) Close() error { return s.Store.Close() }

func installRLS(ctx context.Context, db *sql.DB) error {
	const setup = `
CREATE SCHEMA IF NOT EXISTS oktapod;
CREATE OR REPLACE FUNCTION oktapod.rls_org_id() RETURNS text
LANGUAGE sql STABLE AS $$ SELECT NULLIF(current_setting('app.org_id', true), '') $$;
CREATE OR REPLACE FUNCTION oktapod.rls_user_id() RETURNS text
LANGUAGE sql STABLE AS $$ SELECT NULLIF(current_setting('app.user_id', true), '') $$;
CREATE OR REPLACE FUNCTION oktapod.rls_bootstrap() RETURNS boolean
LANGUAGE sql STABLE AS $$ SELECT current_setting('app.bootstrap', true) = '1' $$;
CREATE OR REPLACE FUNCTION oktapod.rls_member(target_org text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT EXISTS (
    SELECT 1 FROM memberships m
    WHERE m.organization_id = target_org
      AND m.user_id = oktapod.rls_user_id()
      AND m.status = 'active'
  )
$$;
DO $$
DECLARE table_name text;
BEGIN
  FOR table_name IN
    SELECT DISTINCT c.table_name
    FROM information_schema.columns c
    WHERE c.table_schema = 'public'
      AND c.column_name = 'organization_id'
      -- These tables participate in unauthenticated login/invitation flows or
      -- are the membership source used to establish a scope. Repository
      -- authorization still filters them explicitly; project/run data below
      -- remains RLS-protected.
      AND c.table_name NOT IN ('auth_sessions', 'memberships', 'project_invitations')
  LOOP
    EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('DROP POLICY IF EXISTS oktapod_org_scope ON public.%I', table_name);
    EXECUTE format($policy$
      CREATE POLICY oktapod_org_scope ON public.%I
      USING (oktapod.rls_bootstrap() OR (organization_id = oktapod.rls_org_id() AND oktapod.rls_member(organization_id)))
      WITH CHECK (oktapod.rls_bootstrap() OR (organization_id = oktapod.rls_org_id() AND oktapod.rls_member(organization_id)))
    $policy$, table_name);
  END LOOP;
END $$;`
	if _, err := db.ExecContext(ctx, setup); err != nil {
		return fmt.Errorf("install postgres RLS policies: %w", err)
	}
	const projectScoped = `
ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.workspaces FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS oktapod_project_scope ON public.workspaces;
CREATE POLICY oktapod_project_scope ON public.workspaces
  USING (oktapod.rls_bootstrap() OR EXISTS (SELECT 1 FROM public.projects p WHERE p.id = project_id AND p.organization_id = oktapod.rls_org_id()))
  WITH CHECK (oktapod.rls_bootstrap() OR EXISTS (SELECT 1 FROM public.projects p WHERE p.id = project_id AND p.organization_id = oktapod.rls_org_id()));
ALTER TABLE public.project_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.project_memberships FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS oktapod_project_scope ON public.project_memberships;
CREATE POLICY oktapod_project_scope ON public.project_memberships
  USING (oktapod.rls_bootstrap() OR EXISTS (SELECT 1 FROM public.projects p WHERE p.id = project_id AND p.organization_id = oktapod.rls_org_id()))
  WITH CHECK (oktapod.rls_bootstrap() OR EXISTS (SELECT 1 FROM public.projects p WHERE p.id = project_id AND p.organization_id = oktapod.rls_org_id()));
ALTER TABLE public.run_event_streams ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.run_event_streams FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS oktapod_project_scope ON public.run_event_streams;
CREATE POLICY oktapod_project_scope ON public.run_event_streams
  USING (oktapod.rls_bootstrap() OR EXISTS (SELECT 1 FROM public.runs r WHERE r.id = run_id AND r.organization_id = oktapod.rls_org_id()))
  WITH CHECK (oktapod.rls_bootstrap() OR EXISTS (SELECT 1 FROM public.runs r WHERE r.id = run_id AND r.organization_id = oktapod.rls_org_id()));`
	if _, err := db.ExecContext(ctx, projectScoped); err != nil {
		return fmt.Errorf("install postgres project RLS policies: %w", err)
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_versions (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("initialize postgres migration ledger: %w", err)
	}
	entries, err := fs.ReadDir(sqlitemigrations.FS, ".")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return err
		}
		var applied int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_versions WHERE version = $1`, version).Scan(&applied); err != nil {
			return err
		}
		if applied > 0 {
			continue
		}
		body, err := fs.ReadFile(sqlitemigrations.FS, name)
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, statement := range splitStatements(string(body)) {
			statement = normalizeMigration(statement)
			if strings.TrimSpace(statement) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("apply postgres migration %s: %w", name, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_versions(version, applied_at) VALUES ($1, CURRENT_TIMESTAMP) ON CONFLICT DO NOTHING`, version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func normalizeMigration(statement string) string {
	statement = strings.TrimSpace(statement)
	// SQLite migrations occasionally place explanatory line comments before
	// an INSERT OR IGNORE statement. Remove only leading comments so the
	// shared dialect rewrite can recognize that statement; comments have no
	// runtime meaning in PostgreSQL.
	for strings.HasPrefix(statement, "--") {
		if newline := strings.IndexByte(statement, '\n'); newline >= 0 {
			statement = strings.TrimSpace(statement[newline+1:])
			continue
		}
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(statement), "PRAGMA ") {
		return ""
	}
	statement = strings.ReplaceAll(statement, "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')", "CURRENT_TIMESTAMP")
	return rewriteSQL(statement)
}

func splitStatements(script string) []string {
	statements := make([]string, 0, 8)
	start := 0
	var quote byte
	for i := 0; i < len(script); i++ {
		ch := script[i]
		if quote != 0 {
			if ch == quote {
				if i+1 < len(script) && script[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		if ch == ';' {
			statements = append(statements, script[start:i])
			start = i + 1
		}
	}
	if tail := strings.TrimSpace(script[start:]); tail != "" {
		statements = append(statements, tail)
	}
	return statements
}

func EnsureWorkspace(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return os.MkdirAll(absolute, 0o700)
}
