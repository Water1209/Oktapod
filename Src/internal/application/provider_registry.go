package application

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	providerCatalogTTL       = 15 * time.Minute
	providerOperationTimeout = 15 * time.Second
)

type ProviderSecretStore interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}

type ProviderCatalogFactory func(core.ProviderInstance, string) ModelCatalog

type SaveProviderInput struct {
	DisplayName    string
	ProtocolFamily string
	Endpoint       string
	DefaultModelID string
	Status         string
	IsDefault      bool
	Credential     *string
}

func (s *Service) WithProviderRegistry(secrets ProviderSecretStore, catalogs ProviderCatalogFactory) *Service {
	s.providerSecrets = secrets
	s.providerCatalogs = catalogs
	return s
}

func (s *Service) ListProviders(ctx context.Context) ([]core.ProviderInstance, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return nil, err
	}
	items, err := s.repository.ListProviderInstances(ctx, s.ScopeFor(ctx).OrganizationID)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index].CredentialExists = s.providerCredentialExists(ctx, items[index].CredentialRef)
	}
	return items, nil
}

func (s *Service) GetProvider(ctx context.Context, providerID string) (core.ProviderInstance, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return core.ProviderInstance{}, err
	}
	item, err := s.repository.GetProviderInstance(ctx, s.ScopeFor(ctx).OrganizationID, providerID)
	if err != nil {
		return core.ProviderInstance{}, err
	}
	item.CredentialExists = s.providerCredentialExists(ctx, item.CredentialRef)
	return item, nil
}

func (s *Service) SaveProvider(ctx context.Context, providerID string, input SaveProviderInput) (core.ProviderInstance, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return core.ProviderInstance{}, err
	}
	if s.providerSecrets == nil {
		return core.ProviderInstance{}, fmt.Errorf("provider secret store is unavailable")
	}
	organizationID := s.ScopeFor(ctx).OrganizationID
	providerID = strings.TrimSpace(providerID)
	isCreate := providerID == ""
	if isCreate {
		providerID = uuid.NewString()
	}
	endpoint, err := validateProviderEndpoint(input.Endpoint)
	if err != nil {
		return core.ProviderInstance{}, err
	}
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.ProtocolFamily = strings.TrimSpace(input.ProtocolFamily)
	input.DefaultModelID = strings.TrimSpace(input.DefaultModelID)
	input.Status = strings.TrimSpace(input.Status)
	if input.DisplayName == "" {
		return core.ProviderInstance{}, &core.ValidationError{Field: "display_name", Message: "is required"}
	}
	if input.ProtocolFamily == "" {
		input.ProtocolFamily = core.ProviderProtocolOpenAICompatible
	}
	switch input.ProtocolFamily {
	case core.ProviderProtocolOpenAICompatible, core.ProviderProtocolAnthropic, core.ProviderProtocolGemini:
	default:
		return core.ProviderInstance{}, &core.ValidationError{Field: "protocol_family", Message: "must be openai_compatible, anthropic, or gemini"}
	}
	if input.Status == "" {
		input.Status = core.ProviderStatusActive
	}

	var existing core.ProviderInstance
	existing, existingErr := s.repository.GetProviderInstance(ctx, organizationID, providerID)
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return core.ProviderInstance{}, existingErr
	}
	if isCreate {
		current, listErr := s.repository.ListProviderInstances(ctx, organizationID)
		if listErr != nil {
			return core.ProviderInstance{}, listErr
		}
		if len(current) == 0 {
			input.IsDefault = true
		}
	}

	credentialRef := existing.CredentialRef
	if credentialRef == "" {
		credentialRef = "model-provider/" + organizationID + "/" + uuid.NewString()
	}
	var oldCredential string
	var oldCredentialExists bool
	if existingErr == nil {
		oldCredential, err = s.providerSecrets.Get(ctx, credentialRef)
		oldCredentialExists = err == nil
	}
	if input.Credential != nil {
		value := strings.TrimSpace(*input.Credential)
		if value == "" {
			return core.ProviderInstance{}, &core.ValidationError{Field: "credential", Message: "must not be empty when supplied"}
		}
		if err = s.providerSecrets.Set(ctx, credentialRef, value); err != nil {
			return core.ProviderInstance{}, fmt.Errorf("store provider credential: %w", err)
		}
	} else if isCreate {
		return core.ProviderInstance{}, &core.ValidationError{Field: "credential", Message: "is required for a new provider"}
	}

	saved, err := s.repository.UpsertProviderInstance(ctx, organizationID, core.UpsertProviderInstanceInput{
		ProviderID: providerID, DisplayName: input.DisplayName,
		ProtocolFamily: input.ProtocolFamily, Endpoint: endpoint,
		CredentialRef: credentialRef, DefaultModelID: input.DefaultModelID,
		Status: input.Status, IsDefault: input.IsDefault,
	})
	if err != nil {
		if input.Credential != nil {
			if oldCredentialExists {
				_ = s.providerSecrets.Set(ctx, credentialRef, oldCredential)
			} else {
				_ = s.providerSecrets.Delete(ctx, credentialRef)
			}
		}
		return core.ProviderInstance{}, err
	}
	saved.CredentialExists = s.providerCredentialExists(ctx, credentialRef)
	return saved, nil
}

