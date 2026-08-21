package core

import "time"

const (
	SecretVaultStatePending = "pending"
	SecretVaultStateActive  = "active"

	VaultRuntimeUninitialized = "uninitialized"
	VaultRuntimePending       = "pending"
	VaultRuntimeActive        = "active"
	VaultRuntimeLocked        = "locked"
	VaultRuntimeCorrupt       = "corrupt"

	SecretKindProvider = "provider"
	SecretKindMCP      = "mcp"
	SecretKindMCPOAuth = "mcp_oauth"

	CredentialReady   = "ready"
	CredentialLocked  = "locked"
	CredentialMissing = "missing"
	CredentialCorrupt = "corrupt"
)

// SecretVaultRecord contains only encrypted key material. The local KEK and
// recovery code are deliberately excluded from this durable representation.
type SecretVaultRecord struct {
	OrganizationID     string
	State              string
	Algorithm          string
	KeyVersion         int
	LocalWrappedDEK    string
	LocalNonce         string
	RecoveryWrappedDEK string
	RecoveryNonce      string
	RecoverySalt       string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type EncryptedSecretRecord struct {
	OrganizationID string
	SecretRef      string
	SecretKind     string
	KeyVersion     int
	Nonce          string
	Ciphertext     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CredentialTarget is non-secret inventory metadata used by diagnostics to
// explain which resource needs attention without exposing credential_ref.
type CredentialTarget struct {
	Kind          string
	ResourceID    string
	Name          string
	ProjectID     string
	CredentialRef string
}

type VaultStatus struct {
	State              string
	Algorithm          string
	KeyVersion         int
	RecoveryConfigured bool
	SecretCount        int64
	Detail             string
	UpdatedAt          time.Time
}

type AIGovernanceSettings struct {
	OrganizationID     string
	ReviewerProviderID string
	ReviewerModelID    string
	Enabled            bool
	TimeoutSeconds     int
	FailClosed         bool
	PolicyVersion      int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type ModelPricingVersion struct {
	OrganizationID string
	ProviderID     string
	ModelID        string
	Currency       string
	InputPer1K     string
	OutputPer1K    string
	EffectiveAt    time.Time
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type UserPreferences struct {
	OrganizationID    string
	UserID            string
	Theme             string
	Density           string
	DefaultPermission string
	RecentProviderID  string
	RecentModelID     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type BootstrapConfiguration struct {
	Profile         string
	ListenAddress   string
	DataRoot        string
	RunConcurrency  int
	DatabaseDriver  string
	TrustedKeysFile string
	WebAccess       WebAccessConfiguration
	ConfigPath      string
	Source          string
	RestartRequired bool
}

type WebAccessConfiguration struct {
	PrivateFetch PrivateFetchConfiguration
}

type PrivateFetchConfiguration struct {
	Enabled            bool
	AllowedOrigins     []WebPrivateOrigin
	AllowedOriginCount int
	OriginsRedacted    bool
}

type UpdateBootstrapConfiguration struct {
	Profile         *string
	ListenAddress   *string
	DataRoot        *string
	RunConcurrency  *int
	DatabaseDriver  *string
	TrustedKeysFile *string
	WebAccess       *WebAccessConfiguration
}
