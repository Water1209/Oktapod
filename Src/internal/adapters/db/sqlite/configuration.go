package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (s *Store) GetAIGovernanceSettings(ctx context.Context, organizationID string) (core.AIGovernanceSettings, error) {
	var item core.AIGovernanceSettings
	var enabled, failClosed int
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT organization_id, reviewer_provider_id, reviewer_model_id, enabled,
       timeout_seconds, fail_closed, policy_version, created_at, updated_at
FROM ai_governance_settings WHERE organization_id = ?`, strings.TrimSpace(organizationID)).Scan(
		&item.OrganizationID, &item.ReviewerProviderID, &item.ReviewerModelID, &enabled,
		&item.TimeoutSeconds, &failClosed, &item.PolicyVersion, &createdAt, &updatedAt,
	)
	if err != nil {
		return core.AIGovernanceSettings{}, err
	}
	item.Enabled = enabled == 1
	item.FailClosed = failClosed == 1
	item.CreatedAt = mustParseTime(createdAt)
	item.UpdatedAt = mustParseTime(updatedAt)
	return item, nil
}

func (s *Store) UpsertAIGovernanceSettings(ctx context.Context, organizationID string, input core.AIGovernanceSettings) (core.AIGovernanceSettings, error) {
	organizationID = strings.TrimSpace(organizationID)
	input.ReviewerProviderID = strings.TrimSpace(input.ReviewerProviderID)
	input.ReviewerModelID = strings.TrimSpace(input.ReviewerModelID)
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 30
	}
	if input.TimeoutSeconds < 5 || input.TimeoutSeconds > 120 || !input.FailClosed {
		return core.AIGovernanceSettings{}, fmt.Errorf("AI governance timeout must be 5-120 seconds and fail_closed must be true")
	}
	if input.Enabled && (input.ReviewerProviderID == "" || input.ReviewerModelID == "") {
		return core.AIGovernanceSettings{}, fmt.Errorf("enabled Reviewer requires provider and model")
	}
	if input.ReviewerProviderID != "" {
		var enabled int
		err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM provider_instances pi
JOIN provider_enabled_models pem ON pem.provider_id = pi.provider_id AND pem.organization_id = pi.organization_id
WHERE pi.organization_id = ? AND pi.provider_id = ? AND pem.model_id = ? AND pi.status = 'active'`,
			organizationID, input.ReviewerProviderID, input.ReviewerModelID).Scan(&enabled)
		if err != nil || enabled == 0 {
			return core.AIGovernanceSettings{}, fmt.Errorf("Reviewer provider/model is not enabled")
		}
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO ai_governance_settings (
    organization_id, reviewer_provider_id, reviewer_model_id, enabled,
    timeout_seconds, fail_closed, policy_version, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, 1, 1, ?, ?)
