package application

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
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
	Storage             *StorageDiagnostics
	AutoReview          AutoReviewDiagnostics
	Pricing             PricingDiagnostics
	Vault               core.VaultStatus
	CredentialStates    map[string]int
	CredentialIssues    []CredentialIssue
	OverallStatus       string
}

type CredentialIssue struct {
	Kind       string
	ResourceID string
	Name       string
	State      string
	ActionPath string
}

type StorageDiagnostics struct {
	DataRoot      string
	WorkspaceRoot string
}

type AutoReviewDiagnostics struct {
	Configured bool
	Available  bool
	Reason     string
}

type PricingDiagnostics struct {
	VersionCount    int
	UncoveredModels int
}

func (s *Service) Diagnostics(ctx context.Context) (Diagnostics, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return Diagnostics{}, err
	}
	result := Diagnostics{
		Version:          RuntimeVersion,
		Profile:          s.profile,
		Database:         s.database,
		DatabaseStatus:   "ok",
		OverallStatus:    "ok",
		CredentialStates: map[string]int{core.CredentialReady: 0, core.CredentialLocked: 0, core.CredentialMissing: 0, core.CredentialCorrupt: 0},
		CredentialIssues: []CredentialIssue{},
	}
	if s.vault != nil {
		result.Vault = s.vault.Status(ctx)
	} else {
		result.Vault = core.VaultStatus{State: core.VaultRuntimeUninitialized, Detail: "数据库密钥库未装配。"}
	}
	if result.Vault.State != core.VaultRuntimeActive {
		result.OverallStatus = "attention"
	}
	if s.profile == core.ProfilePersonal && strings.TrimSpace(s.workspaceRoot) != "" {
		workspaceRoot, err := filepath.Abs(s.workspaceRoot)
		if err == nil {
			result.Storage = &StorageDiagnostics{DataRoot: filepath.Dir(workspaceRoot), WorkspaceRoot: workspaceRoot}
		}
	}
	if err := s.repository.Ping(ctx); err != nil {
		result.DatabaseStatus = "degraded"
		result.OverallStatus = "degraded"
		return result, nil
	}
	providers, err := s.repository.ListProviderInstances(ctx, s.ScopeFor(ctx).OrganizationID)
	if err != nil {
		return Diagnostics{}, err
	}
	result.ProviderCount = len(providers)
	pricingKeys := map[string]struct{}{}
	if configuration, ok := s.repository.(ConfigurationRepository); ok {
		if prices, priceErr := configuration.ListModelPricingVersions(ctx, s.ScopeFor(ctx).OrganizationID); priceErr == nil {
			result.Pricing.VersionCount = len(prices)
			for _, price := range prices {
				pricingKeys[price.ProviderID+"\x00"+price.ModelID] = struct{}{}
			}
		}
	}
	now := time.Now().UTC()
	for _, provider := range providers {
		if provider.Status == core.ProviderStatusActive {
			result.ActiveProviderCount++
		}
		status := s.providerCredentialStatus(ctx, provider.CredentialRef)
		result.CredentialStates[status]++
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
		models, modelErr := s.repository.ListProviderModelOptions(ctx, s.ScopeFor(ctx).OrganizationID, provider.ProviderID)
		if modelErr == nil {
			for _, model := range models {
				if !model.Enabled {
					continue
				}
				if _, covered := pricingKeys[provider.ProviderID+"\x00"+model.ModelID]; !covered {
					result.Pricing.UncoveredModels++
				}
			}
		}
	}
	if targets, ok := s.repository.(interface {
		ListCredentialTargets(context.Context, core.Scope) ([]core.CredentialTarget, error)
	}); ok {
		items, targetsErr := targets.ListCredentialTargets(ctx, s.ScopeFor(ctx))
		if targetsErr == nil {
			result.CredentialStates = map[string]int{core.CredentialReady: 0, core.CredentialLocked: 0, core.CredentialMissing: 0, core.CredentialCorrupt: 0}
			for _, target := range items {
				status := s.providerCredentialStatus(ctx, target.CredentialRef)
				result.CredentialStates[status]++
				if status == core.CredentialReady {
					continue
				}
				actionPath := "/resources"
				if target.Kind == core.SecretKindProvider {
					actionPath = "/settings/providers"
				} else {
					actionPath = fmt.Sprintf("/resources?view=project&kind=mcp&project=%s&resource=%s", url.QueryEscape(target.ProjectID), url.QueryEscape(target.ResourceID))
				}
				result.CredentialIssues = append(result.CredentialIssues, CredentialIssue{Kind: target.Kind, ResourceID: target.ResourceID, Name: target.Name, State: status, ActionPath: actionPath})
			}
		}
	} else if references, ok := s.repository.(interface {
		ListCredentialReferences(context.Context, string) ([]string, error)
	}); ok {
		refs, refsErr := references.ListCredentialReferences(ctx, s.ScopeFor(ctx).OrganizationID)
		if refsErr == nil {
			result.CredentialStates = map[string]int{core.CredentialReady: 0, core.CredentialLocked: 0, core.CredentialMissing: 0, core.CredentialCorrupt: 0}
			for _, ref := range refs {
				result.CredentialStates[s.providerCredentialStatus(ctx, ref)]++
			}
		}
	}
	settings, settingsErr := s.AIGovernanceSettings(ctx)
	result.AutoReview.Configured = settingsErr == nil && settings.Enabled && settings.ReviewerProviderID != "" && settings.ReviewerModelID != ""
	if !result.AutoReview.Configured {
		result.AutoReview.Reason = "未在设置中启用独立 Reviewer Provider 与 Model；自动审查会转为人工审批。"
	} else {
		provider, providerErr := s.repository.GetProviderInstance(ctx, s.ScopeFor(ctx).OrganizationID, settings.ReviewerProviderID)
		if providerErr != nil || provider.Status != core.ProviderStatusActive {
			result.AutoReview.Reason = "Reviewer Provider 不存在或未启用；自动审查不可用。"
		} else if credentialState := s.providerCredentialStatus(ctx, provider.CredentialRef); credentialState != core.CredentialReady {
			result.AutoReview.Reason = fmt.Sprintf("Reviewer 使用 %s，但其凭据状态为%s；请重新录入后再试。", provider.DisplayName, credentialStatusLabel(credentialState))
		} else {
			models, listErr := s.repository.ListProviderModelOptions(ctx, s.ScopeFor(ctx).OrganizationID, settings.ReviewerProviderID)
			if listErr == nil {
				for _, model := range models {
					if model.Enabled && model.ModelID == settings.ReviewerModelID {
						result.AutoReview.Available = true
						break
					}
				}
			}
			if result.AutoReview.Available {
				result.AutoReview.Reason = "独立 Reviewer Provider 与 Model 已启用；运行时故障仍会关闭式转人工。"
			} else {
				result.AutoReview.Reason = "Reviewer Model 未在所选 Provider 中启用；自动审查不可用。"
			}
		}
	}
	if !result.AutoReview.Available || result.Pricing.UncoveredModels > 0 || result.CredentialStates[core.CredentialLocked] > 0 || result.CredentialStates[core.CredentialMissing] > 0 || result.CredentialStates[core.CredentialCorrupt] > 0 {
		if result.OverallStatus == "ok" {
			result.OverallStatus = "attention"
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

func credentialStatusLabel(status string) string {
	switch status {
	case core.CredentialLocked:
		return "锁定"
	case core.CredentialCorrupt:
		return "损坏"
	case core.CredentialMissing:
		return "缺失"
	default:
		return "不可用"
	}
}
