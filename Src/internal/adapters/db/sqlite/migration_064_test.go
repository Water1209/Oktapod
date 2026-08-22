package sqlite

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRuntimePerformanceV2MigrationAddsOnlyAdjunctTables(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:runtime-performance-v2-migration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	applyMigrationsThrough(t, ctx, db, 64)

	for _, table := range []string{
		"task_presentations", "model_call_performance", "tool_batch_schedulers",
		"tool_call_barriers", "run_context_checkpoints",
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing adjunct table %s", table)
		}
	}
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_versions`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 64 {
		t.Fatalf("schema version=%d, want 64", version)
	}
}