func (s *Service) DeleteProvider(ctx context.Context, providerID string) error {
	if _, err := s.principalFor(ctx); err != nil {
		return err
	}
	if s.providerSecrets == nil {
		return fmt.Errorf("provider secret store is unavailable")
	}
	organizationID := s.ScopeFor(ctx).OrganizationID
	existing, err := s.repository.GetProviderInstance(ctx, organizationID, providerID)
	if err != nil {
		return err
	}
	oldCredential, credentialErr := s.providerSecrets.Get(ctx, existing.CredentialRef)
	if credentialErr == nil {
		if err = s.providerSecrets.Delete(ctx, existing.CredentialRef); err != nil {
			return fmt.Errorf("delete provider credential: %w", err)
		}
	}
	if err = s.repository.DeleteProviderInstance(ctx, organizationID, providerID); err != nil {
		if credentialErr == nil {
			_ = s.providerSecrets.Set(ctx, existing.CredentialRef, oldCredential)
		}
		return err
	}
	if existing.IsDefault {
		remaining, listErr := s.repository.ListProviderInstances(ctx, organizationID)
		if listErr == nil && len(remaining) > 0 {
			next := remaining[0]
			next.IsDefault = true
			_, _ = s.repository.UpsertProviderInstance(ctx, organizationID, core.UpsertProviderInstanceInput{
				ProviderID: next.ProviderID, DisplayName: next.DisplayName,
				ProtocolFamily: next.ProtocolFamily, Endpoint: next.Endpoint,
				CredentialRef: next.CredentialRef, DefaultModelID: next.DefaultModelID,
				Status: next.Status, IsDefault: true,
			})
		}
	}
	return nil
}

func (s *Service) TestProvider(ctx context.Context, providerID string) (core.ProviderTestResult, error) {
	started := time.Now()
	result := core.ProviderTestResult{ProviderID: providerID, Stage: "configuration", CatalogStatus: "unavailable"}
	provider, err := s.GetProvider(ctx, providerID)
	if err != nil {
		return result, err
	}
	entries, err := s.refreshProviderCatalog(ctx, provider)
	result.LatencyMillis = time.Since(started).Milliseconds()
	result.CompletedAt = time.Now().UTC()
	if err != nil {
		result.Stage = "catalog"
		result.RedactedError = redactProviderError(err)
		return result, nil
	}
	result.Stage = "complete"
	result.CatalogStatus = "fresh"
	result.ModelCount = len(entries)
	return result, nil
}

func (s *Service) ListProviderModels(ctx context.Context, providerID string) ([]core.ModelCatalogEntry, error) {
	provider, err := s.GetProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	cached, cacheErr := s.repository.ListModelCatalogEntries(ctx, provider.OrganizationID, providerID)
	if cacheErr == nil && len(cached) > 0 {
		if cached[0].Stale {
			go func(snapshot core.ProviderInstance) {
				refreshCtx, cancel := context.WithTimeout(context.Background(), providerOperationTimeout)
				defer cancel()
				_, _ = s.refreshProviderCatalog(refreshCtx, snapshot)
			}(provider)
		}
		return cached, nil
	}
	return s.refreshProviderCatalog(ctx, provider)
}

