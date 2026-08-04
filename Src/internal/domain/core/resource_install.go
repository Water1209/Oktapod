package core

import "time"

type MCPInstallCandidate struct {
	Name          string          `json:"name"`
	Title         string          `json:"title,omitempty"`
	Version       string          `json:"version"`
	Description   string          `json:"description,omitempty"`
	RepositoryURL string          `json:"repository_url,omitempty"`
	Definition    MCPDefinitionV1 `json:"mcp_definition"`
}

// ResourceInstallDraft is a side-effect-free resolution result. It is not an
// installed resource: the user must still review the source and permissions,
// provide required secrets through SecretStore, and confirm installation.
type ResourceInstallDraft struct {
	ID              string                `json:"id"`
	Kind            string                `json:"kind"`
	SourceInput     string                `json:"source_input"`
	ResolvedName    string                `json:"resolved_name,omitempty"`
	ResolvedVersion string                `json:"resolved_version,omitempty"`
	MCPDefinition   *MCPDefinitionV1      `json:"mcp_definition,omitempty"`
	Candidates      []MCPInstallCandidate `json:"candidates,omitempty"`
	Status          string                `json:"status"`
	Warnings        []string              `json:"warnings"`
	RequiredSecrets []MCPSecretBinding    `json:"required_secrets"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}
