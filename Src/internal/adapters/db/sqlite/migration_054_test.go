package sqlite

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestToolContractSnapshotMigrationAddsMCPVersioning(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/migration-054.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	applyMigrationsThrough(t, ctx, db, 53)
	applyMigrationsRange(t, ctx, db, 54, 54)

	columns := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(mcp_connections)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"schema_hash", "config_version", "health_state"} {
		if !columns[name] {
			t.Fatalf("mcp_connections is missing %q after migration 54", name)
		}
	}

	var tableName string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'run_tool_snapshots'`,
	).Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	if tableName != "run_tool_snapshots" {
		t.Fatalf("unexpected snapshot table name %q", tableName)
	}

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 54 {
		t.Fatalf("schema version = %d, want 54", version)
	}
}
