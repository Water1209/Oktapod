package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type MCPOAuthCoordinator interface {
	Start(context.Context, core.Capability) (core.MCPOAuthSession, error)
	LookupState(context.Context, string) (core.MCPOAuthSession, error)
	Complete(context.Context, string, string, string) (core.MCPOAuthSession, error)
	Status(context.Context, string) (core.MCPOAuthSession, error)
	Cancel(context.Context, string) (core.MCPOAuthSession, error)
}

func (s *Service) WithMCPOAuthCoordinator(coordinator MCPOAuthCoordinator) *Service {
	s.mcpOAuth = coordinator
	return s
}

func (s *Service) StartMCPOAuth(ctx context.Context, capabilityID string) (core.MCPOAuthSession, error) {
	if s.vault == nil || s.vault.Status(ctx).State != core.VaultRuntimeActive {
		return core.MCPOAuthSession{}, &core.ResourceConflictError{Resource: "MCP OAuth", Reason: "database vault must be active before starting authorization"}
	}
	capability, err := s.oauthCapability(ctx, capabilityID)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	if capability.Status != core.CapabilityStatusEnabled {
		return core.MCPOAuthSession{}, &core.ForbiddenError{Message: "MCP must be reviewed and enabled before connecting an account"}
	}
	if capability.CredentialRef == "" {
		capability.CredentialRef = mcpCapabilityCredentialRef(capability.OrganizationID, capability.ScopeType, capability.ScopeID, capability.Name, capability.Version)
		if err := s.repository.SetCapabilityCredentialRef(ctx, s.ScopeFor(ctx), capability.ID, capability.CredentialRef); err != nil {
			return core.MCPOAuthSession{}, err
		}
	}
	session, err := s.mcpOAuth.Start(ctx, capability)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	if session.Status == core.MCPOAuthStatusConnected {
		_, _ = s.ConnectMCP(ctx, capability.ID, "mcp-oauth-restore")
	}
	return session, nil
}

func (s *Service) GetMCPOAuthStatus(ctx context.Context, capabilityID, sessionID string) (core.MCPOAuthSession, error) {
	if _, err := s.oauthCapability(ctx, capabilityID); err != nil {
		return core.MCPOAuthSession{}, err
	}
	session, err := s.mcpOAuth.Status(ctx, sessionID)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	if session.CapabilityID != capabilityID {
		return core.MCPOAuthSession{}, &core.ForbiddenError{Message: "OAuth session does not belong to this capability"}
	}
	return session, nil
}

func (s *Service) CancelMCPOAuth(ctx context.Context, capabilityID, sessionID string) (core.MCPOAuthSession, error) {
	if _, err := s.GetMCPOAuthStatus(ctx, capabilityID, sessionID); err != nil {
		return core.MCPOAuthSession{}, err
	}
	return s.mcpOAuth.Cancel(ctx, sessionID)
}

func (s *Service) CompleteMCPOAuth(ctx context.Context, state, code, issuer string) (core.MCPOAuthSession, error) {
	if s.mcpOAuth == nil {
		return core.MCPOAuthSession{}, errors.New("MCP OAuth is unavailable")
	}
	pending, err := s.mcpOAuth.LookupState(ctx, state)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	if _, err := s.oauthCapability(ctx, pending.CapabilityID); err != nil {
		return core.MCPOAuthSession{}, err
	}
	session, err := s.mcpOAuth.Complete(ctx, state, code, issuer)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	if session.Status == core.MCPOAuthStatusConnected {
		if _, connectErr := s.ConnectMCP(ctx, session.CapabilityID, "mcp-oauth-callback"); connectErr != nil {
			session.Status = core.MCPOAuthStatusFailed
			session.LastError = "OAuth completed, but MCP validation failed"
		}
	}
	return session, nil
}

func (s *Service) CancelMCPOAuthByState(ctx context.Context, state string) (core.MCPOAuthSession, error) {
	if s.mcpOAuth == nil {
		return core.MCPOAuthSession{}, errors.New("MCP OAuth is unavailable")
	}
	pending, err := s.mcpOAuth.LookupState(ctx, state)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	if _, err := s.oauthCapability(ctx, pending.CapabilityID); err != nil {
		return core.MCPOAuthSession{}, err
	}
	return s.mcpOAuth.Cancel(ctx, pending.ID)
}

func (s *Service) oauthCapability(ctx context.Context, capabilityID string) (core.Capability, error) {
	if s.mcpOAuth == nil || s.providerSecrets == nil {
		return core.Capability{}, errors.New("MCP OAuth is unavailable")
	}
	capability, err := s.repository.GetCapability(ctx, s.ScopeFor(ctx), strings.TrimSpace(capabilityID))
	if err != nil {
		return core.Capability{}, err
	}
	if capability.Kind != core.CapabilityKindMCP {
		return core.Capability{}, &core.ValidationError{Field: "capability_id", Message: "capability is not an MCP"}
	}
	definition, err := core.ParseMCPDefinition(capability.ManifestJSON)
	if err != nil || definition.Auth.Type != "oauth" || definition.Transport.Type != "streamable_http" {
		return core.Capability{}, &core.ValidationError{Field: "capability_id", Message: "capability is not a remote OAuth MCP"}
	}
	return capability, nil
}

func mcpCapabilityCredentialRef(organizationID, scopeType, scopeID, name, version string) string {
	refSeed := strings.Join([]string{scopeType, scopeID, name, version}, "\x00")
	refHash := sha256.Sum256([]byte(refSeed))
	return "mcp-capability/" + organizationID + "/" + hex.EncodeToString(refHash[:16])
}
