package postgres

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestPostgresBackupArgsKeepPasswordOutOfProcessArguments(t *testing.T) {
	args, env, err := postgresCLIArgs("postgres://backup-user:secret-value@127.0.0.1:5432/oktapod?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "secret-value") {
		t.Fatalf("password leaked into args: %q", joined)
	}
	found := false
	for _, value := range env {
		if value == "PGPASSWORD=secret-value" {
			found = true
		}
	}
	if !found {
		t.Fatal("PGPASSWORD was not passed to child environment")
	}
}

func TestRewriteSQLPreservesQuotedQuestionMarksAndTranslatesPlaceholders(t *testing.T) {
	got := rewriteSQL("SELECT '?' AS literal, col FROM items WHERE id = ? AND note = \"?\" AND kind = ?")
	want := "SELECT '?' AS literal, col FROM items WHERE id = $1 AND note = \"?\" AND kind = $2"
	if got != want {
		t.Fatalf("rewriteSQL=%q want %q", got, want)
	}
}

func TestRewriteSQLTranslatesInsertIgnoreBeforeReturning(t *testing.T) {
	got := rewriteSQL("INSERT OR IGNORE INTO items (id, value) VALUES (?, ?) RETURNING id")
	want := "INSERT INTO items (id, value) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING id"
	if got != want {
		t.Fatalf("rewriteSQL=%q want %q", got, want)
	}
}

type recordingTx struct {
	queries []string
}

func (r *recordingTx) Commit() error   { return nil }
func (r *recordingTx) Rollback() error { return nil }
func (r *recordingTx) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	r.queries = append(r.queries, query)
	return driver.RowsAffected(1), nil
}

func TestTxWrapperSetsRLSContextBeforeSharedRepositorySQL(t *testing.T) {
	base := &recordingTx{}
	tx := &txWrapper{
		Tx:    base,
		scope: scopeFromContext(application.WithScope(context.Background(), core.Scope{OrganizationID: "org-1", UserID: "user-1"})),
	}
	if _, err := tx.ExecContext(context.Background(), "INSERT OR IGNORE INTO items (id) VALUES (?)", []driver.NamedValue{{Name: "id", Value: "item-1"}}); err != nil {
		t.Fatal(err)
	}
	if len(base.queries) != 2 {
		t.Fatalf("queries=%d want 2", len(base.queries))
	}
	if base.queries[0] != "SELECT set_config('app.org_id', $1, true), set_config('app.user_id', $2, true), set_config('app.bootstrap', $3, true)" {
		t.Fatalf("scope setup query=%q", base.queries[0])
	}
	if got := base.queries[1]; got != "INSERT INTO items (id) VALUES ($1) ON CONFLICT DO NOTHING" {
		t.Fatalf("rewritten tx query=%q", got)
	}
}
