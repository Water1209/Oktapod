package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// GetModelProviderConfig remains as a narrow compatibility port for the model
// gateway. provider_instances is the only durable source of truth.
func (s *Store) GetModelProviderConfig(ctx context.Context, organizationID string) (core.ModelProviderConfig, error) {
	provider, err := s.GetDefaultProviderInstance(ctx, organizationID)
	if err != nil {
		return core.ModelProviderConfig{}, err
	}
	return core.ModelProviderConfig{
		OrganizationID: provider.OrganizationID, ProviderKind: provider.ProtocolFamily,
		Endpoint: provider.Endpoint, DefaultModel: provider.DefaultModelID,
		SecretRef: provider.CredentialRef, CreatedAt: provider.CreatedAt, UpdatedAt: provider.UpdatedAt,
	}, nil
}

// UpsertModelProviderConfig maps the legacy CLI port directly onto the
// provider registry; it never recreates model_provider_configs.
func (s *Store) UpsertModelProviderConfig(ctx context.Context, organizationID string, input core.UpsertModelProviderConfigInput) (core.ModelProviderConfig, error) {
	organizationID = strings.TrimSpace(organizationID)
	input.Endpoint = strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	input.DefaultModel = strings.TrimSpace(input.DefaultModel)
	input.SecretRef = strings.TrimSpace(input.SecretRef)
	if organizationID == "" || input.Endpoint == "" || input.DefaultModel == "" || input.SecretRef == "" {
		return core.ModelProviderConfig{}, fmt.Errorf("model provider organization, endpoint, model, and secret reference are required")
	}
	providerID := "default-" + organizationID
	displayName := "Default Provider"
	if existing, err := s.GetDefaultProviderInstance(ctx, organizationID); err == nil {
		providerID, displayName = existing.ProviderID, existing.DisplayName
	} else if err != nil && err != sql.ErrNoRows {
		return core.ModelProviderConfig{}, err
	}
	provider, err := s.UpsertProviderInstance(ctx, organizationID, core.UpsertProviderInstanceInput{
		ProviderID: providerID, DisplayName: displayName,
		ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint:       input.Endpoint, CredentialRef: input.SecretRef, DefaultModelID: input.DefaultModel,
		Status: core.ProviderStatusActive, IsDefault: true,
	})
	if err != nil {
		return core.ModelProviderConfig{}, fmt.Errorf("save default provider: %w", err)
	}
	if err := s.ReplaceEnabledModels(ctx, organizationID, provider.ProviderID, []string{input.DefaultModel}, input.DefaultModel); err != nil {
		return core.ModelProviderConfig{}, fmt.Errorf("save default provider model: %w", err)
	}
	return s.GetModelProviderConfig(ctx, organizationID)
}

func (s *Store) DeleteModelProviderConfig(ctx context.Context, organizationID string) error {
	provider, err := s.GetDefaultProviderInstance(ctx, organizationID)
	if err != nil {
		return err
	}
	return s.DeleteProviderInstance(ctx, organizationID, provider.ProviderID)
}
