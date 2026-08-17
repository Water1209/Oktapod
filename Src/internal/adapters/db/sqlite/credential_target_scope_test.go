package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestListCredentialTargetsUsesCurrentPrincipalProjectScope(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ownerID := "00000000-0000-7000-8000-000000000001"
	otherID := "00000000-0000-7000-8000-000000000003"
	organizationID := "00000000-0000-7000-8000-000000000002"
	if err := store.Bootstrap(ctx, core.Actor{ID: ownerID, DisplayName: "Owner"}, core.Organization{ID: organizationID, Name: "Org"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, display_name, created_at, updated_at) VALUES (?, 'Other', ?, ?)`, []any{otherID, now, now}},
		{`INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)`, []any{organizationID, otherID, now, now}},
		{`INSERT INTO projects (id, organization_id, owner_user_id, name, kind, scene, status, created_at, updated_at) VALUES ('project-visible', ?, ?, 'Visible', 'personal', 'work', 'active', ?, ?)`, []any{organizationID, ownerID, now, now}},
		{`INSERT INTO projects (id, organization_id, owner_user_id, name, kind, scene, status, created_at, updated_at) VALUES ('project-hidden', ?, ?, 'Hidden', 'personal', 'work', 'active', ?, ?)`, []any{organizationID, otherID, now, now}},
		{`INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at) VALUES ('project-visible', ?, 'owner', 'active', ?, ?)`, []any{ownerID, now, now}},
		{`INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at) VALUES ('project-hidden', ?, 'owner', 'active', ?, ?)`, []any{otherID, now, now}},
		{`INSERT INTO capabilities (id, organization_id, project_id, owner_user_id, kind, name, version, manifest_json, content_hash, status, risk_level, scope_type, scope_id, credential_ref, created_at, updated_at) VALUES ('mcp-visible', ?, 'project-visible', ?, 'mcp', 'Visible MCP', '1', '{}', 'hash-visible', 'enabled', 'L1', 'project', 'project-visible', 'mcp/visible', ?, ?)`, []any{organizationID, ownerID, now, now}},
		{`INSERT INTO capabilities (id, organization_id, project_id, owner_user_id, kind, name, version, manifest_json, content_hash, status, risk_level, scope_type, scope_id, credential_ref, created_at, updated_at) VALUES ('mcp-hidden', ?, 'project-hidden', ?, 'mcp', 'Hidden MCP', '1', '{}', 'hash-hidden', 'enabled', 'L1', 'project', 'project-hidden', 'mcp/hidden', ?, ?)`, []any{organizationID, otherID, now, now}},
	}
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	targets, err := store.ListCredentialTargets(ctx, core.Scope{OrganizationID: organizationID, UserID: ownerID})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ResourceID != "mcp-visible" || targets[0].ProjectID != "project-visible" {
		t.Fatalf("targets=%+v", targets)
	}
}
