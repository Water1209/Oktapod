package core

import "time"

const ModelProviderOpenAICompatible = "openai_compatible"

// ModelProviderConfig contains only non-secret provider settings and a
// reference to the credential held by the operating-system credential store.
type ModelProviderConfig struct {
	OrganizationID string
	ProviderKind   string
	Endpoint       string
	DefaultModel   string
	SecretRef      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type UpsertModelProviderConfigInput struct {
	Endpoint     string
	DefaultModel string
	SecretRef    string
}
