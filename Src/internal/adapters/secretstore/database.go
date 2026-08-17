package secretstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	vaultAlgorithm  = "aes-256-gcm"
	vaultKeyVersion = 1
	vaultAADSchema  = "oktapod.database-secret/v1"
)

var (
	ErrVaultUninitialized = errors.New("SECRET_VAULT_UNINITIALIZED")
	ErrVaultPending       = errors.New("SECRET_VAULT_PENDING")
	ErrVaultLocked        = errors.New("SECRET_VAULT_LOCKED")
	ErrSecretMissing      = errors.New("SECRET_MISSING")
	ErrCiphertextInvalid  = errors.New("SECRET_CIPHERTEXT_INVALID")
	ErrRecoveryInvalid    = errors.New("SECRET_RECOVERY_INVALID")
)

type VaultRepository interface {
	GetSecretVault(context.Context, string) (core.SecretVaultRecord, error)
	UpsertSecretVault(context.Context, core.SecretVaultRecord) (core.SecretVaultRecord, error)
	DeletePendingSecretVault(context.Context, string) error
	GetEncryptedSecret(context.Context, string, string) (core.EncryptedSecretRecord, error)
	UpsertEncryptedSecret(context.Context, core.EncryptedSecretRecord) error
	DeleteEncryptedSecret(context.Context, string, string) error
	CountEncryptedSecrets(context.Context, string) (int64, error)
}

type VaultKeyManager struct {
	repository     VaultRepository
	organizationID string
	keyPath        string

	mu         sync.RWMutex
	dek        []byte
	runtime    string
	lastDetail string
}

func NewVaultKeyManager(ctx context.Context, repository VaultRepository, organizationID, dataRoot string) (*VaultKeyManager, error) {
	if repository == nil || strings.TrimSpace(organizationID) == "" || strings.TrimSpace(dataRoot) == "" {
		return nil, fmt.Errorf("vault repository, organization, and data root are required")
	}
	manager := &VaultKeyManager{
		repository: repository, organizationID: strings.TrimSpace(organizationID),
		keyPath: filepath.Join(dataRoot, "vault", "bootstrap.key"), runtime: core.VaultRuntimeUninitialized,
	}
	manager.tryAutoUnlock(ctx)
	return manager, nil
}

func (m *VaultKeyManager) Status(ctx context.Context) core.VaultStatus {
	record, err := m.repository.GetSecretVault(ctx, m.organizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.VaultStatus{State: core.VaultRuntimeUninitialized, Detail: "密钥库尚未初始化。"}
	}
	if err != nil {
		return core.VaultStatus{State: core.VaultRuntimeCorrupt, Detail: "无法读取密钥库元数据。"}
	}
	if validateVaultRecord(record) != nil {
		return core.VaultStatus{State: core.VaultRuntimeCorrupt, Algorithm: record.Algorithm, KeyVersion: record.KeyVersion, RecoveryConfigured: record.RecoveryWrappedDEK != "", Detail: "密钥库算法、版本或包装元数据无效。", UpdatedAt: record.UpdatedAt}
	}
	m.mu.RLock()
	runtimeState, detail := m.runtime, m.lastDetail
	m.mu.RUnlock()
	if record.State == core.SecretVaultStatePending {
		runtimeState = core.VaultRuntimePending
		detail = "恢复口令尚未确认，暂不能保存凭据。"
	} else if runtimeState == "" || runtimeState == core.VaultRuntimeUninitialized {
		runtimeState = core.VaultRuntimeLocked
	}
	count, _ := m.repository.CountEncryptedSecrets(ctx, m.organizationID)
	return core.VaultStatus{
		State: runtimeState, Algorithm: record.Algorithm, KeyVersion: record.KeyVersion,
		RecoveryConfigured: record.RecoveryWrappedDEK != "", SecretCount: count,
		Detail: detail, UpdatedAt: record.UpdatedAt,
	}
}

