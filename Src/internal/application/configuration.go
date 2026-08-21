package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type VaultController interface {
	Status(context.Context) core.VaultStatus
	Initialize(context.Context) (string, core.VaultStatus, error)
	Confirm(context.Context, string) (core.VaultStatus, error)
	Recover(context.Context, string) (core.VaultStatus, error)
	RotateRecovery(context.Context) (string, core.VaultStatus, error)
	ResetPending(context.Context) error
}

type BootstrapConfigurationManager interface {
	Snapshot() core.BootstrapConfiguration
	Update(core.UpdateBootstrapConfiguration) (core.BootstrapConfiguration, error)
}

type ConfigurationRepository interface {
	GetOrganizationRole(context.Context, string, string) (string, error)
	GetAIGovernanceSettings(context.Context, string) (core.AIGovernanceSettings, error)
	UpsertAIGovernanceSettings(context.Context, string, core.AIGovernanceSettings) (core.AIGovernanceSettings, error)
	ListModelPricingVersions(context.Context, string) ([]core.ModelPricingVersion, error)
	UpsertModelPricingVersions(context.Context, string, []core.ModelPricingVersion) ([]core.ModelPricingVersion, error)
	GetUserPreferences(context.Context, core.Scope) (core.UserPreferences, error)
	UpsertUserPreferences(context.Context, core.Scope, core.UserPreferences) (core.UserPreferences, error)
}

func (s *Service) WithVault(controller VaultController) *Service {
	s.vault = controller
	return s
}

func (s *Service) WithBootstrapConfiguration(manager BootstrapConfigurationManager) *Service {
	s.bootstrapConfiguration = manager
	return s
}

func (s *Service) VaultStatus(ctx context.Context) (core.VaultStatus, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return core.VaultStatus{}, err
	}
	if s.vault == nil {
		return core.VaultStatus{State: core.VaultRuntimeUninitialized, Detail: "数据库密钥库未装配。"}, nil
	}
	return s.vault.Status(ctx), nil
}

func (s *Service) InitializeVault(ctx context.Context) (string, core.VaultStatus, error) {
	if err := s.requireOwner(ctx); err != nil {
		return "", core.VaultStatus{}, err
	}
	if s.vault == nil {
		return "", core.VaultStatus{}, fmt.Errorf("database vault is unavailable")
	}
	if status := s.vault.Status(ctx); status.State != core.VaultRuntimeUninitialized {
		return "", status, &core.ResourceConflictError{Resource: "database vault", Reason: "initialization is only allowed while uninitialized"}
	}
	return s.vault.Initialize(ctx)
}

func (s *Service) ConfirmVault(ctx context.Context, recoveryCode string) (core.VaultStatus, error) {
	if err := s.requireOwner(ctx); err != nil {
		return core.VaultStatus{}, err
	}
	if s.vault == nil {
		return core.VaultStatus{}, fmt.Errorf("database vault is unavailable")
	}
	if status := s.vault.Status(ctx); status.State != core.VaultRuntimePending {
		return status, &core.ResourceConflictError{Resource: "database vault", Reason: "confirmation is only allowed while pending"}
	}
	return s.vault.Confirm(ctx, strings.TrimSpace(recoveryCode))
}

func (s *Service) RecoverVault(ctx context.Context, recoveryCode string) (core.VaultStatus, error) {
	if err := s.requireOwner(ctx); err != nil {
		return core.VaultStatus{}, err
	}
	if s.vault == nil {
		return core.VaultStatus{}, fmt.Errorf("database vault is unavailable")
	}
	return s.vault.Recover(ctx, strings.TrimSpace(recoveryCode))
}

func (s *Service) RotateVaultRecovery(ctx context.Context) (string, core.VaultStatus, error) {
	if err := s.requireOwner(ctx); err != nil {
		return "", core.VaultStatus{}, err
	}
	if s.vault == nil {
		return "", core.VaultStatus{}, fmt.Errorf("database vault is unavailable")
	}
	return s.vault.RotateRecovery(ctx)
}

func (s *Service) ResetPendingVault(ctx context.Context) error {
	if err := s.requireOwner(ctx); err != nil {
		return err
	}
	if s.vault == nil {
		return fmt.Errorf("database vault is unavailable")
	}
	if status := s.vault.Status(ctx); status.State != core.VaultRuntimePending {
		return &core.ResourceConflictError{Resource: "database vault", Reason: "reset is only allowed while pending"}
	}
	return s.vault.ResetPending(ctx)
}

