package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) GetSecretVault(ctx context.Context, organizationID string) (core.SecretVaultRecord, error) {
	var record core.SecretVaultRecord
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT organization_id, state, algorithm, key_version,
       local_wrapped_dek, local_nonce, recovery_wrapped_dek, recovery_nonce,
       recovery_salt, created_at, updated_at
FROM secret_vaults WHERE organization_id = ?`, strings.TrimSpace(organizationID)).Scan(
		&record.OrganizationID, &record.State, &record.Algorithm, &record.KeyVersion,
		&record.LocalWrappedDEK, &record.LocalNonce, &record.RecoveryWrappedDEK,
		&record.RecoveryNonce, &record.RecoverySalt, &createdAt, &updatedAt,
	)
	if err != nil {
		return core.SecretVaultRecord{}, err
	}
	record.CreatedAt = mustParseTime(createdAt)
	record.UpdatedAt = mustParseTime(updatedAt)
	return record, nil
}

func (s *Store) UpsertSecretVault(ctx context.Context, record core.SecretVaultRecord) (core.SecretVaultRecord, error) {
	if strings.TrimSpace(record.OrganizationID) == "" || record.Algorithm != "aes-256-gcm" || record.KeyVersion < 1 {
		return core.SecretVaultRecord{}, fmt.Errorf("invalid secret vault record")
	}
	now := time.Now().UTC()
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO secret_vaults (
    organization_id, state, algorithm, key_version,
    local_wrapped_dek, local_nonce, recovery_wrapped_dek, recovery_nonce,
    recovery_salt, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(organization_id) DO UPDATE SET
    state = excluded.state,
    algorithm = excluded.algorithm,
    key_version = excluded.key_version,
    local_wrapped_dek = excluded.local_wrapped_dek,
    local_nonce = excluded.local_nonce,
    recovery_wrapped_dek = excluded.recovery_wrapped_dek,
    recovery_nonce = excluded.recovery_nonce,
    recovery_salt = excluded.recovery_salt,
    updated_at = excluded.updated_at`,
		record.OrganizationID, record.State, record.Algorithm, record.KeyVersion,
		record.LocalWrappedDEK, record.LocalNonce, record.RecoveryWrappedDEK,
		record.RecoveryNonce, record.RecoverySalt, formatTime(createdAt), formatTime(now),
	)
	if err != nil {
		return core.SecretVaultRecord{}, fmt.Errorf("save secret vault: %w", err)
	}
	return s.GetSecretVault(ctx, record.OrganizationID)
}

func (s *Store) DeletePendingSecretVault(ctx context.Context, organizationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM secret_vaults WHERE organization_id = ?`, strings.TrimSpace(organizationID)).Scan(&state); err != nil {
		return err
	}
	if state != core.SecretVaultStatePending {
		return fmt.Errorf("only a pending secret vault can be reset")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secret_records WHERE organization_id = ?`, organizationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secret_vaults WHERE organization_id = ? AND state = 'pending'`, organizationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetEncryptedSecret(ctx context.Context, organizationID, secretRef string) (core.EncryptedSecretRecord, error) {
	var record core.EncryptedSecretRecord
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT organization_id, secret_ref, secret_kind, key_version, nonce, ciphertext, created_at, updated_at
FROM secret_records WHERE organization_id = ? AND secret_ref = ?`,
		strings.TrimSpace(organizationID), strings.TrimSpace(secretRef)).Scan(
		&record.OrganizationID, &record.SecretRef, &record.SecretKind, &record.KeyVersion,
		&record.Nonce, &record.Ciphertext, &createdAt, &updatedAt,
	)
	if err != nil {
		return core.EncryptedSecretRecord{}, err
	}
	record.CreatedAt = mustParseTime(createdAt)
	record.UpdatedAt = mustParseTime(updatedAt)
	return record, nil
}

func (s *Store) UpsertEncryptedSecret(ctx context.Context, record core.EncryptedSecretRecord) error {
	now := time.Now().UTC()
	createdAt := record.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO secret_records (
    organization_id, secret_ref, secret_kind, key_version, nonce, ciphertext, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(organization_id, secret_ref) DO UPDATE SET
    secret_kind = excluded.secret_kind,
    key_version = excluded.key_version,
    nonce = excluded.nonce,
    ciphertext = excluded.ciphertext,
    updated_at = excluded.updated_at`,
		record.OrganizationID, record.SecretRef, record.SecretKind, record.KeyVersion,
		record.Nonce, record.Ciphertext, formatTime(createdAt), formatTime(now),
	)
	if err != nil {
		return fmt.Errorf("save encrypted secret: %w", err)
	}
	return nil
}

func (s *Store) DeleteEncryptedSecret(ctx context.Context, organizationID, secretRef string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM secret_records WHERE organization_id = ? AND secret_ref = ?`, strings.TrimSpace(organizationID), strings.TrimSpace(secretRef))
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CountEncryptedSecrets(ctx context.Context, organizationID string) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM secret_records WHERE organization_id = ?`, strings.TrimSpace(organizationID)).Scan(&count)
	return count, err
}

func (s *Store) GetOrganizationRole(ctx context.Context, organizationID, userID string) (string, error) {
	var role string
	err := s.db.QueryRowContext(ctx, `SELECT role FROM memberships WHERE organization_id = ? AND user_id = ? AND status = 'active'`, strings.TrimSpace(organizationID), strings.TrimSpace(userID)).Scan(&role)
	return role, err
}

func (s *Store) ListCredentialReferences(ctx context.Context, organizationID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT credential_ref FROM provider_instances WHERE organization_id = ? AND credential_ref <> ''
UNION
SELECT credential_ref FROM capabilities WHERE organization_id = ? AND kind = 'mcp' AND credential_ref <> ''`, organizationID, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []string{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *Store) ListCredentialTargets(ctx context.Context, scope core.Scope) ([]core.CredentialTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT 'provider', provider_id, display_name, '', credential_ref
FROM provider_instances
WHERE organization_id = ? AND credential_ref <> ''
UNION ALL
SELECT CASE WHEN json_extract(manifest_json, '$.auth.type') = 'oauth' THEN 'mcp_oauth' ELSE 'mcp' END,
       capabilities.id, capabilities.name || ' · ' || projects.name, capabilities.project_id, capabilities.credential_ref
FROM capabilities
JOIN projects ON projects.id = capabilities.project_id
JOIN project_memberships ON project_memberships.project_id = projects.id
                         AND project_memberships.user_id = ?
                         AND project_memberships.status = 'active'
WHERE capabilities.organization_id = ?
  AND projects.organization_id = ?
  AND projects.status = 'active'
  AND capabilities.kind = 'mcp'
  AND capabilities.credential_ref <> ''
  AND ((capabilities.scope_type = 'project' AND capabilities.scope_id = projects.id)
       OR (capabilities.scope_type = 'global' AND capabilities.owner_user_id = ?))
ORDER BY 1, 3`, strings.TrimSpace(scope.OrganizationID), strings.TrimSpace(scope.UserID), strings.TrimSpace(scope.OrganizationID), strings.TrimSpace(scope.OrganizationID), strings.TrimSpace(scope.UserID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []core.CredentialTarget{}
	for rows.Next() {
		var item core.CredentialTarget
		if err := rows.Scan(&item.Kind, &item.ResourceID, &item.Name, &item.ProjectID, &item.CredentialRef); err != nil {
			return nil, err
		}
		targets = append(targets, item)
	}
	return targets, rows.Err()
}
