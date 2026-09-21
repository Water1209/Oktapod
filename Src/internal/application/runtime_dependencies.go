package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Service) WithRuntimeDependencyProvider(provider RuntimeDependencyProvider) *Service {
	if provider != nil {
		s.runtimeDependencies = append(s.runtimeDependencies, provider)
	}
	return s
}

// RuntimeDependencyProviders is used by tests to verify the generic provider
// contract without exposing mutation of the production provider set.
func (s *Service) RuntimeDependencyProviders() []RuntimeDependencyProvider {
	return append([]RuntimeDependencyProvider(nil), s.runtimeDependencies...)
}

func (s *Service) ListRuntimeDependencies(ctx context.Context, capabilityID string) ([]RuntimeDependency, error) {
	capability, definition, err := s.mcpCapability(ctx, capabilityID)
	if err != nil {
		return nil, err
	}
	if capability.Status != core.CapabilityStatusEnabled {
		return []RuntimeDependency{}, nil
	}
	health, healthErr := s.repository.GetMCPHealth(ctx, s.ScopeFor(ctx), capabilityID)
	lastError := ""
	if healthErr == nil {
		lastError = health.LastError
	} else if !errors.Is(healthErr, core.ErrMCPNotConnected) {
		return nil, healthErr
	}
	request := RuntimeDependencyRequest{Capability: capability, Definition: definition, LastError: lastError}
	dependencies := []RuntimeDependency{}
	for _, provider := range s.runtimeDependencies {
		items, err := provider.Detect(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("detect runtime dependency from provider %T: %w", provider, err)
		}
		dependencies = append(dependencies, items...)
	}
	return dependencies, nil
}

func (s *Service) InstallRuntimeDependency(ctx context.Context, capabilityID, dependencyProvider, dependencyID, correlationID string, confirmed bool) (core.Capability, error) {
	if !confirmed {
		return core.Capability{}, &core.ValidationError{Field: "confirmed", Message: "must be true after the user reviews the dependency install"}
	}
	if strings.TrimSpace(dependencyProvider) == "" {
		return core.Capability{}, &core.ValidationError{Field: "provider", Message: "is required"}
	}
	if strings.TrimSpace(dependencyID) == "" {
		return core.Capability{}, &core.ValidationError{Field: "dependency_id", Message: "is required"}
	}
	if len(s.runtimeDependencies) == 0 {
		return core.Capability{}, &core.ResourceConflictError{Resource: "runtime_dependency_provider", Reason: "运行时依赖安装器暂不可用"}
	}
	capability, definition, err := s.mcpCapability(ctx, capabilityID)
	if err != nil {
		return core.Capability{}, err
	}
	dependencies, err := s.ListRuntimeDependencies(ctx, capabilityID)
	if err != nil {
		return core.Capability{}, err
	}
	for _, dependency := range dependencies {
		if dependency.Provider != dependencyProvider || dependency.ID != dependencyID {
			continue
		}
		if !dependency.Installable {
			return core.Capability{}, &core.ValidationError{Field: "dependency_id", Message: "this runtime dependency cannot be installed automatically"}
		}
		var installer RuntimeDependencyProvider
		for _, provider := range s.runtimeDependencies {
			if provider.Supports(dependency) {
				installer = provider
				break
			}
		}
		if installer == nil {
			return core.Capability{}, &core.ValidationError{Field: "provider", Message: "the provider that reported this runtime dependency is unavailable"}
		}
		if err := installer.Install(ctx, RuntimeDependencyInstallRequest{Capability: capability, Definition: definition, Dependency: dependency}); err != nil {
			return core.Capability{}, fmt.Errorf("install runtime dependency %s: %w", dependency.ID, err)
		}
		if auditor, ok := s.repository.(CapabilityAuditor); ok {
			if err := auditor.AuditCapability(ctx, s.ScopeFor(ctx), capabilityID, "capability.runtime_dependency_installed", map[string]any{
				"dependency_id": dependency.ID, "provider": dependency.Provider, "kind": dependency.Kind, "version": dependency.Version,
			}, correlationID); err != nil {
				return core.Capability{}, err
			}
		}
		if _, err := s.ConnectMCP(ctx, capabilityID, correlationID); err != nil {
			return s.decoratedCapability(ctx, capability), err
		}
		return s.decoratedCapability(ctx, capability), nil
	}
	return core.Capability{}, &core.ValidationError{Field: "dependency_id", Message: "is not currently reported for this capability"}
}

func (s *Service) mcpCapability(ctx context.Context, capabilityID string) (core.Capability, core.MCPDefinitionV1, error) {
	capability, err := s.repository.GetCapability(ctx, s.ScopeFor(ctx), capabilityID)
	if err != nil {
		return core.Capability{}, core.MCPDefinitionV1{}, err
	}
	if capability.Kind != core.CapabilityKindMCP {
		return core.Capability{}, core.MCPDefinitionV1{}, &core.ValidationError{Field: "capability_id", Message: "must identify an MCP capability"}
	}
	definition, err := core.ParseMCPDefinition(capability.ManifestJSON)
	if err != nil {
		return core.Capability{}, core.MCPDefinitionV1{}, err
	}
	return capability, definition, nil
}

func (s *Service) decoratedCapability(ctx context.Context, capability core.Capability) core.Capability {
	capability = s.decorateCapabilityCredential(ctx, capability)
	return s.decorateCapabilityAvailability(ctx, capability)
}
