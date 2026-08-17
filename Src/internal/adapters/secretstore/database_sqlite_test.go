package secretstore_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/secretstore"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestSQLiteVaultNeverPersistsPlaintextSentinel(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "oktapod.db")
	store, err := sqlite.Open(ctx, databasePath, filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	organizationID := "00000000-0000-7000-8000-000000000002"
	if err := store.Bootstrap(ctx, core.Actor{ID: "00000000-0000-7000-8000-000000000001", DisplayName: "User"}, core.Organization{ID: organizationID, Name: "Org"}); err != nil {
		t.Fatal(err)
	}
	manager, err := secretstore.NewVaultKeyManager(ctx, store, organizationID, root)
	if err != nil {
		t.Fatal(err)
	}
	recoveryCode, _, err := manager.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Confirm(ctx, recoveryCode); err != nil {
		t.Fatal(err)
	}
	secrets := secretstore.NewDatabase(store, manager, organizationID)
	values := map[string]string{
		"model-provider/org/sentinel": "OKTAPOD-PLAINTEXT-SENTINEL-provider-9f6fe990",
		"mcp-capability/org/sentinel": "{\"schema\":\"oktapod.mcp-secrets/v1\",\"values\":{\"token\":\"OKTAPOD-PLAINTEXT-SENTINEL-mcp-9f6fe990\"}}",
		"mcp-capability/org/oauth":    "{\"schema\":\"oktapod.mcp-oauth/v1\",\"access_token\":\"OKTAPOD-PLAINTEXT-SENTINEL-oauth-9f6fe990\"}",
	}
	for ref, value := range values {
		if err := secrets.Set(ctx, ref, value); err != nil {
			t.Fatal(err)
		}
		if got, err := secrets.Get(ctx, ref); err != nil || got != value {
			t.Fatalf("ref=%s got=%q err=%v", ref, got, err)
		}
	}
	if record, err := store.GetEncryptedSecret(ctx, organizationID, "mcp-capability/org/oauth"); err != nil || record.SecretKind != core.SecretKindMCPOAuth {
		t.Fatalf("OAuth secret kind=%q err=%v", record.SecretKind, err)
	}
	if err := secrets.Set(ctx, "model-provider/org/sentinel", "OKTAPOD-PLAINTEXT-SENTINEL-provider-updated"); err != nil {
		t.Fatal(err)
	}
	values["model-provider/org/sentinel"] = "OKTAPOD-PLAINTEXT-SENTINEL-provider-updated"
	if err := secrets.Delete(ctx, "mcp-capability/org/sentinel"); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(ctx, "mcp-capability/org/sentinel"); !errors.Is(err, secretstore.ErrSecretMissing) {
		t.Fatalf("deleted MCP secret err=%v", err)
	}
	delete(values, "mcp-capability/org/sentinel")

	backupRoot := filepath.Join(root, "backup")
	manifest, err := store.Backup(ctx, backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != 2 || manifest.Vault == nil || !manifest.Vault.RecoveryConfigured || manifest.Vault.SecretCount != 2 {
		t.Fatalf("backup manifest=%+v", manifest)
	}
	paths := []string{databasePath, databasePath + "-wal", filepath.Join(backupRoot, "database.sqlite"), filepath.Join(backupRoot, "manifest.json")}
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil && os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, sentinel := range values {
			if bytes.Contains(data, []byte(sentinel)) {
				t.Fatalf("plaintext secret found in %s", filepath.Base(path))
			}
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restoredRoot := filepath.Join(root, "restored")
	restoredDatabase := filepath.Join(restoredRoot, "oktapod.db")
	if _, err := sqlite.Restore(ctx, backupRoot, restoredDatabase, filepath.Join(restoredRoot, "workspaces")); err != nil {
		t.Fatal(err)
	}
	restoredStore, err := sqlite.Open(ctx, restoredDatabase, filepath.Join(restoredRoot, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer restoredStore.Close()
	restoredManager, err := secretstore.NewVaultKeyManager(ctx, restoredStore, organizationID, restoredRoot)
	if err != nil || restoredManager.Status(ctx).State != core.VaultRuntimeLocked {
		t.Fatalf("restored vault status=%+v err=%v", restoredManager.Status(ctx), err)
	}
	if _, err := restoredManager.Recover(ctx, recoveryCode); err != nil {
		t.Fatal(err)
	}
	restoredSecrets := secretstore.NewDatabase(restoredStore, restoredManager, organizationID)
	for ref, want := range values {
		if got, err := restoredSecrets.Get(ctx, ref); err != nil || got != want {
			t.Fatalf("restored ref=%s got=%q err=%v", ref, got, err)
		}
	}
}