func (s *Service) refreshProviderCatalog(ctx context.Context, provider core.ProviderInstance) ([]core.ModelCatalogEntry, error) {
	if s.providerSecrets == nil || s.providerCatalogs == nil {
		return nil, fmt.Errorf("provider catalog runtime is unavailable")
	}
	credential, err := s.providerSecrets.Get(ctx, provider.CredentialRef)
	if err != nil {
		return nil, fmt.Errorf("provider credential is unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, providerOperationTimeout)
	defer cancel()
	models, err := s.providerCatalogs(provider, credential).ListModels(callCtx)
	if err != nil {
		return nil, fmt.Errorf("provider catalog request failed")
	}
	now := time.Now().UTC()
	payload, _ := json.Marshal(models)
	hash := sha256.Sum256(payload)
	version := hex.EncodeToString(hash[:8])
	entries := make([]core.ModelCatalogEntry, 0, len(models))
	for _, model := range models {
		capabilities, _ := json.Marshal(map[string]bool{
			"tool_calling": model.ToolCalling, "vision": model.Vision,
			"structured_output": model.StructuredOutput, "reasoning": model.Reasoning,
		})
		entries = append(entries, core.ModelCatalogEntry{
			ProviderID: provider.ProviderID, ModelID: model.ID,
			CapabilitiesJSON: string(capabilities), ContextWindow: model.ContextWindow,
			FetchedAt: now, ExpiresAt: now.Add(providerCatalogTTL), CatalogVersion: version,
		})
	}
	if err := s.repository.ReplaceModelCatalog(ctx, provider.OrganizationID, provider.ProviderID, entries); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Service) providerCredentialExists(ctx context.Context, ref string) bool {
	if s.providerSecrets == nil || strings.TrimSpace(ref) == "" {
		return false
	}
	_, err := s.providerSecrets.Get(ctx, ref)
	return err == nil
}

func validateProviderEndpoint(raw string) (string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(raw), "/")
	if len(endpoint) > 2048 {
		return "", &core.ValidationError{Field: "endpoint", Message: "exceeds 2048 characters"}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", &core.ValidationError{Field: "endpoint", Message: "must be an absolute HTTP(S) URL without credentials or a fragment"}
	}
	return endpoint, nil
}

func redactProviderError(err error) string {
	if err == nil {
		return ""
	}
	return "Provider request failed. Verify the endpoint, credential, and network access."
}

func (s *Service) freezeRunModelSnapshot(ctx context.Context, snapshot map[string]any, input core.CreateTaskInput) error {
	thinking := strings.TrimSpace(input.Thinking)
	if thinking == "" {
		thinking = "auto"
	}
	switch thinking {
	case "auto", "off", "low", "medium", "high":
	default:
		return &core.ValidationError{Field: "thinking", Message: "must be auto, off, low, medium, or high"}
	}
	snapshot["thinking_level"] = thinking

	organizationID := s.ScopeFor(ctx).OrganizationID
	var provider core.ProviderInstance
	var err error
	if strings.TrimSpace(input.ProviderID) != "" {
		provider, err = s.repository.GetProviderInstance(ctx, organizationID, input.ProviderID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &core.ValidationError{Field: "provider_id", Message: "does not identify a visible provider instance"}
			}
			return err
		}
	} else {
		provider, err = s.repository.GetDefaultProviderInstance(ctx, organizationID)
		if errors.Is(err, sql.ErrNoRows) {
			// Offline/unconfigured mode may still create a durable task. The
			// worker will block visibly until a provider is configured.
			return nil
		}
		if err != nil {
			return err
		}
	}
	if provider.Status != core.ProviderStatusActive {
		return &core.ValidationError{Field: "provider_id", Message: "provider is not active"}
	}
	modelID := modelFromSnapshot(snapshot)
	if modelID == "" {
		modelID = provider.DefaultModelID
	}
	snapshot["requested_provider_id"] = provider.ProviderID
	snapshot["requested_model_id"] = modelID
	snapshot["effective_provider_id"] = provider.ProviderID
	snapshot["effective_model_id"] = modelID
	snapshot["model_fallback_reason"] = ""
	snapshot["model_ref"] = map[string]any{"provider_id": provider.ProviderID, "model_id": modelID}
	if entries, listErr := s.repository.ListModelCatalogEntries(ctx, organizationID, provider.ProviderID); listErr == nil {
		for _, entry := range entries {
			if entry.ModelID == modelID {
				snapshot["model_catalog_version"] = entry.CatalogVersion
				break
			}
		}
	}
	return nil
}

func modelFromSnapshot(snapshot map[string]any) string {
	if value, _ := snapshot["model_override"].(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if value, _ := snapshot["project_default_model"].(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if agent, ok := snapshot["agent"].(map[string]any); ok {
		if value, _ := agent["model"].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
