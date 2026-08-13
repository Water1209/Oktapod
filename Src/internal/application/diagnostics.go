package application

import (
	"context"
	"os"
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
	reviewerModel := strings.TrimSpace(os.Getenv("OKTAPOD_REVIEWER_MODEL"))
	result.AutoReview = AutoReviewDiagnostics{Configured: reviewerModel != ""}
	if reviewerModel == "" {
		result.AutoReview.Reason = "未配置 OKTAPOD_REVIEWER_MODEL；自动审查会转为人工审批。"
	}
	if s.profile == core.ProfilePersonal && strings.TrimSpace(s.workspaceRoot) != "" {
		workspaceRoot, err := filepath.Abs(s.workspaceRoot)
		if err == nil {
			result.Storage = &StorageDiagnostics{DataRoot: filepath.Dir(workspaceRoot), WorkspaceRoot: workspaceRoot}
		}
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
	if reviewerModel != "" {
		switch {
		case result.ActiveProviderCount == 0:
			result.AutoReview.Reason = "已配置 Reviewer Model，但当前没有启用的 Provider；自动审查不可用。"
		case s.catalog == nil:
			result.AutoReview.Reason = "已配置 Reviewer Model，但模型目录不可用；自动审查不可用。"
		default:
			models, listErr := s.catalog.ListModels(ctx)
			if listErr != nil {
				result.AutoReview.Reason = "已配置 Reviewer Model，但模型目录读取失败；自动审查不可用。"
				break
			}
			for _, model := range models {
				if model.ID == reviewerModel {
					result.AutoReview.Available = true
					break
				}
			}
			if result.AutoReview.Available {
				result.AutoReview.Reason = "独立 Reviewer Model 已配置且可解析；运行时故障仍会关闭式转人工。"
			} else {
				result.AutoReview.Reason = "已配置的 Reviewer Model 不在当前模型目录中；自动审查不可用。"
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