ON CONFLICT(organization_id) DO UPDATE SET
    reviewer_provider_id = excluded.reviewer_provider_id,
    reviewer_model_id = excluded.reviewer_model_id,
    enabled = excluded.enabled,
    timeout_seconds = excluded.timeout_seconds,
    fail_closed = 1,
    policy_version = ai_governance_settings.policy_version + 1,
    updated_at = excluded.updated_at`,
		organizationID, input.ReviewerProviderID, input.ReviewerModelID, boolToInt(input.Enabled),
		input.TimeoutSeconds, formatTime(now), formatTime(now),
	)
	if err != nil {
		return core.AIGovernanceSettings{}, err
	}
	return s.GetAIGovernanceSettings(ctx, organizationID)
}

func (s *Store) ListModelPricingVersions(ctx context.Context, organizationID string) ([]core.ModelPricingVersion, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT organization_id, provider_id, model_id, currency, input_per_1k, output_per_1k,
       effective_at, version, created_at, updated_at
FROM model_pricing_versions WHERE organization_id = ?
ORDER BY provider_id, model_id, effective_at DESC, version DESC`, strings.TrimSpace(organizationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ModelPricingVersion{}
	for rows.Next() {
		var item core.ModelPricingVersion
		var effectiveAt, createdAt, updatedAt string
		if err := rows.Scan(&item.OrganizationID, &item.ProviderID, &item.ModelID, &item.Currency,
			&item.InputPer1K, &item.OutputPer1K, &effectiveAt, &item.Version, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.EffectiveAt = mustParseTime(effectiveAt)
		item.CreatedAt = mustParseTime(createdAt)
		item.UpdatedAt = mustParseTime(updatedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpsertModelPricingVersions(ctx context.Context, organizationID string, items []core.ModelPricingVersion) ([]core.ModelPricingVersion, error) {
	if len(items) > 500 {
		return nil, fmt.Errorf("at most 500 pricing versions can be saved at once")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	for _, item := range items {
		item.ProviderID = strings.TrimSpace(item.ProviderID)
		item.ModelID = strings.TrimSpace(item.ModelID)
		item.Currency = strings.ToUpper(strings.TrimSpace(item.Currency))
		if item.Currency == "" {
			item.Currency = "USD"
		}
		if item.ProviderID == "" || item.ModelID == "" || item.Version < 1 || item.EffectiveAt.IsZero() ||
			!validNonNegativeDecimal(item.InputPer1K) || !validNonNegativeDecimal(item.OutputPer1K) || len(item.Currency) != 3 {
			return nil, fmt.Errorf("invalid model pricing version for %s/%s", item.ProviderID, item.ModelID)
		}
		var providerExists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_instances WHERE organization_id = ? AND provider_id = ?`, organizationID, item.ProviderID).Scan(&providerExists); err != nil || providerExists == 0 {
			return nil, fmt.Errorf("pricing provider %s is not available", item.ProviderID)
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO model_pricing_versions (
    organization_id, provider_id, model_id, currency, input_per_1k, output_per_1k,
    effective_at, version, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(organization_id, provider_id, model_id, effective_at, version) DO UPDATE SET
    currency = excluded.currency,
    input_per_1k = excluded.input_per_1k,
    output_per_1k = excluded.output_per_1k,
    updated_at = excluded.updated_at`,
			organizationID, item.ProviderID, item.ModelID, item.Currency,
			strings.TrimSpace(item.InputPer1K), strings.TrimSpace(item.OutputPer1K),
			formatTime(item.EffectiveAt.UTC()), item.Version, now, now,
		)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListModelPricingVersions(ctx, organizationID)
}

func (s *Store) ResolveModelPricing(ctx context.Context, organizationID, providerID, modelID string, at time.Time) (core.ModelPricingVersion, error) {
	var item core.ModelPricingVersion
	var effectiveAt, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT organization_id, provider_id, model_id, currency, input_per_1k, output_per_1k,
       effective_at, version, created_at, updated_at
FROM model_pricing_versions
WHERE organization_id = ? AND provider_id = ? AND model_id = ? AND effective_at <= ?
ORDER BY effective_at DESC, version DESC LIMIT 1`,
		strings.TrimSpace(organizationID), strings.TrimSpace(providerID), strings.TrimSpace(modelID), formatTime(at.UTC())).Scan(
		&item.OrganizationID, &item.ProviderID, &item.ModelID, &item.Currency,
		&item.InputPer1K, &item.OutputPer1K, &effectiveAt, &item.Version, &createdAt, &updatedAt,
	)
	if err != nil {
		return core.ModelPricingVersion{}, err
	}
	item.EffectiveAt = mustParseTime(effectiveAt)
	item.CreatedAt = mustParseTime(createdAt)
	item.UpdatedAt = mustParseTime(updatedAt)
	return item, nil
}

func (s *Store) GetUserPreferences(ctx context.Context, scope core.Scope) (core.UserPreferences, error) {
	var item core.UserPreferences
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `
SELECT organization_id, user_id, theme, density, default_permission,
       recent_provider_id, recent_model_id, created_at, updated_at
FROM user_preferences WHERE organization_id = ? AND user_id = ?`, scope.OrganizationID, scope.UserID).Scan(
		&item.OrganizationID, &item.UserID, &item.Theme, &item.Density, &item.DefaultPermission,
		&item.RecentProviderID, &item.RecentModelID, &createdAt, &updatedAt,
	)
	if errorsIsNoRows(err) {
		now := time.Now().UTC()
		return core.UserPreferences{OrganizationID: scope.OrganizationID, UserID: scope.UserID, Theme: "system", Density: "comfortable", DefaultPermission: "ask", CreatedAt: now, UpdatedAt: now}, nil
	}
	if err != nil {
		return core.UserPreferences{}, err
	}
	item.CreatedAt = mustParseTime(createdAt)
	item.UpdatedAt = mustParseTime(updatedAt)
	return item, nil
}

func (s *Store) UpsertUserPreferences(ctx context.Context, scope core.Scope, input core.UserPreferences) (core.UserPreferences, error) {
	if input.Theme != "system" && input.Theme != "light" && input.Theme != "dark" {
		return core.UserPreferences{}, fmt.Errorf("invalid theme")
	}
	if input.Density != "compact" && input.Density != "comfortable" {
		return core.UserPreferences{}, fmt.Errorf("invalid density")
	}
	if input.DefaultPermission != "ask" && input.DefaultPermission != "auto_review" {
		return core.UserPreferences{}, fmt.Errorf("full_access cannot be persisted as a default permission")
	}
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `
INSERT INTO user_preferences (
    organization_id, user_id, theme, density, default_permission,
    recent_provider_id, recent_model_id, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(organization_id, user_id) DO UPDATE SET
    theme = excluded.theme,
    density = excluded.density,
    default_permission = excluded.default_permission,
    recent_provider_id = excluded.recent_provider_id,
    recent_model_id = excluded.recent_model_id,
    updated_at = excluded.updated_at`,
		scope.OrganizationID, scope.UserID, input.Theme, input.Density, input.DefaultPermission,
		strings.TrimSpace(input.RecentProviderID), strings.TrimSpace(input.RecentModelID), now, now,
	)
	if err != nil {
		return core.UserPreferences{}, err
	}
	return s.GetUserPreferences(ctx, scope)
}

func errorsIsNoRows(err error) bool { return err == sql.ErrNoRows }

func validNonNegativeDecimal(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "eE") {
		return false
	}
	rational, ok := new(big.Rat).SetString(value)
	return ok && rational.Sign() >= 0 && len(value) <= 40
}