func (s *Service) Configuration(ctx context.Context) (core.BootstrapConfiguration, error) {
	principal, err := s.principalFor(ctx)
	if err != nil {
		return core.BootstrapConfiguration{}, err
	}
	if s.bootstrapConfiguration == nil {
		return core.BootstrapConfiguration{}, fmt.Errorf("bootstrap configuration manager is unavailable")
	}
	configuration := s.bootstrapConfiguration.Snapshot()
	canReadOrigins := principal.Administrator
	if !canReadOrigins {
		if repository, ok := s.repository.(ConfigurationRepository); ok {
			role, roleErr := repository.GetOrganizationRole(ctx, principal.OrganizationID, principal.UserID)
			canReadOrigins = roleErr == nil && role == "owner"
		}
	}
	if !canReadOrigins {
		configuration.WebAccess.PrivateFetch.AllowedOrigins = nil
		configuration.WebAccess.PrivateFetch.OriginsRedacted = true
	}
	return configuration, nil
}

func (s *Service) UpdateBootstrapConfiguration(ctx context.Context, input core.UpdateBootstrapConfiguration) (core.BootstrapConfiguration, error) {
	if err := s.requireOwner(ctx); err != nil {
		return core.BootstrapConfiguration{}, err
	}
	return s.bootstrapConfiguration.Update(input)
}

func (s *Service) AIGovernanceSettings(ctx context.Context) (core.AIGovernanceSettings, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return core.AIGovernanceSettings{}, err
	}
	repository, ok := s.repository.(ConfigurationRepository)
	if !ok {
		return core.AIGovernanceSettings{}, fmt.Errorf("AI governance repository is unavailable")
	}
	item, err := repository.GetAIGovernanceSettings(ctx, s.ScopeFor(ctx).OrganizationID)
	if errors.Is(err, sql.ErrNoRows) {
		return core.AIGovernanceSettings{OrganizationID: s.ScopeFor(ctx).OrganizationID, TimeoutSeconds: 30, FailClosed: true, PolicyVersion: 1}, nil
	}
	return item, err
}

func (s *Service) UpdateAIGovernanceSettings(ctx context.Context, input core.AIGovernanceSettings) (core.AIGovernanceSettings, error) {
	if err := s.requireOwner(ctx); err != nil {
		return core.AIGovernanceSettings{}, err
	}
	repository, ok := s.repository.(ConfigurationRepository)
	if !ok {
		return core.AIGovernanceSettings{}, fmt.Errorf("AI governance repository is unavailable")
	}
	input.FailClosed = true
	return repository.UpsertAIGovernanceSettings(ctx, s.ScopeFor(ctx).OrganizationID, input)
}

func (s *Service) ModelPricing(ctx context.Context) ([]core.ModelPricingVersion, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return nil, err
	}
	repository, ok := s.repository.(ConfigurationRepository)
	if !ok {
		return nil, fmt.Errorf("model pricing repository is unavailable")
	}
	return repository.ListModelPricingVersions(ctx, s.ScopeFor(ctx).OrganizationID)
}

func (s *Service) UpdateModelPricing(ctx context.Context, items []core.ModelPricingVersion) ([]core.ModelPricingVersion, error) {
	if err := s.requireOwner(ctx); err != nil {
		return nil, err
	}
	repository, ok := s.repository.(ConfigurationRepository)
	if !ok {
		return nil, fmt.Errorf("model pricing repository is unavailable")
	}
	return repository.UpsertModelPricingVersions(ctx, s.ScopeFor(ctx).OrganizationID, items)
}

func (s *Service) Preferences(ctx context.Context) (core.UserPreferences, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return core.UserPreferences{}, err
	}
	repository, ok := s.repository.(ConfigurationRepository)
	if !ok {
		return core.UserPreferences{}, fmt.Errorf("preferences repository is unavailable")
	}
	return repository.GetUserPreferences(ctx, s.ScopeFor(ctx))
}

func (s *Service) UpdatePreferences(ctx context.Context, input core.UserPreferences) (core.UserPreferences, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return core.UserPreferences{}, err
	}
	repository, ok := s.repository.(ConfigurationRepository)
	if !ok {
		return core.UserPreferences{}, fmt.Errorf("preferences repository is unavailable")
	}
	return repository.UpsertUserPreferences(ctx, s.ScopeFor(ctx), input)
}

func (s *Service) requireOwner(ctx context.Context) error {
	principal, err := s.principalFor(ctx)
	if err != nil {
		return err
	}
	if principal.Administrator {
		return nil
	}
	repository, ok := s.repository.(ConfigurationRepository)
	if !ok {
		return &core.ForbiddenError{Message: "Owner or administrator permission is required"}
	}
	role, err := repository.GetOrganizationRole(ctx, principal.OrganizationID, principal.UserID)
	if err != nil || role != "owner" {
		return &core.ForbiddenError{Message: "Owner or administrator permission is required"}
	}
	return nil
}
