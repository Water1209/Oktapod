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
	CredentialStatus string
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

const (
	ProviderModelSourceCatalog = "catalog"
	ProviderModelSourceManual  = "manual"
)

// ProviderModelOption is the user-controlled model selection for one Provider.
// Catalog metadata is optional: manually entered model IDs intentionally keep
// capabilities unknown until the Provider advertises them.
type ProviderModelOption struct {
	OrganizationID   string
	ProviderID       string
	ProviderName     string
	ModelID          string
	Source           string
	Enabled          bool
	Default          bool
	ProviderDefault  bool
	CapabilitiesJSON string
	ContextWindow    int64
	MaxOutput        int64
	FetchedAt        time.Time
	ExpiresAt        time.Time
	CatalogVersion   string
	Stale            bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
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