// Initialize creates encrypted key material and returns the one-time recovery
// code. The code is never persisted, logged, or returned by Status.
func (m *VaultKeyManager) Initialize(ctx context.Context) (string, core.VaultStatus, error) {
	if _, err := m.repository.GetSecretVault(ctx, m.organizationID); err == nil {
		return "", m.Status(ctx), fmt.Errorf("secret vault already exists")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", core.VaultStatus{}, err
	}
	dek, err := randomBytes(32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	localKEK, err := randomBytes(32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	recoveryRaw, err := randomBytes(32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	recoveryCode := base64.RawURLEncoding.EncodeToString(recoveryRaw)
	salt, err := randomBytes(32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	recoveryKEK, err := hkdf.Key(sha256.New, recoveryRaw, salt, "oktapod/vault/recovery-kek/v1", 32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	localWrapped, localNonce, err := wrapDEK(localKEK, dek, vaultWrapperAAD(m.organizationID, "local", vaultKeyVersion))
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	recoveryWrapped, recoveryNonce, err := wrapDEK(recoveryKEK, dek, vaultWrapperAAD(m.organizationID, "recovery", vaultKeyVersion))
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	if err := writeBootstrapKey(m.keyPath, localKEK); err != nil {
		return "", core.VaultStatus{}, err
	}
	_, err = m.repository.UpsertSecretVault(ctx, core.SecretVaultRecord{
		OrganizationID: m.organizationID, State: core.SecretVaultStatePending,
		Algorithm: vaultAlgorithm, KeyVersion: vaultKeyVersion,
		LocalWrappedDEK: encode(localWrapped), LocalNonce: encode(localNonce),
		RecoveryWrappedDEK: encode(recoveryWrapped), RecoveryNonce: encode(recoveryNonce), RecoverySalt: encode(salt),
	})
	if err != nil {
		_ = os.Remove(m.keyPath)
		return "", core.VaultStatus{}, err
	}
	m.setRuntime(core.VaultRuntimePending, nil, "恢复口令尚未确认，暂不能保存凭据。")
	return recoveryCode, m.Status(ctx), nil
}

func (m *VaultKeyManager) Confirm(ctx context.Context, recoveryCode string) (core.VaultStatus, error) {
	record, err := m.repository.GetSecretVault(ctx, m.organizationID)
	if err != nil {
		return core.VaultStatus{}, err
	}
	if err := validateVaultRecord(record); err != nil {
		return m.Status(ctx), err
	}
	if record.State != core.SecretVaultStatePending {
		return m.Status(ctx), fmt.Errorf("secret vault is not pending confirmation")
	}
	dek, err := recoverDEK(record, strings.TrimSpace(recoveryCode))
	if err != nil {
		return m.Status(ctx), err
	}
	localKEK, err := os.ReadFile(m.keyPath)
	if err != nil || len(localKEK) != 32 {
		return m.Status(ctx), ErrVaultLocked
	}
	localDEK, err := unwrapRecordDEK(record, localKEK, "local")
	if err != nil || !equalBytes(localDEK, dek) {
		return m.Status(ctx), ErrRecoveryInvalid
	}
	record.State = core.SecretVaultStateActive
	if _, err := m.repository.UpsertSecretVault(ctx, record); err != nil {
		return core.VaultStatus{}, err
	}
	m.setRuntime(core.VaultRuntimeActive, dek, "密钥库已自动解锁。")
	return m.Status(ctx), nil
}

func (m *VaultKeyManager) Recover(ctx context.Context, recoveryCode string) (core.VaultStatus, error) {
	record, err := m.repository.GetSecretVault(ctx, m.organizationID)
	if err != nil {
		return core.VaultStatus{}, err
	}
	if err := validateVaultRecord(record); err != nil {
		return m.Status(ctx), err
	}
	if record.State != core.SecretVaultStateActive {
		return m.Status(ctx), ErrVaultPending
	}
	dek, err := recoverDEK(record, strings.TrimSpace(recoveryCode))
	if err != nil {
		return m.Status(ctx), err
	}
	localKEK, err := randomBytes(32)
	if err != nil {
		return core.VaultStatus{}, err
	}
	wrapped, nonce, err := wrapDEK(localKEK, dek, vaultWrapperAAD(m.organizationID, "local", record.KeyVersion))
	if err != nil {
		return core.VaultStatus{}, err
	}
	previousKey, previousKeyErr := os.ReadFile(m.keyPath)
	if err := writeBootstrapKey(m.keyPath, localKEK); err != nil {
		return core.VaultStatus{}, err
	}
	record.LocalWrappedDEK, record.LocalNonce = encode(wrapped), encode(nonce)
	if _, err := m.repository.UpsertSecretVault(ctx, record); err != nil {
		if previousKeyErr == nil && len(previousKey) == 32 {
			_ = writeBootstrapKey(m.keyPath, previousKey)
		} else {
			_ = os.Remove(m.keyPath)
		}
		return core.VaultStatus{}, err
	}
	m.setRuntime(core.VaultRuntimeActive, dek, "密钥库已恢复并绑定到本机。")
	return m.Status(ctx), nil
}

func (m *VaultKeyManager) RotateRecovery(ctx context.Context) (string, core.VaultStatus, error) {
	record, err := m.repository.GetSecretVault(ctx, m.organizationID)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	if err := validateVaultRecord(record); err != nil {
		return "", m.Status(ctx), err
	}
	dek, err := m.activeDEK()
	if err != nil {
		return "", m.Status(ctx), err
	}
	recoveryRaw, err := randomBytes(32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	salt, err := randomBytes(32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	recoveryKEK, err := hkdf.Key(sha256.New, recoveryRaw, salt, "oktapod/vault/recovery-kek/v1", 32)
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	wrapped, nonce, err := wrapDEK(recoveryKEK, dek, vaultWrapperAAD(m.organizationID, "recovery", record.KeyVersion))
	if err != nil {
		return "", core.VaultStatus{}, err
	}
	record.RecoveryWrappedDEK, record.RecoveryNonce, record.RecoverySalt = encode(wrapped), encode(nonce), encode(salt)
	if _, err := m.repository.UpsertSecretVault(ctx, record); err != nil {
		return "", core.VaultStatus{}, err
	}
	return base64.RawURLEncoding.EncodeToString(recoveryRaw), m.Status(ctx), nil
}

func (m *VaultKeyManager) ResetPending(ctx context.Context) error {
	if err := m.repository.DeletePendingSecretVault(ctx, m.organizationID); err != nil {
		return err
	}
	_ = os.Remove(m.keyPath)
	m.setRuntime(core.VaultRuntimeUninitialized, nil, "密钥库尚未初始化。")
	return nil
}

func (m *VaultKeyManager) tryAutoUnlock(ctx context.Context) {
	record, err := m.repository.GetSecretVault(ctx, m.organizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		m.setRuntime(core.VaultRuntimeCorrupt, nil, "无法读取密钥库元数据。")
		return
	}
	if validateVaultRecord(record) != nil {
		m.setRuntime(core.VaultRuntimeCorrupt, nil, "密钥库算法、版本或包装元数据无效。")
		return
	}
	if record.State == core.SecretVaultStatePending {
		m.setRuntime(core.VaultRuntimePending, nil, "恢复口令尚未确认，暂不能保存凭据。")
		return
	}
	localKEK, err := os.ReadFile(m.keyPath)
	if err != nil || len(localKEK) != 32 {
		m.setRuntime(core.VaultRuntimeLocked, nil, "缺少本机解锁密钥，请使用恢复口令恢复。")
		return
	}
	dek, err := unwrapRecordDEK(record, localKEK, "local")
	if err != nil {
		m.setRuntime(core.VaultRuntimeCorrupt, nil, "本机包装密钥无法解密，请使用恢复口令修复。")
		return
	}
	m.setRuntime(core.VaultRuntimeActive, dek, "密钥库已自动解锁。")
}

func (m *VaultKeyManager) activeDEK() ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.runtime == core.VaultRuntimePending {
		return nil, ErrVaultPending
	}
	if m.runtime == core.VaultRuntimeUninitialized {
		return nil, ErrVaultUninitialized
	}
	if m.runtime != core.VaultRuntimeActive || len(m.dek) != 32 {
		return nil, ErrVaultLocked
	}
	return append([]byte(nil), m.dek...), nil
}

func (m *VaultKeyManager) setRuntime(state string, dek []byte, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtime, m.lastDetail = state, detail
	m.dek = append(m.dek[:0], dek...)
}

type DatabaseSecretStore struct {
	repository     VaultRepository
	keys           *VaultKeyManager
	organizationID string
}

func NewDatabase(repository VaultRepository, keys *VaultKeyManager, organizationID string) *DatabaseSecretStore {
	return &DatabaseSecretStore{repository: repository, keys: keys, organizationID: strings.TrimSpace(organizationID)}
}

func (s *DatabaseSecretStore) Get(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ErrSecretMissing
	}
	dek, err := s.keys.activeDEK()
	if err != nil {
		return "", err
	}
	record, err := s.repository.GetEncryptedSecret(ctx, s.organizationID, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSecretMissing
	}
	if err != nil {
		return "", err
	}
	nonce, err := decode(record.Nonce)
	if err != nil {
		return "", ErrCiphertextInvalid
	}
	ciphertext, err := decode(record.Ciphertext)
	if err != nil {
		return "", ErrCiphertextInvalid
	}
	plaintext, err := openAEAD(dek, nonce, ciphertext, secretAAD(record.OrganizationID, record.SecretRef, record.SecretKind, record.KeyVersion))
	if err != nil || len(plaintext) == 0 {
		return "", ErrCiphertextInvalid
	}
	return string(plaintext), nil
}

func (s *DatabaseSecretStore) Set(ctx context.Context, ref, value string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.TrimSpace(value) == "" {
		return fmt.Errorf("credential reference and value are required")
	}
	dek, err := s.keys.activeDEK()
	if err != nil {
		return err
	}
	kind := secretKind(ref, value)
	nonce, ciphertext, err := sealAEAD(dek, []byte(value), secretAAD(s.organizationID, ref, kind, vaultKeyVersion))
	if err != nil {
		return err
	}
	return s.repository.UpsertEncryptedSecret(ctx, core.EncryptedSecretRecord{
		OrganizationID: s.organizationID, SecretRef: ref, SecretKind: kind,
		KeyVersion: vaultKeyVersion, Nonce: encode(nonce), Ciphertext: encode(ciphertext),
	})
}

func (s *DatabaseSecretStore) Delete(ctx context.Context, ref string) error {
	err := s.repository.DeleteEncryptedSecret(ctx, s.organizationID, strings.TrimSpace(ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (s *DatabaseSecretStore) CredentialStatus(ctx context.Context, ref string) string {
	if strings.TrimSpace(ref) == "" {
		return core.CredentialMissing
	}
	if _, err := s.repository.GetEncryptedSecret(ctx, s.organizationID, strings.TrimSpace(ref)); errors.Is(err, sql.ErrNoRows) {
		return core.CredentialMissing
	} else if err != nil {
		return core.CredentialCorrupt
	}
	_, err := s.Get(ctx, ref)
	switch {
	case err == nil:
		return core.CredentialReady
	case errors.Is(err, ErrVaultLocked), errors.Is(err, ErrVaultPending), errors.Is(err, ErrVaultUninitialized):
		return core.CredentialLocked
	case errors.Is(err, ErrCiphertextInvalid):
		return core.CredentialCorrupt
	default:
		return core.CredentialMissing
	}
}

func secretKind(ref, value string) string {
	if strings.HasPrefix(ref, "mcp-capability/") {
		if strings.Contains(value, `"schema":"oktapod.mcp-oauth/`) {
			return core.SecretKindMCPOAuth
		}
		return core.SecretKindMCP
	}
	return core.SecretKindProvider
}

func secretAAD(organizationID, ref, kind string, version int) []byte {
	return []byte(fmt.Sprintf("%s|org=%s|ref=%s|kind=%s|key=%d", vaultAADSchema, organizationID, ref, kind, version))
}

func vaultWrapperAAD(organizationID, wrapper string, version int) []byte {
	return []byte(fmt.Sprintf("oktapod.vault-wrapper/v1|org=%s|wrapper=%s|key=%d", organizationID, wrapper, version))
}

func validateVaultRecord(record core.SecretVaultRecord) error {
	if record.Algorithm != vaultAlgorithm || record.KeyVersion != vaultKeyVersion ||
		(record.State != core.SecretVaultStatePending && record.State != core.SecretVaultStateActive) ||
		strings.TrimSpace(record.LocalWrappedDEK) == "" || strings.TrimSpace(record.LocalNonce) == "" ||
		strings.TrimSpace(record.RecoveryWrappedDEK) == "" || strings.TrimSpace(record.RecoveryNonce) == "" || strings.TrimSpace(record.RecoverySalt) == "" {
		return ErrCiphertextInvalid
	}
	return nil
}

func recoverDEK(record core.SecretVaultRecord, recoveryCode string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(recoveryCode)
	if err != nil || len(raw) != 32 {
		return nil, ErrRecoveryInvalid
	}
	salt, err := decode(record.RecoverySalt)
	if err != nil {
		return nil, ErrCiphertextInvalid
	}
	kek, err := hkdf.Key(sha256.New, raw, salt, "oktapod/vault/recovery-kek/v1", 32)
	if err != nil {
		return nil, ErrRecoveryInvalid
	}
	dek, err := unwrapRecordDEK(record, kek, "recovery")
	if err != nil {
		return nil, ErrRecoveryInvalid
	}
	return dek, nil
}

func unwrapRecordDEK(record core.SecretVaultRecord, kek []byte, wrapper string) ([]byte, error) {
	wrapped, nonceText := record.LocalWrappedDEK, record.LocalNonce
	if wrapper == "recovery" {
		wrapped, nonceText = record.RecoveryWrappedDEK, record.RecoveryNonce
	}
	nonce, err := decode(nonceText)
	if err != nil {
		return nil, err
	}
	ciphertext, err := decode(wrapped)
	if err != nil {
		return nil, err
	}
	return openAEAD(kek, nonce, ciphertext, vaultWrapperAAD(record.OrganizationID, wrapper, record.KeyVersion))
}

func wrapDEK(kek, dek, aad []byte) ([]byte, []byte, error) {
	nonce, ciphertext, err := sealAEAD(kek, dek, aad)
	return ciphertext, nonce, err
}

func sealAEAD(key, plaintext, aad []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce, err := randomBytes(aead.NonceSize())
	if err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, aad), nil
}

func openAEAD(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, ErrCiphertextInvalid
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrCiphertextInvalid
	}
	return plaintext, nil
}

func randomBytes(size int) ([]byte, error) {
	data := make([]byte, size)
	_, err := rand.Read(data)
	return data, err
}

func encode(value []byte) string { return base64.RawStdEncoding.EncodeToString(value) }
func decode(value string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for index := range left {
		different |= left[index] ^ right[index]
	}
	return different == 0
}
