package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) ListProviderInstances(ctx context.Context, organizationID string) ([]core.ProviderInstance, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT provider_id, organization_id, display_name, protocol_family, endpoint,
       credential_ref, default_model_id, status, is_default, created_at, updated_at
FROM provider_instances
WHERE organization_id = ?
ORDER BY is_default DESC, updated_at DESC, display_name ASC`, strings.TrimSpace(organizationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ProviderInstance{}
	for rows.Next() {
		item, scanErr := scanProviderInstance(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetProviderInstance(ctx context.Context, organizationID, providerID string) (core.ProviderInstance, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT provider_id, organization_id, display_name, protocol_family, endpoint,
       credential_ref, default_model_id, status, is_default, created_at, updated_at
FROM provider_instances
WHERE organization_id = ? AND provider_id = ?`,
		strings.TrimSpace(organizationID), strings.TrimSpace(providerID))
	return scanProviderInstance(row)
}

func (s *Store) GetDefaultProviderInstance(ctx context.Context, organizationID string) (core.ProviderInstance, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT provider_id, organization_id, display_name, protocol_family, endpoint,
       credential_ref, default_model_id, status, is_default, created_at, updated_at
FROM provider_instances
WHERE organization_id = ? AND is_default = 1
ORDER BY updated_at DESC
LIMIT 1`, strings.TrimSpace(organizationID))
	return scanProviderInstance(row)
}

type providerScanner interface {
	Scan(...any) error
}

func scanProviderInstance(row providerScanner) (core.ProviderInstance, error) {
	var item core.ProviderInstance
	var isDefault int
	var createdAt, updatedAt string
	err := row.Scan(
		&item.ProviderID, &item.OrganizationID, &item.DisplayName, &item.ProtocolFamily,
		&item.Endpoint, &item.CredentialRef, &item.DefaultModelID, &item.Status,
		&isDefault, &createdAt, &updatedAt,
	)
	if err != nil {
		return core.ProviderInstance{}, err
	}
	item.IsDefault = isDefault == 1
	item.CreatedAt = mustParseTime(createdAt)
	item.UpdatedAt = mustParseTime(updatedAt)
	return item, nil
}

func (s *Store) UpsertProviderInstance(ctx context.Context, organizationID string, input core.UpsertProviderInstanceInput) (core.ProviderInstance, error) {
	organizationID = strings.TrimSpace(organizationID)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.ProtocolFamily = strings.TrimSpace(input.ProtocolFamily)
	input.Endpoint = strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	input.CredentialRef = strings.TrimSpace(input.CredentialRef)
	input.DefaultModelID = strings.TrimSpace(input.DefaultModelID)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = core.ProviderStatusActive
	}
	if organizationID == "" || input.ProviderID == "" || input.DisplayName == "" || input.ProtocolFamily == "" || input.Endpoint == "" || input.CredentialRef == "" {
		return core.ProviderInstance{}, fmt.Errorf("provider organization, id, name, protocol, endpoint, and credential reference are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.ProviderInstance{}, err
	}
	defer tx.Rollback()
	if input.IsDefault {
		if _, err = tx.ExecContext(ctx, `UPDATE provider_instances SET is_default = 0 WHERE organization_id = ? AND provider_id <> ?`, organizationID, input.ProviderID); err != nil {
			return core.ProviderInstance{}, err
		}
	}
	now := formatTime(time.Now().UTC())
	_, err = tx.ExecContext(ctx, `
INSERT INTO provider_instances (
    provider_id, organization_id, display_name, protocol_family, endpoint,
    credential_ref, default_model_id, status, is_default, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(provider_id) DO UPDATE SET
    display_name = excluded.display_name,
    protocol_family = excluded.protocol_family,
    endpoint = excluded.endpoint,
    credential_ref = excluded.credential_ref,
    default_model_id = excluded.default_model_id,
    status = excluded.status,
    is_default = excluded.is_default,
    updated_at = excluded.updated_at
WHERE provider_instances.organization_id = excluded.organization_id`,
		input.ProviderID, organizationID, input.DisplayName, input.ProtocolFamily, input.Endpoint,
		input.CredentialRef, input.DefaultModelID, input.Status, boolToInt(input.IsDefault), now, now)
	if err != nil {
		return core.ProviderInstance{}, fmt.Errorf("save provider instance: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return core.ProviderInstance{}, err
	}
	return s.GetProviderInstance(ctx, organizationID, input.ProviderID)
}

func (s *Store) DeleteProviderInstance(ctx context.Context, organizationID, providerID string) error {
	organizationID = strings.TrimSpace(organizationID)
	providerID = strings.TrimSpace(providerID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `
DELETE FROM model_provider_configs
WHERE organization_id = ?
  AND secret_ref = (
      SELECT credential_ref FROM provider_instances
      WHERE organization_id = ? AND provider_id = ?
  )`, organizationID, organizationID, providerID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM provider_instances WHERE organization_id = ? AND provider_id = ?`,
		organizationID, providerID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) ReplaceModelCatalog(ctx context.Context, organizationID, providerID string, entries []core.ModelCatalogEntry) error {
	if _, err := s.GetProviderInstance(ctx, organizationID, providerID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM model_catalog_entries WHERE provider_id = ?`, providerID); err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO model_catalog_entries (
    provider_id, model_id, capabilities_json, context_window, max_output,
    thinking_map_json, pricing_json, fetched_at, expires_at, etag, last_error, catalog_version
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			providerID, entry.ModelID, defaultJSON(entry.CapabilitiesJSON), entry.ContextWindow,
			entry.MaxOutput, defaultJSON(entry.ThinkingMapJSON), defaultJSON(entry.PricingJSON),
			formatTime(entry.FetchedAt), formatTime(entry.ExpiresAt), entry.ETag,
			sanitizeProviderError(entry.LastError), entry.CatalogVersion); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListModelCatalogEntries(ctx context.Context, organizationID, providerID string) ([]core.ModelCatalogEntry, error) {
	if _, err := s.GetProviderInstance(ctx, organizationID, providerID); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT provider_id, model_id, capabilities_json, context_window, max_output,
       thinking_map_json, pricing_json, fetched_at, expires_at, etag, last_error, catalog_version
FROM model_catalog_entries WHERE provider_id = ? ORDER BY model_id`, providerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now().UTC()
	items := []core.ModelCatalogEntry{}
	for rows.Next() {
		var item core.ModelCatalogEntry
		var fetchedAt, expiresAt string
		if err := rows.Scan(&item.ProviderID, &item.ModelID, &item.CapabilitiesJSON,
			&item.ContextWindow, &item.MaxOutput, &item.ThinkingMapJSON, &item.PricingJSON,
			&fetchedAt, &expiresAt, &item.ETag, &item.LastError, &item.CatalogVersion); err != nil {
			return nil, err
		}
		item.FetchedAt = mustParseTime(fetchedAt)
		item.ExpiresAt = mustParseTime(expiresAt)
		item.Stale = !item.ExpiresAt.After(now)
		items = append(items, item)
	}
	return items, rows.Err()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func defaultJSON(value string) string {
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return value
}

func sanitizeProviderError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
