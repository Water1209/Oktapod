package secretstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type memoryVaultRepository struct {
	vault   *core.SecretVaultRecord
	secrets map[string]core.EncryptedSecretRecord
}

func (r *memoryVaultRepository) GetSecretVault(_ context.Context, organizationID string) (core.SecretVaultRecord, error) {
	if r.vault == nil || r.vault.OrganizationID != organizationID {
		return core.SecretVaultRecord{}, sql.ErrNoRows
	}
	return *r.vault, nil
}
func (r *memoryVaultRepository) UpsertSecretVault(_ context.Context, item core.SecretVaultRecord) (core.SecretVaultRecord, error) {
	copy := item
	r.vault = &copy
	return copy, nil
}
func (r *memoryVaultRepository) DeletePendingSecretVault(_ context.Context, _ string) error {
	if r.vault == nil || r.vault.State != core.SecretVaultStatePending {
		return sql.ErrNoRows
	}
	r.vault = nil
	r.secrets = map[string]core.EncryptedSecretRecord{}
	return nil
}
func (r *memoryVaultRepository) GetEncryptedSecret(_ context.Context, organizationID, ref string) (core.EncryptedSecretRecord, error) {
	item, ok := r.secrets[organizationID+"\x00"+ref]
	if !ok {
		return core.EncryptedSecretRecord{}, sql.ErrNoRows
	}
	return item, nil
}
func (r *memoryVaultRepository) UpsertEncryptedSecret(_ context.Context, item core.EncryptedSecretRecord) error {
	if r.secrets == nil {
		r.secrets = map[string]core.EncryptedSecretRecord{}
	}
	r.secrets[item.OrganizationID+"\x00"+item.SecretRef] = item
	return nil
}
func (r *memoryVaultRepository) DeleteEncryptedSecret(_ context.Context, organizationID, ref string) error {
	key := organizationID + "\x00" + ref
	if _, ok := r.secrets[key]; !ok {
		return sql.ErrNoRows
	}
	delete(r.secrets, key)
	return nil
}
func (r *memoryVaultRepository) CountEncryptedSecrets(_ context.Context, _ string) (int64, error) {
	return int64(len(r.secrets)), nil
}

func TestDatabaseVaultLifecycleAndTamperDetection(t *testing.T) {
	ctx := context.Background()
	repository := &memoryVaultRepository{secrets: map[string]core.EncryptedSecretRecord{}}
	root := t.TempDir()
	manager, err := NewVaultKeyManager(ctx, repository, "org-1", root)
	if err != nil {
		t.Fatal(err)
	}
	code, status, err := manager.Initialize(ctx)
	if err != nil || status.State != core.VaultRuntimePending || len(code) < 40 {
		t.Fatalf("status=%+v code=%q err=%v", status, code, err)
	}
	pendingRestart, err := NewVaultKeyManager(ctx, repository, "org-1", root)
	if err != nil || pendingRestart.Status(ctx).State != core.VaultRuntimePending {
		t.Fatalf("pending restart status=%+v err=%v", pendingRestart.Status(ctx), err)
	}
	store := NewDatabase(repository, manager, "org-1")
	if err := store.Set(ctx, "model-provider/org-1/one", "sentinel-secret"); !errors.Is(err, ErrVaultPending) {
		t.Fatalf("pending Set err=%v", err)
	}
	if _, err := manager.Confirm(ctx, "not-the-recovery-code"); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("wrong recovery err=%v", err)
	}
	if status, err = manager.Confirm(ctx, code); err != nil || status.State != core.VaultRuntimeActive {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	ref := "model-provider/org-1/one"
	if err := store.Set(ctx, ref, "sentinel-secret"); err != nil {
		t.Fatal(err)
	}
	if value, err := store.Get(ctx, ref); err != nil || value != "sentinel-secret" {
		t.Fatalf("value=%q err=%v", value, err)
	}
	key := "org-1\x00" + ref
	record := repository.secrets[key]
	record.SecretRef = "model-provider/org-1/two"
	repository.secrets["org-1\x00"+record.SecretRef] = record
	if _, err := store.Get(ctx, record.SecretRef); !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("AAD swap err=%v", err)
	}
	record = repository.secrets[key]
	record.Ciphertext = record.Ciphertext[:len(record.Ciphertext)-2] + "AA"
	repository.secrets[key] = record
	if _, err := store.Get(ctx, ref); !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("tamper err=%v", err)
	}

	// A new manager auto-unlocks from the local bootstrap key.
	repository.secrets[key] = core.EncryptedSecretRecord{}
	restarted, err := NewVaultKeyManager(ctx, repository, "org-1", root)
	if err != nil || restarted.Status(ctx).State != core.VaultRuntimeActive {
		t.Fatalf("restart status=%+v err=%v", restarted.Status(ctx), err)
	}

	// Removing the machine key simulates restoring the database elsewhere.
	if err := removeBootstrapKey(filepath.Join(root, "vault", "bootstrap.key")); err != nil {
		t.Fatal(err)
	}
	locked, err := NewVaultKeyManager(ctx, repository, "org-1", root)
	if err != nil || locked.Status(ctx).State != core.VaultRuntimeLocked {
		t.Fatalf("locked status=%+v err=%v", locked.Status(ctx), err)
	}
	if _, err := locked.Recover(ctx, "wrong"); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("wrong restore err=%v", err)
	}
	if status, err := locked.Recover(ctx, code); err != nil || status.State != core.VaultRuntimeActive {
		t.Fatalf("recovered status=%+v err=%v", status, err)
	}
	newCode, _, err := locked.RotateRecovery(ctx)
	if err != nil || newCode == code {
		t.Fatalf("rotation code=%q err=%v", newCode, err)
	}
	if err := removeBootstrapKey(filepath.Join(root, "vault", "bootstrap.key")); err != nil {
		t.Fatal(err)
	}
	lockedAgain, _ := NewVaultKeyManager(ctx, repository, "org-1", root)
	if _, err := lockedAgain.Recover(ctx, code); !errors.Is(err, ErrRecoveryInvalid) {
		t.Fatalf("old recovery code err=%v", err)
	}
	if _, err := lockedAgain.Recover(ctx, newCode); err != nil {
		t.Fatal(err)
	}
}

func removeBootstrapKey(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
