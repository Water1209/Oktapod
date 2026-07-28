package core

import "time"

const (
	ProviderProtocolOpenAICompatible = "openai_compatible"
	ProviderProtocolAnthropic        = "anthropic"
	ProviderProtocolGemini           = "gemini"

	ProviderStatusActive   = "active"
	ProviderStatusDisabled = "disabled"
	ProviderStatusError    = "error"
)

// ProviderInstance is an organization-owned model endpoint. CredentialRef is
// internal-only and must never be serialized to API clients.
type ProviderInstance struct {
	ProviderID       string
	OrganizationID   string
	DisplayName      string
	ProtocolFamily   string
	Endpoint         string
	CredentialRef    string
	DefaultModelID   string
	Status           string
	IsDefault        bool
	CredentialExists bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type UpsertProviderInstanceInput struct {
	ProviderID     string
	DisplayName    string
	ProtocolFamily string
	Endpoint       string
	CredentialRef  string
	DefaultModelID string
	Status         string
	IsDefault      bool
}

type ModelCatalogEntry struct {
	ProviderID       string
	ModelID          string
	CapabilitiesJSON string
	ContextWindow    int64
	MaxOutput        int64
	ThinkingMapJSON  string
	PricingJSON      string
	FetchedAt        time.Time
	ExpiresAt        time.Time
	ETag             string
	LastError        string
	CatalogVersion   string
	Stale            bool
}

type ProviderTestResult struct {
	ProviderID    string
	Stage         string
	LatencyMillis int64
	CatalogStatus string
	ModelCount    int
	RedactedError string
	CompletedAt   time.Time
}
