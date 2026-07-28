package sqlite

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	sqlitemigrations "github.com/oktapod/oktapod/Src/db/migrations/sqlite"
)

func TestSessionTreeV2MigrationBackfillsPublished044Database(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	applyMigrationsThrough(t, ctx, db, 43)
	now := "2026-07-26T00:00:00.000Z"
	for _, statement := range []string{
		`INSERT INTO organizations (id, name, created_at, updated_at) VALUES ('org', 'Org', '` + now + `', '` + now + `')`,
		`INSERT INTO users (id, display_name, created_at, updated_at) VALUES ('user', 'User', '` + now + `', '` + now + `')`,
		`INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at) VALUES ('org', 'user', 'owner', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO projects (id, organization_id, owner_user_id, name, kind, scene, created_at, updated_at) VALUES ('project', 'org', 'user', 'Project', 'personal', 'work', '` + now + `', '` + now + `')`,
		`INSERT INTO project_memberships (project_id, user_id, role, status, created_at, updated_at) VALUES ('project', 'user', 'owner', 'active', '` + now + `', '` + now + `')`,
		`INSERT INTO conversations (id, organization_id, project_id, owner_user_id, title, visibility, next_message_seq, created_at, updated_at) VALUES ('conversation', 'org', 'project', 'user', 'Thread', 'private', 2, '` + now + `', '` + now + `')`,
		`INSERT INTO messages (id, organization_id, project_id, conversation_id, owner_user_id, server_seq, role, visibility, content, created_at) VALUES ('message-1', 'org', 'project', 'conversation', 'user', 1, 'user', 'private', 'question', '` + now + `')`,
		`INSERT INTO messages (id, organization_id, project_id, conversation_id, owner_user_id, server_seq, role, visibility, content, created_at) VALUES ('message-2', 'org', 'project', 'conversation', 'user', 2, 'assistant', 'private', 'answer', '` + now + `')`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seed legacy database: %v\n%s", err, statement)
		}
	}
	applyMigrationsRange(t, ctx, db, 44, 46)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO conversation_compactions (
			id, organization_id, project_id, conversation_id, owner_user_id,
			summary, compacted_through_seq, retained_from_seq,
			source_message_count, source_tokens, trigger, created_at
		) VALUES ('compact-1', 'org', 'project', 'conversation', 'user',
		          'legacy summary', 1, 2, 1, 10, 'manual', ?)`, now); err != nil {
		t.Fatal(err)
	}
	store, err := NewExternalStore(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var entryCount, minSeq, maxSeq int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(entry_seq), MAX(entry_seq) FROM session_entries WHERE conversation_id = 'conversation'`).Scan(&entryCount, &minSeq, &maxSeq); err != nil {
		t.Fatal(err)
	}
	if entryCount != 2 || minSeq != 1 || maxSeq != 2 {
		t.Fatalf("entry backfill count=%d range=%d..%d", entryCount, minSeq, maxSeq)
	}
	var activeLeaf string
	var nextSeq int64
	if err := db.QueryRowContext(ctx, `SELECT active_leaf_entry_id, next_entry_seq FROM conversations WHERE id = 'conversation'`).Scan(&activeLeaf, &nextSeq); err != nil {
		t.Fatal(err)
	}
	if activeLeaf != "entry-message-2" || nextSeq != 3 {
		t.Fatalf("leaf=%q next=%d", activeLeaf, nextSeq)
	}
	var splitMessage string
	var splitRunes int64
	if err := db.QueryRowContext(ctx, `SELECT split_message_id, split_prefix_runes FROM conversation_compactions WHERE id = 'compact-1'`).Scan(&splitMessage, &splitRunes); err != nil {
		t.Fatal(err)
	}
	if splitMessage != "" || splitRunes != 0 {
		t.Fatalf("legacy compaction split=%q/%d", splitMessage, splitRunes)
	}
}

func applyMigrationsThrough(t *testing.T, ctx context.Context, db *sql.DB, maximum int64) {
	t.Helper()
	applyMigrationsRange(t, ctx, db, 1, maximum)
}

func applyMigrationsRange(t *testing.T, ctx context.Context, db *sql.DB, minimum, maximum int64) {
	t.Helper()
	entries, err := fs.ReadDir(sqlitemigrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		version, parseErr := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if parseErr == nil && version >= minimum && version <= maximum {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version, _ := strconv.ParseInt(strings.SplitN(name, "_", 2)[0], 10, 64)
		body, err := fs.ReadFile(sqlitemigrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_versions(version, applied_at) VALUES (?, '2026-07-26T00:00:00.000Z')`, version); err != nil {
			t.Fatalf("record %s: %v", name, err)
		}
	}
}
