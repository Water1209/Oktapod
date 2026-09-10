package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestRegistrationWaitsForWriterBeforeReadingOwnerCount(t *testing.T) {
	root := t.TempDir()
	store, err := Open(t.Context(), filepath.Join(root, "auth.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actorID, _ := newID()
	orgID, _ := newID()
	userID, _ := newID()
	if err := store.Bootstrap(t.Context(), core.Actor{ID: actorID, DisplayName: "Local"}, core.Organization{ID: orgID, Name: "Isolated auth"}); err != nil {
		t.Fatal(err)
	}
	password, _ := newID()
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := store.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.ExecContext(t.Context(), `UPDATE organizations SET name = name WHERE id = ?`, orgID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- store.registerLocalUserTx(t.Context(), orgID, userID, core.LocalRegisterInput{Username: "first.login", DisplayName: "First"}, hash, formatTime(time.Now()))
	}()
	select {
	case err := <-done:
		t.Fatalf("registration must wait for the existing writer: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not continue after the writer committed")
	}
	var role string
	if err := store.db.QueryRowContext(t.Context(), `SELECT role FROM memberships WHERE organization_id = ? AND user_id = ?`, orgID, userID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != "owner" {
		t.Fatalf("first login role=%q", role)
	}
}

func TestConfigureLocalPasswordPersistsBootstrapHashOnlyOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000001", DisplayName: "本地用户"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000002", Name: "个人空间"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}
	const bootstrapTestPassword = "test-only-bootstrap-password"
	if err := store.ConfigureLocalPassword(ctx, organization.ID, actor.ID, bootstrapTestPassword); err != nil {
		t.Fatal(err)
	}

	var username, passwordHash string
	if err := store.db.QueryRowContext(ctx, `SELECT username, password_hash FROM users WHERE id = ?`, actor.ID).Scan(&username, &passwordHash); err != nil {
		t.Fatal(err)
	}
	if username != "admin" {
		t.Fatalf("username=%q", username)
	}
	if passwordHash == bootstrapTestPassword || !strings.HasPrefix(passwordHash, "pbkdf2-sha256$") {
		t.Fatalf("password was not stored as a PBKDF2 hash: %q", passwordHash)
	}
	if _, err := store.AuthenticateLocal(ctx, organization.ID, core.LocalLoginInput{Username: "admin", Password: bootstrapTestPassword}); err != nil {
		t.Fatalf("authenticate initialized account: %v", err)
	}

	if err := store.ConfigureLocalPassword(ctx, organization.ID, actor.ID, "replacement-password"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("second initialization error=%v", err)
	}
	if _, err := store.AuthenticateLocal(ctx, organization.ID, core.LocalLoginInput{Username: "admin", Password: bootstrapTestPassword}); err != nil {
		t.Fatalf("bootstrap password was unexpectedly replaced: %v", err)
	}
}
