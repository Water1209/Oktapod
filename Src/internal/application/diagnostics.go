package application

import (
	"context"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// RuntimeVersion is replaced by release and container builds through -ldflags.
// Keeping developer builds explicit prevents an unversioned local binary from
// being mistaken for a published artifact.
var RuntimeVersion = "dev"

type RuntimeDiagnosticsRepository interface {
	RuntimeDiagnostics(context.Context, string, time.Time) (core.RuntimeDiagnostics, error)
}

type Diagnostics struct {
	Version             string
	Profile             string
	Database            string
	DatabaseStatus      string
	ProviderCount       int
	ActiveProviderCount int
	StaleCatalogCount   int
	Worker              core.RuntimeDiagnostics
}

func (s *Service) Diagnostics(ctx context.Context) (Diagnostics, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return Diagnostics{}, err
	}
	result := Diagnostics{
		Version:        RuntimeVersion,
		Profile:        s.profile,
		Database:       s.database,
		DatabaseStatus: "ok",
	}
	if err := s.repository.Ping(ctx); err != nil {
		result.DatabaseStatus = "degraded"
		return result, nil
	}
	providers, err := s.repository.ListProviderInstances(ctx, s.ScopeFor(ctx).OrganizationID)
	if err != nil {
		return Diagnostics{}, err
	}
	result.ProviderCount = len(providers)
	now := time.Now().UTC()
	for _, provider := range providers {
		if provider.Status == core.ProviderStatusActive {
			result.ActiveProviderCount++
		}
		entries, listErr := s.repository.ListModelCatalogEntries(ctx, s.ScopeFor(ctx).OrganizationID, provider.ProviderID)
		if listErr != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.ExpiresAt.IsZero() && entry.ExpiresAt.Before(now) {
				result.StaleCatalogCount++
				break
			}
		}
	}
	if repository, ok := s.repository.(RuntimeDiagnosticsRepository); ok {
		result.Worker, err = repository.RuntimeDiagnostics(ctx, s.ScopeFor(ctx).OrganizationID, now)
		if err != nil {
			return Diagnostics{}, err
		}
	}
	return result, nil
}
