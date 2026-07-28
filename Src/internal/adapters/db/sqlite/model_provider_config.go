package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) GetModelProviderConfig(ctx context.Context, organizationID string) (core.ModelProviderConfig, error) {
	if provider, err := s.GetDefaultProviderInstance(ctx, organizationID); err == nil {
		return core.ModelProviderConfig{
			OrganizationID: provider.OrganizationID,
			ProviderKind:   provider.ProtocolFamily,
			Endpoint:       provider.Endpoint,
			DefaultModel:   provider.DefaultModelID,
			SecretRef:      provider.CredentialRef,
			CreatedAt:      provider.CreatedAt,
			UpdatedAt:      provider.UpdatedAt,
		}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return core.ModelProviderConfig{}, err
	}
	var config core.ModelProviderConfig
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT organization_id, provider_kind, endpoint, default_model, secret_ref, created_at, updated_at
FROM model_provider_configs
WHERE organization_id = ?`, strings.TrimSpace(organizationID)).Scan(
		&config.OrganizationID, &config.ProviderKind, &config.Endpoint, &config.DefaultModel,
		&config.SecretRef, &createdAt, &updatedAt,
	)
	if err != nil {
		return core.ModelProviderConfig{}, err
	}
	config.CreatedAt = mustParseTime(createdAt)
	config.UpdatedAt = mustParseTime(updatedAt)
	return config, nil
}

func (s *Store) UpsertModelProviderConfig(ctx context.Context, organizationID string, input core.UpsertModelProviderConfigInput) (core.ModelProviderConfig, error) {
	organizationID = strings.TrimSpace(organizationID)
	input.Endpoint = strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	input.DefaultModel = strings.TrimSpace(input.DefaultModel)
	input.SecretRef = strings.TrimSpace(input.SecretRef)
	if organizationID == "" || input.Endpoint == "" || input.DefaultModel == "" || input.SecretRef == "" {
		return core.ModelProviderConfig{}, fmt.Errorf("model provider organization, endpoint, model, and secret reference are required")
	}
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `
INSERT INTO model_provider_configs (
    organization_id, provider_kind, endpoint, default_model, secret_ref, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(organization_id) DO UPDATE SET
    provider_kind = excluded.provider_kind,
    endpoint = excluded.endpoint,
    default_model = excluded.default_model,
    secret_ref = excluded.secret_ref,
    updated_at = excluded.updated_at`,
		organizationID, core.ModelProviderOpenAICompatible, input.Endpoint, input.DefaultModel, input.SecretRef, now, now,
	)
	if err != nil {
		return core.ModelProviderConfig{}, fmt.Errorf("save model provider config: %w", err)
	}
	_, err = s.UpsertProviderInstance(ctx, organizationID, core.UpsertProviderInstanceInput{
		ProviderID:     "legacy-default-" + organizationID,
		DisplayName:    "Default Provider",
		ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint:       input.Endpoint,
		CredentialRef:  input.SecretRef,
		DefaultModelID: input.DefaultModel,
		Status:         core.ProviderStatusActive,
		IsDefault:      true,
	})
	if err != nil {
		return core.ModelProviderConfig{}, fmt.Errorf("sync default provider registry: %w", err)
	}
	return s.GetModelProviderConfig(ctx, organizationID)
}

func (s *Store) DeleteModelProviderConfig(ctx context.Context, organizationID string) error {
	providerDeleted := false
	if provider, providerErr := s.GetDefaultProviderInstance(ctx, organizationID); providerErr == nil {
		if deleteErr := s.DeleteProviderInstance(ctx, organizationID, provider.ProviderID); deleteErr != nil {
			return deleteErr
		}
		providerDeleted = true
	} else if !errors.Is(providerErr, sql.ErrNoRows) {
		return providerErr
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM model_provider_configs WHERE organization_id = ?`, strings.TrimSpace(organizationID))
	if err != nil {
		return fmt.Errorf("delete model provider config: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		if providerDeleted {
			return nil
		}
		return sql.ErrNoRows
	}
	return nil
}
