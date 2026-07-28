package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestProviderRegistryMigrationBackfillsSingletonConfiguration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "provider-upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	applyMigrationsThrough(t, ctx, db, 47)
	now := "2026-07-26T00:00:00.000Z"
	if _, err := db.ExecContext(ctx, `INSERT INTO organizations (id, name, created_at, updated_at) VALUES ('org', 'Org', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO model_provider_configs (
    organization_id, provider_kind, endpoint, default_model, secret_ref, created_at, updated_at
) VALUES ('org', 'openai_compatible', 'https://legacy.example.test/v1', 'legacy-model', 'secret/ref', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	applyMigrationsRange(t, ctx, db, 48, 48)
	var providerID, endpoint, modelID, secretRef string
	var isDefault int
	err = db.QueryRowContext(ctx, `
SELECT provider_id, endpoint, default_model_id, credential_ref, is_default
FROM provider_instances WHERE organization_id = 'org'`).Scan(&providerID, &endpoint, &modelID, &secretRef, &isDefault)
	if err != nil {
		t.Fatal(err)
	}
	if providerID != "legacy-default-org" || endpoint != "https://legacy.example.test/v1" ||
		modelID != "legacy-model" || secretRef != "secret/ref" || isDefault != 1 {
		t.Fatalf("backfill provider=%q endpoint=%q model=%q ref=%q default=%d", providerID, endpoint, modelID, secretRef, isDefault)
	}
}
