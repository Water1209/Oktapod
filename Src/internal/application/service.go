package application

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	MaxAssetFileBytes = 100 * 1024 * 1024
	ProjectAssetQuota = 5 * 1024 * 1024 * 1024
)

type Service struct {
	repository       Repository
	profile          string
	endpoint         string
	actor            core.Actor
	org              core.Organization
	modelKey         func() string
	advisor          AdvisorModel
	catalog          ModelCatalog
	capabilityKeys   map[string]ed25519.PublicKey
	database         string
	authorizer       Authorizer
	localPrincipal   LocalPrincipalResolver
	providerSecrets  ProviderSecretStore
	providerCatalogs ProviderCatalogFactory
	chatToolExec     ChatToolExecutor
	workspaceRoot    string
}

const (
	TurnRouteChat = "chat"
	TurnRouteTask = "task"
)

// WithAdvisorModel enables the optional model-backed review path. A nil
// gateway keeps the deterministic, zero-model preview/approval path usable in
// offline Personal deployments and tests.
func (s *Service) WithAdvisorModel(model AdvisorModel) *Service {
	s.advisor = model
	return s
}

// WithModelCatalog wires the provider catalog used by the model picker. A nil
// catalog is valid for offline Personal mode and keeps the rest of the API
// usable when no provider adapter is configured.
func (s *Service) WithModelCatalog(catalog ModelCatalog) *Service {
	s.catalog = catalog
	return s
}

// WithChatToolExecutor wires a read-only workspace executor for chat-mode tool
// use. When set, chat turns can invoke read-only workspace tools (list, read,
// find, grep) to answer questions without creating a full Task/Run.
func (s *Service) WithChatToolExecutor(executor ChatToolExecutor, workspaceRoot string) *Service {
	s.chatToolExec = executor
	s.workspaceRoot = strings.TrimSpace(workspaceRoot)
	return s
}

// WithCapabilityTrustedKeys enables optional Ed25519 verification for signed
// capability manifests. Unsigned manifests retain the existing compatibility
// path; signed manifests with unknown keys are quarantined.
func (s *Service) WithCapabilityTrustedKeys(keys map[string]ed25519.PublicKey) *Service {
	s.capabilityKeys = make(map[string]ed25519.PublicKey, len(keys))
	for id, key := range keys {
		s.capabilityKeys[strings.TrimSpace(id)] = append(ed25519.PublicKey(nil), key...)
	}
	return s
}

func (s *Service) ListModels(ctx context.Context) ([]core.ModelDescriptor, error) {
	if s.catalog == nil {
		return []core.ModelDescriptor{}, nil
	}
	models, err := s.catalog.ListModels(ctx)
	if err != nil {
		code := "MODEL_CATALOG_UNAVAILABLE"
		var coded errorCoder
		if errors.As(err, &coded) && strings.TrimSpace(coded.ErrorCode()) != "" {
			code = coded.ErrorCode()
		}
		return nil, &ModelCatalogUnavailableError{Code: code, Err: err}
	}
	return models, nil
}

func (s *Service) DefaultModel(ctx context.Context, models []core.ModelDescriptor) (string, error) {
	if resolver, ok := s.catalog.(DefaultModelCatalog); ok {
		return resolver.DefaultModel(ctx)
	}
	if len(models) > 0 {
		return models[0].ID, nil
	}
	return "", nil
}

func NewService(repository Repository, profile, endpoint string, actor core.Actor, organization core.Organization, modelKey func() string) *Service {
	localPrincipal := LocalPrincipalResolver{Principal: Principal{OrganizationID: organization.ID, UserID: actor.ID, AuthenticationMethod: AuthenticationLocal, Administrator: profile != core.ProfileTeam}}
	return &Service{
		repository:     repository,
		profile:        profile,
		endpoint:       endpoint,
		actor:          actor,
		org:            organization,
		modelKey:       modelKey,
		database:       "sqlite",
		authorizer:     RepositoryAuthorizer{Repository: repository},
		localPrincipal: localPrincipal,
	}
}

func (s *Service) WithAuthorizer(authorizer Authorizer) *Service {
	if authorizer != nil {
		s.authorizer = authorizer
	}
	return s
}

// WithDatabase annotates health/status responses without changing repository
// behavior. Personal defaults to SQLite; Team can report PostgreSQL when the
// configured adapter is active.
func (s *Service) WithDatabase(name string) *Service {
	if strings.TrimSpace(name) != "" {
		s.database = strings.TrimSpace(name)
	}
	return s
}

func (s *Service) Database() string { return s.database }

func (s *Service) Scope() core.Scope {
	return core.Scope{OrganizationID: s.org.ID, UserID: s.actor.ID}
}

type scopeContextKey struct{}
type adminContextKey struct{}

func WithScope(ctx context.Context, scope core.Scope) context.Context {
	return context.WithValue(ctx, scopeContextKey{}, scope)
}

// ScopeFromContext exposes the validated request scope to infrastructure
// adapters. PostgreSQL uses it to set transaction-local RLS variables; the
// domain and repository APIs still receive Scope explicitly, so this helper
// is only a defense-in-depth transport and never an authorization decision.
func ScopeFromContext(ctx context.Context) (core.Scope, bool) {
	scope, ok := ctx.Value(scopeContextKey{}).(core.Scope)
	if !ok || scope.OrganizationID == "" || scope.UserID == "" {
		return core.Scope{}, false
	}
	return scope, true
}

func WithAdmin(ctx context.Context, admin bool) context.Context {
	return context.WithValue(ctx, adminContextKey{}, admin)
}

func IsAdmin(ctx context.Context) bool {
	admin, _ := ctx.Value(adminContextKey{}).(bool)
	return admin
}

func (s *Service) ScopeFor(ctx context.Context) core.Scope {
	if principal, ok := PrincipalFromContext(ctx); ok && principal.OrganizationID == s.org.ID {
		return principal.Scope()
	}
	principal, err := s.localPrincipal.ResolvePrincipal(ctx, PrincipalCredential{Method: AuthenticationLocal})
	if err == nil {
		return principal.Scope()
	}
	return s.Scope()
}

func (s *Service) principalFor(ctx context.Context) (Principal, error) {
	if principal, ok := PrincipalFromContext(ctx); ok && principal.OrganizationID == s.org.ID {
		return principal, nil
	}
	return s.localPrincipal.ResolvePrincipal(ctx, PrincipalCredential{Method: AuthenticationLocal})
}

func (s *Service) authorize(ctx context.Context, action, resourceType, resourceID string) error {
	principal, err := s.principalFor(ctx)
	if err != nil {
		return err
	}
	return s.authorizer.Authorize(ctx, principal, AccessRequest{Action: action, ResourceType: resourceType, ResourceID: resourceID})
}

func (s *Service) CreateAuthSession(ctx context.Context, input core.CreateAuthSessionInput) (core.AuthSession, error) {
	if input.UserID == "" {
		input.UserID = s.actor.ID
	}
	if input.ExpiresInSeconds <= 0 || input.ExpiresInSeconds > 30*24*60*60 {
		input.ExpiresInSeconds = 8 * 60 * 60
	}
	return s.repository.CreateAuthSession(ctx, s.org.ID, input)
}

func (s *Service) ConfigureLocalPassword(ctx context.Context, password string) error {
	return s.repository.ConfigureLocalPassword(ctx, s.org.ID, s.actor.ID, password)
}

func (s *Service) AuthenticateLocal(ctx context.Context, input core.LocalLoginInput) (core.AuthSession, error) {
	return s.repository.AuthenticateLocal(ctx, s.org.ID, input)
}

func (s *Service) RegisterLocalUser(ctx context.Context, input core.LocalRegisterInput) (core.AuthSession, error) {
	return s.repository.RegisterLocalUser(ctx, s.org.ID, input)
}

func (s *Service) ResolveAuthSession(ctx context.Context, token string) (core.AuthSession, error) {
	return s.repository.ResolveAuthSession(ctx, token)
}

func (s *Service) RevokeAuthSession(ctx context.Context, sessionID string) error {
	return s.repository.RevokeAuthSession(ctx, sessionID, s.org.ID)
}

func (s *Service) Profile() string { return s.profile }

func (s *Service) Bootstrap(ctx context.Context) (core.Bootstrap, error) {
	if err := s.repository.Ping(ctx); err != nil {
		return core.Bootstrap{}, err
	}
	scope := s.ScopeFor(ctx)
	actor, err := s.repository.GetActor(ctx, scope)
	if err != nil {
		return core.Bootstrap{}, err
	}
	return core.Bootstrap{
		Profile:         s.profile,
		ModelEndpoint:   s.endpoint,
		ModelConfigured: strings.TrimSpace(s.modelKey()) != "",
		Actor:           actor,
		Organization:    s.org,
		Capabilities: core.RuntimeCapabilities{
			OS:         runtime.GOOS,
			Sandbox:    "restricted-native",
			Filesystem: "workspace-scoped",
			Command:    "structured-no-shell",
			Network:    "search-only-fixed-provider",
			Browser:    "not-configured",
			MCP:        "approval-gated",
		},
	}, nil
}

func (s *Service) CreateProject(ctx context.Context, input core.CreateProjectInput, idempotencyKey string) (core.Project, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.DefaultModel = strings.TrimSpace(input.DefaultModel)
	input.FallbackModel = strings.TrimSpace(input.FallbackModel)
	if input.Kind == "" {
		input.Kind = core.ProjectKindPersonal
	}
	if err := core.ValidateCreateProject(input); err != nil {
		return core.Project{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Project{}, err
	}
	idempotency := core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(input)}
	return retryTransientSQLiteValue(ctx, func() (core.Project, error) {
		return s.repository.CreateProject(ctx, s.ScopeFor(ctx), input, idempotency)
	})
}

func (s *Service) GetProject(ctx context.Context, projectID string) (core.Project, error) {
	if err := s.authorize(ctx, "read", "project", projectID); err != nil {
		return core.Project{}, err
	}
	return s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID)
}

func (s *Service) UpdateProjectSettings(ctx context.Context, projectID string, input core.UpdateProjectSettingsInput, idempotencyKey string) (core.Project, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return core.Project{}, &core.ValidationError{Field: "project_id", Message: "is required"}
	}
	current, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID)
	if err != nil {
		return core.Project{}, err
	}
	if input.DefaultModel != nil {
		value := strings.TrimSpace(*input.DefaultModel)
		input.DefaultModel = &value
	} else {
		value := current.DefaultModel
		input.DefaultModel = &value
	}
	if input.FallbackModel != nil {
		value := strings.TrimSpace(*input.FallbackModel)
		input.FallbackModel = &value
	} else {
		value := current.FallbackModel
		input.FallbackModel = &value
	}
	if input.MaxModelCalls == nil {
		value := current.MaxModelCalls
		input.MaxModelCalls = &value
	}
	if input.MaxTokens == nil {
		value := current.MaxTokens
		input.MaxTokens = &value
	}
	if *input.MaxModelCalls < 0 || *input.MaxModelCalls > 1000 {
		return core.Project{}, &core.ValidationError{Field: "max_model_calls", Message: "must be between 0 and 1000; zero means unlimited"}
	}
	if *input.MaxTokens < 0 || *input.MaxTokens > 10_000_000 {
		return core.Project{}, &core.ValidationError{Field: "max_tokens", Message: "must be between 0 and 10000000; zero means unlimited"}
	}
	if (*input.MaxModelCalls == 0) != (*input.MaxTokens == 0) {
		return core.Project{}, &core.ValidationError{Field: "resource_budget", Message: "max_model_calls and max_tokens must both be zero (unlimited) or both be greater than zero"}
	}
	for field, value := range map[string]string{"default_model": *input.DefaultModel, "fallback_model": *input.FallbackModel} {
		if len([]rune(value)) > 200 || strings.ContainsAny(value, "\r\n") {
			return core.Project{}, &core.ValidationError{Field: field, Message: "must be a single-line model id with at most 200 characters"}
		}
	}
	idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Project{}, err
	}
	return s.repository.UpdateProjectSettings(ctx, s.ScopeFor(ctx), projectID, input, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(struct {
		ProjectID string                          `json:"project_id"`
		Input     core.UpdateProjectSettingsInput `json:"input"`
	}{ProjectID: projectID, Input: input})})
}

func (s *Service) ListProjects(ctx context.Context) ([]core.Project, error) {
	return s.repository.ListProjects(ctx, s.ScopeFor(ctx), 100)
}

func (s *Service) AddProjectMember(ctx context.Context, projectID string, input core.AddProjectMemberInput, correlationID, idempotencyKey string) (core.ProjectMember, error) {
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Role = strings.ToLower(strings.TrimSpace(input.Role))
	if err := core.ValidateAddProjectMember(input); err != nil {
		return core.ProjectMember{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.ProjectMember{}, err
	}
	request := struct {
		ProjectID string                     `json:"project_id"`
		Input     core.AddProjectMemberInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.AddProjectMember(ctx, s.ScopeFor(ctx), projectID, input, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListProjectMembers(ctx context.Context, projectID string) ([]core.ProjectMember, error) {
	return s.repository.ListProjectMembers(ctx, s.ScopeFor(ctx), projectID)
}

func (s *Service) RemoveProjectMember(ctx context.Context, projectID, userID, correlationID, idempotencyKey string) error {
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return err
	}
	request := struct {
		ProjectID string `json:"project_id"`
		UserID    string `json:"user_id"`
	}{ProjectID: projectID, UserID: userID}
	return s.repository.RemoveProjectMember(ctx, s.ScopeFor(ctx), projectID, userID, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) CreateProjectInvitation(ctx context.Context, projectID string, input core.CreateProjectInvitationInput, correlationID, idempotencyKey string) (core.ProjectInvitation, error) {
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Role = strings.ToLower(strings.TrimSpace(input.Role))
	if input.ExpiresIn == 0 {
		input.ExpiresIn = 7 * 24 * 60 * 60
	}
	if err := core.ValidateCreateProjectInvitation(input); err != nil {
		return core.ProjectInvitation{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	request := struct {
		ProjectID string                            `json:"project_id"`
		Input     core.CreateProjectInvitationInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.CreateProjectInvitation(ctx, s.ScopeFor(ctx), projectID, input, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListProjectInvitations(ctx context.Context, projectID string) ([]core.ProjectInvitation, error) {
	return s.repository.ListProjectInvitations(ctx, s.ScopeFor(ctx), projectID)
}

func (s *Service) AcceptProjectInvitation(ctx context.Context, token, correlationID, idempotencyKey string) (core.ProjectInvitation, error) {
	token = strings.TrimSpace(token)
	if len(token) < 32 || len(token) > 256 {
		return core.ProjectInvitation{}, &core.ValidationError{Field: "token", Message: "is invalid"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.ProjectInvitation{}, err
	}
	request := struct {
		Token string `json:"token"`
	}{Token: token}
	return s.repository.AcceptProjectInvitation(ctx, token, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) RevokeProjectInvitation(ctx context.Context, invitationID, correlationID, idempotencyKey string) error {
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return err
	}
	request := struct {
		InvitationID string `json:"invitation_id"`
	}{InvitationID: invitationID}
	return s.repository.RevokeProjectInvitation(ctx, s.ScopeFor(ctx), invitationID, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) CreateTask(ctx context.Context, projectID string, input core.CreateTaskInput, correlationID, idempotencyKey string) (core.Task, error) {
	input.ConversationID = strings.TrimSpace(input.ConversationID)
	input.Title = strings.TrimSpace(input.Title)
	input.Goal = strings.TrimSpace(input.Goal)
	input.Acceptance = strings.TrimSpace(input.Acceptance)
	input.Model = strings.TrimSpace(input.Model)
	input.FallbackModel = strings.TrimSpace(input.FallbackModel)
	input.SceneOverride = strings.ToLower(strings.TrimSpace(input.SceneOverride))
	input.PermissionMode = strings.ToLower(strings.TrimSpace(input.PermissionMode))
	input.ContextAssetIDs = normalizeIDs(input.ContextAssetIDs)
	input.ContextMemoryIDs = normalizeIDs(input.ContextMemoryIDs)
	input.SkillID = strings.TrimSpace(input.SkillID)
	input.SkillArgs = strings.TrimSpace(input.SkillArgs)
	requestedMode := input.Mode
	if input.SkillID != "" {
		if input.Mode == core.ModeAgentTeam || strings.TrimSpace(input.TeamDefinitionID) != "" || len(input.TeamRoles) > 0 {
			return core.Task{}, &core.ValidationError{Field: "skill_id", Message: "explicit Skill invocation cannot be combined with Agent Team execution"}
		}
		input.Mode = core.ModeSingleAgent
	}
	if input.PermissionMode == "" {
		input.PermissionMode = core.PermissionModeAsk
	}
	if input.PermissionMode == core.PermissionModeDefault {
		input.PermissionMode = core.PermissionModeAsk
	}
	project, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID)
	if err != nil {
		return core.Task{}, err
	}
	if input.MaxModelCalls == 0 {
		input.MaxModelCalls = project.MaxModelCalls
	}
	if input.MaxTokens == 0 {
		input.MaxTokens = project.MaxTokens
	}
	scene := project.Scene
	if input.SceneOverride != "" {
		scene = input.SceneOverride
	}
	if input.Title == "" {
		input.Title = titleFromGoal(input.Goal)
	}
	classificationReason := "user_selected"
	if input.SkillID != "" {
		classificationReason = "explicit_skill_invocation"
	}
	if input.Mode == core.ModeAuto {
		input.Mode, classificationReason = classifyTaskMode(input)
	}
	if err := core.ValidateCreateTask(input); err != nil {
		return core.Task{}, err
	}
	if len(input.ContextAssetIDs) > 0 {
		for _, assetID := range input.ContextAssetIDs {
			asset, assetErr := s.repository.GetAsset(ctx, s.ScopeFor(ctx), assetID)
			if assetErr != nil || asset.ProjectID != projectID || asset.Kind != core.AssetKindFile {
				return core.Task{}, &core.ForbiddenError{Message: "selected context asset is not visible in this project"}
			}
		}
	}
	if len(input.ContextMemoryIDs) > 0 {
		memories, memoryErr := s.repository.ListMemories(ctx, s.ScopeFor(ctx), projectID, "", 500)
		if memoryErr != nil {
			return core.Task{}, memoryErr
		}
		visible := make(map[string]struct{}, len(memories))
		for _, memory := range memories {
			visible[memory.ID] = struct{}{}
		}
		for _, memoryID := range input.ContextMemoryIDs {
			if _, ok := visible[memoryID]; !ok {
				return core.Task{}, &core.ForbiddenError{Message: "selected context memory is not visible in this project"}
			}
		}
	}
	idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Task{}, err
	}
	snapshot := map[string]any{
		"schema_version":        1,
		"profile":               s.profile,
		"model_endpoint":        s.endpoint,
		"mode":                  input.Mode,
		"requested_mode":        requestedMode,
		"classification_reason": classificationReason,
		"scene":                 scene,
		"permission_mode":       input.PermissionMode,
		"scene_policy": map[string]any{
			"workspace_write":   true,
			"workspace_command": scene == core.SceneCode,
			"network":           false,
		},
	}
	if input.MaxModelCalls > 0 {
		snapshot["max_model_calls"] = input.MaxModelCalls
	}
	if input.MaxTokens > 0 {
		snapshot["max_tokens"] = input.MaxTokens
	}
	if input.Model != "" {
		snapshot["model_override"] = input.Model
	}
	if input.FallbackModel != "" {
		snapshot["fallback_model_override"] = input.FallbackModel
	}
	if input.Model == "" && project.DefaultModel != "" {
		snapshot["project_default_model"] = project.DefaultModel
	}
	if input.FallbackModel == "" && project.FallbackModel != "" {
		snapshot["project_fallback_model"] = project.FallbackModel
	}
	if input.MaxTokens > 0 {
		snapshot["max_tokens"] = input.MaxTokens
	}
	// Only connected, enabled MCP capabilities are snapshotted. This keeps
	// model context deterministic and prevents a later capability change from
	// silently widening an in-flight Run.
	capabilities, listErr := s.repository.ListCapabilities(ctx, s.ScopeFor(ctx), projectID, 500)
	if listErr != nil && input.SkillID != "" {
		return core.Task{}, listErr
	}
	if listErr == nil {
		mcpSnapshots := make([]map[string]any, 0)
		var explicitSkill *core.Capability
		for _, capability := range capabilities {
			if capability.ID == input.SkillID {
				candidate := capability
				explicitSkill = &candidate
			}
			if capability.Kind != core.CapabilityKindMCP || capability.Status != core.CapabilityStatusEnabled {
				continue
			}
			health, healthErr := s.repository.GetMCPHealth(ctx, s.ScopeFor(ctx), capability.ID)
			if healthErr != nil || health.Status != core.MCPStatusConnected {
				continue
			}
			var tools []map[string]any
			if json.Unmarshal([]byte(health.ToolsJSON), &tools) != nil {
				continue
			}
			mcpSnapshots = append(mcpSnapshots, map[string]any{"id": capability.ID, "name": capability.Name, "manifest_json": capability.ManifestJSON, "tools": tools})
		}
		if len(mcpSnapshots) > 0 {
			snapshot["mcp_capabilities"] = mcpSnapshots
		}
		// Skills are frozen as metadata plus their manifest at Run creation, but
		// their instruction body is loaded into the model prompt only when the
		// deterministic runtime relevance plan selects them. This keeps ordinary
		// tasks cheap while preserving reproducibility if a Skill is edited later.
		skillSnapshots := make([]map[string]any, 0)
		for _, capability := range capabilities {
			if capability.Kind != core.CapabilityKindSkill || capability.Status != core.CapabilityStatusEnabled {
				continue
			}
			skillSnapshots = append(skillSnapshots, map[string]any{"id": capability.ID, "name": capability.Name, "version": capability.Version, "content_hash": capability.ContentHash, "manifest_json": capability.ManifestJSON})
		}
		if len(skillSnapshots) > 0 {
			snapshot["skill_capabilities"] = skillSnapshots
		}
		if input.SkillID != "" {
			if explicitSkill == nil || explicitSkill.ProjectID != projectID {
				return core.Task{}, &core.ValidationError{Field: "skill_id", Message: "Skill is not available in this project"}
			}
			if explicitSkill.Kind != core.CapabilityKindSkill {
				return core.Task{}, &core.ValidationError{Field: "skill_id", Message: "selected capability is not a Skill"}
			}
			if explicitSkill.Status != core.CapabilityStatusEnabled {
				return core.Task{}, &core.ForbiddenError{Message: "selected Skill is not enabled"}
			}
			snapshot["explicit_skill_invocation"] = map[string]any{"skill_id": explicitSkill.ID, "skill_name": explicitSkill.Name, "skill_args": input.SkillArgs}
		}
	}
	if strings.TrimSpace(input.AgentID) != "" {
		agents, listErr := s.repository.ListAgentDefinitions(ctx, s.ScopeFor(ctx), projectID, 100)
		if listErr != nil {
			return core.Task{}, listErr
		}
		var selected *core.AgentDefinition
		for index := range agents {
			if agents[index].ID == input.AgentID {
				selected = &agents[index]
				break
			}
		}
		if selected == nil {
			return core.Task{}, &core.ValidationError{Field: "agent_id", Message: "agent is not available in this project"}
		}
		if selected.Status != core.CapabilityStatusEnabled {
			return core.Task{}, &core.ForbiddenError{Message: "selected Agent is not enabled"}
		}
		snapshot["agent"] = map[string]any{"id": selected.ID, "name": selected.Name, "version": selected.Version, "model": selected.Model, "fallback_model": selected.FallbackModel, "config_json": selected.ConfigJSON}
		snapshot["agent_id"] = selected.ID
	}
	if strings.TrimSpace(input.TeamDefinitionID) != "" {
		if input.Mode != core.ModeAgentTeam {
			return core.Task{}, &core.ValidationError{Field: "team_definition_id", Message: "team definition requires agent_team mode"}
		}
		teams, listErr := s.repository.ListTeamDefinitions(ctx, s.ScopeFor(ctx), projectID, 100)
		if listErr != nil {
			return core.Task{}, listErr
		}
		var selected *core.TeamDefinition
		for index := range teams {
			if teams[index].ID == input.TeamDefinitionID {
				selected = &teams[index]
				break
			}
		}
		if selected == nil {
			return core.Task{}, &core.ValidationError{Field: "team_definition_id", Message: "team definition is not available in this project"}
		}
		if selected.Status != core.CapabilityStatusEnabled {
			return core.Task{}, &core.ForbiddenError{Message: "selected TeamDefinition is not enabled"}
		}
		input.TeamRoles = selected.Roles
		var config map[string]any
		if json.Unmarshal([]byte(selected.ConfigJSON), &config) == nil {
			if input.MaxModelCalls == 0 {
				if value, ok := config["max_model_calls"].(float64); ok {
					input.MaxModelCalls = int64(value)
				}
			}
			if input.MaxTokens == 0 {
				if value, ok := config["max_tokens"].(float64); ok {
					input.MaxTokens = int64(value)
				}
			}
		}
		snapshot["team_definition"] = map[string]any{"id": selected.ID, "name": selected.Name, "version": selected.Version, "roles": selected.Roles, "config_json": selected.ConfigJSON}
		snapshot["team_definition_id"] = selected.ID
	}
	if input.MaxModelCalls > 0 {
		snapshot["max_model_calls"] = input.MaxModelCalls
	}
	if len(input.TeamRoles) > 0 {
		snapshot["team_roles"] = input.TeamRoles
	}
	if len(input.ContextAssetIDs) > 0 {
		snapshot["context_asset_ids"] = input.ContextAssetIDs
	}
	if len(input.ContextMemoryIDs) > 0 {
		snapshot["context_memory_ids"] = input.ContextMemoryIDs
	}
	if input.ConversationID != "" {
		conversationMessages, contextErr := s.conversationTranscriptForPrompt(ctx, input.ConversationID, "", input.Model, input.FallbackModel)
		if contextErr != nil {
			return core.Task{}, contextErr
		}
		if len(conversationMessages) > 0 {
			snapshot["conversation_messages"] = conversationMessages
			snapshot["conversation_message_count"] = len(conversationMessages)
		}
	}
	if err := s.freezeRunModelSnapshot(ctx, snapshot, input); err != nil {
		return core.Task{}, err
	}
	request := struct {
		ProjectID string               `json:"project_id"`
		Input     core.CreateTaskInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	idempotency := core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)}
	return retryTransientSQLiteValue(ctx, func() (core.Task, error) {
		return s.repository.CreateTask(ctx, s.ScopeFor(ctx), projectID, input, snapshot, correlationID, idempotency)
	})
}

// AppendConversationMessage starts a new executable Task while preserving the
// conversation's server-sequenced history. Each turn still receives its own
// Run, budget, approvals and recovery state.
func (s *Service) AppendConversationMessage(ctx context.Context, conversationID string, input core.CreateTaskInput, correlationID, idempotencyKey string) (core.Task, error) {
	conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), strings.TrimSpace(conversationID))
	if err != nil {
		return core.Task{}, err
	}
	input.ConversationID = conversation.ID
	if strings.TrimSpace(input.Visibility) == "" {
		input.Visibility = conversation.Visibility
	}
	if strings.TrimSpace(input.Title) == "" {
		input.Title = conversation.Title
	}
	return s.CreateTask(ctx, conversation.ProjectID, input, correlationID, idempotencyKey)
}

// SubmitProjectTurn is the single entry point used by the project composer.
// A chat turn writes messages only; a task turn delegates to the existing
// immutable Task/Run creation path. Auto classification is model-backed and
// never silently falls back to keyword rules.
func (s *Service) SubmitProjectTurn(ctx context.Context, projectID string, input core.ProjectTurnInput, correlationID, idempotencyKey string) (core.ProjectTurnResult, error) {
	return s.submitProjectTurn(ctx, projectID, input, correlationID, idempotencyKey, nil, nil)
}

// SubmitProjectTurnStream preserves the same routing, persistence and
// idempotency semantics as SubmitProjectTurn while exposing the resolved route
// and assistant answer deltas to an HTTP streaming transport.
func (s *Service) SubmitProjectTurnStream(ctx context.Context, projectID string, input core.ProjectTurnInput, correlationID, idempotencyKey string, onRoute func(string) error, onDelta func(string) error) (core.ProjectTurnResult, error) {
	return s.submitProjectTurn(ctx, projectID, input, correlationID, idempotencyKey, onRoute, onDelta)
}

func (s *Service) submitProjectTurn(ctx context.Context, projectID string, input core.ProjectTurnInput, correlationID, idempotencyKey string, onRoute func(string) error, onDelta func(string) error) (core.ProjectTurnResult, error) {
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "content", Message: "is required"}
	}
	if len([]rune(content)) > 20000 {
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "content", Message: "must not exceed 20000 characters"}
	}
	project, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID)
	if err != nil {
		return core.ProjectTurnResult{}, err
	}
	if strings.TrimSpace(input.Model) == "" {
		input.Model = strings.TrimSpace(project.DefaultModel)
	}
	if strings.TrimSpace(input.FallbackModel) == "" {
		input.FallbackModel = strings.TrimSpace(project.FallbackModel)
	}
	input.SkillID = strings.TrimSpace(input.SkillID)
	input.SkillArgs = strings.TrimSpace(input.SkillArgs)
	input.QueueKind = strings.ToLower(strings.TrimSpace(input.QueueKind))
	if input.QueueKind == "" {
		input.QueueKind = "follow_up"
	}
	if input.QueueKind != "follow_up" && input.QueueKind != "steering" {
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "queue_kind", Message: "must be follow_up or steering"}
	}
	if input.SkillArgs != "" && input.SkillID == "" {
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "skill_args", Message: "requires skill_id"}
	}
	mode := strings.ToLower(strings.TrimSpace(input.Mode))
	if mode == "" {
		mode = core.ModeAuto
	}
	if mode != core.ModeAuto && mode != core.ModeAnswer && mode != core.ModeSingleAgent && mode != core.ModeAgentTeam {
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "mode", Message: "is not supported"}
	}
	if input.SkillID != "" {
		mode = core.ModeSingleAgent
	}
	conversationID := strings.TrimSpace(input.ConversationID)
	visibility := strings.ToLower(strings.TrimSpace(input.Visibility))
	if conversationID != "" {
		conversation, conversationErr := s.repository.GetConversation(ctx, s.ScopeFor(ctx), conversationID)
		if conversationErr != nil {
			return core.ProjectTurnResult{}, conversationErr
		}
		if conversation.ProjectID != projectID {
			return core.ProjectTurnResult{}, &core.ForbiddenError{Message: "conversation is not in this project"}
		}
		if visibility == "" {
			visibility = conversation.Visibility
		}
	}
	if visibility == "" {
		visibility = core.VisibilityPrivate
	}
	if visibility != core.VisibilityPrivate && visibility != core.VisibilitySelected && visibility != core.VisibilityProject {
		return core.ProjectTurnResult{}, &core.ValidationError{Field: "visibility", Message: "is not supported"}
	}
	if compactInstructions, compactCommand := parseCompactCommand(content); compactCommand {
		if conversationID == "" {
			return core.ProjectTurnResult{}, &core.ValidationError{Field: "conversation_id", Message: "is required for /compact"}
		}
		if onRoute != nil {
			if err := onRoute(TurnRouteChat); err != nil {
				return core.ProjectTurnResult{}, err
			}
		}
		outcome, err := s.CompactConversation(ctx, conversationID, compactInstructions, input.Model, input.FallbackModel)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		assistant := "当前上下文低于保留预算，无需压缩。"
		if outcome.Compaction != nil {
			assistant = fmt.Sprintf("上下文已压缩：约 %d → %d Token，保留最近 %d 条消息；原始消息仍完整保留，可继续审计。", outcome.BeforeTokens, outcome.AfterTokens, outcome.Retained)
		}
		if onDelta != nil {
			if err := onDelta(assistant); err != nil {
				return core.ProjectTurnResult{}, err
			}
		}
		idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		return s.repository.AppendChatTurn(ctx, s.ScopeFor(ctx), projectID, inputWithTurnDefaults(input, content, conversationID, visibility), assistant, nil, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(struct {
			ProjectID string
			Input     core.ProjectTurnInput
		}{projectID, input})})
	}

	if mode == core.ModeAuto && conversationID != "" && isTaskRecoveryIntent(content) {
		tasks, listErr := s.repository.ListTasks(ctx, s.ScopeFor(ctx), projectID, 100)
		if listErr != nil {
			return core.ProjectTurnResult{}, listErr
		}
		for _, candidate := range tasks {
			if candidate.ConversationID != conversationID {
				continue
			}
			// Only the newest task in this conversation defines the recovery
			// context. Never revive an older failure behind a newer task.
			if candidate.Status != core.StatusFailed && candidate.Status != core.StatusBlocked &&
				candidate.Status != core.StatusQueued && candidate.Status != core.StatusRunning {
				break
			}
			if onRoute != nil {
				if routeErr := onRoute(TurnRouteTask); routeErr != nil {
					return core.ProjectTurnResult{}, routeErr
				}
			}
			updated := candidate
			if candidate.Status == core.StatusFailed || candidate.Status == core.StatusBlocked {
				var retryErr error
				updated, retryErr = s.ControlTask(ctx, candidate.ID, core.ControlTaskInput{Command: core.TaskCommandRetry}, correlationID, idempotencyKey)
				if retryErr != nil {
					return core.ProjectTurnResult{}, retryErr
				}
			}
			assistant := "正在继续当前任务；原执行计划、Skill 和已完成进度保持不变。"
			if onDelta != nil {
				if deltaErr := onDelta(assistant); deltaErr != nil {
					return core.ProjectTurnResult{}, deltaErr
				}
			}
			idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
			if err != nil {
				return core.ProjectTurnResult{}, err
			}
			turnInput := inputWithTurnDefaults(input, content, conversationID, visibility)
			turnInput.TaskID = updated.ID
			result, appendErr := s.repository.AppendChatTurn(ctx, s.ScopeFor(ctx), projectID, turnInput, assistant, nil, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(struct {
				ProjectID string
				Input     core.ProjectTurnInput
			}{projectID, turnInput})})
			if appendErr != nil {
				return core.ProjectTurnResult{}, appendErr
			}
			result.Route = TurnRouteTask
			result.Task = &updated
			return result, nil
		}
	}

	var classification core.ModelResult
	route := TurnRouteTask
	classificationReason := "user_selected_task"
	if mode == core.ModeAnswer {
		route = TurnRouteChat
		classificationReason = "user_selected_chat"
	} else if mode == core.ModeAuto {
		var classifyErr error
		classification, classifyErr = s.classifyProjectTurn(ctx, project, content, input.Model, input.FallbackModel, input.ProviderID)
		if classifyErr != nil {
			// Classification is a best-effort router. When the model call fails
			// (rate limit, timeout, empty response), fall back to chat instead of
			// blocking the user's turn with a hard error. Chat mode streams the
			// answer directly and is the safer default for arbitrary input.
			route, classificationReason = TurnRouteChat, "classify_fallback_chat"
		} else {
			route, classificationReason = parseTurnRoute(classification.Content)
			if route == "" {
				route, classificationReason = TurnRouteChat, "classify_unparsable_chat"
			}
		}
	}
	if onRoute != nil {
		if err := onRoute(route); err != nil {
			return core.ProjectTurnResult{}, err
		}
	}
	if route == TurnRouteChat {
		idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
		if err != nil {
			return core.ProjectTurnResult{}, err
		}
		var answer core.ModelResult
		var answerErr error
		if onDelta != nil {
			answer, answerErr = s.answerProjectTurnStream(ctx, project, conversationID, content, input.Model, input.FallbackModel, input.ProviderID, onDelta)
		} else {
			answer, answerErr = s.answerProjectTurn(ctx, project, conversationID, content, input.Model, input.FallbackModel, input.ProviderID)
		}
		if answerErr != nil {
			return core.ProjectTurnResult{}, answerErr
		}
		calls := []core.ConversationModelCall{{Purpose: "answer", Model: answer}}
		if classification.CallID != "" {
			calls = append([]core.ConversationModelCall{{Purpose: "classification", Model: classification}}, calls...)
		}
		return s.repository.AppendChatTurn(ctx, s.ScopeFor(ctx), projectID, inputWithTurnDefaults(input, content, conversationID, visibility), answer.Content, calls, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(struct {
			ProjectID string
			Input     core.ProjectTurnInput
		}{projectID, input})})
	}

	// A conversation owns at most one active task. Additional task turns are
	// persisted as follow-ups and become visible immediately through the queue
	// API; the worker starts them after the active task reaches a terminal state.
	if conversationID != "" {
		hasActive, activeTaskID, activeErr := s.repository.HasActiveTask(ctx, s.ScopeFor(ctx), conversationID)
		if activeErr != nil {
			return core.ProjectTurnResult{}, activeErr
		}
		if hasActive {
			queuedInput := inputWithTurnDefaults(input, content, conversationID, visibility)
			queuedInput.Mode = mode
			inputJSON, marshalErr := json.Marshal(queuedInput)
			if marshalErr != nil {
				return core.ProjectTurnResult{}, fmt.Errorf("encode queued conversation turn: %w", marshalErr)
			}
			queueItem, enqueueErr := s.repository.EnqueueConversationTurn(ctx, s.ScopeFor(ctx), projectID, conversationID, activeTaskID, content, string(inputJSON), input.QueueKind)
			if enqueueErr != nil {
				return core.ProjectTurnResult{}, enqueueErr
			}
			conversation, conversationErr := s.repository.GetConversation(ctx, s.ScopeFor(ctx), conversationID)
			if conversationErr != nil {
				return core.ProjectTurnResult{}, conversationErr
			}
			messages, messagesErr := s.repository.ListMessages(ctx, s.ScopeFor(ctx), conversationID, 0, 200)
			if messagesErr != nil {
				return core.ProjectTurnResult{}, messagesErr
			}
			activeTask, taskErr := s.repository.GetTask(ctx, s.ScopeFor(ctx), activeTaskID)
			if taskErr != nil {
				return core.ProjectTurnResult{}, taskErr
			}
			return core.ProjectTurnResult{Route: TurnRouteTask, Conversation: conversation, Messages: messages, Task: &activeTask, Queued: true, QueueItem: &queueItem}, nil
		}
	}
	taskGoal := content
	if input.SkillID != "" && input.SkillArgs != "" {
		// The slash command selects the skill; only its arguments are the user's
		// actual goal. Keeping "/skill-name" in the goal makes path-aware models
		// mistake it for an absolute workspace path and triggers a policy denial.
		taskGoal = input.SkillArgs
	}
	taskInput := core.CreateTaskInput{ConversationID: conversationID, Title: titleFromGoal(taskGoal), Goal: taskGoal, Acceptance: input.Acceptance, Mode: mode, Visibility: visibility, PermissionMode: input.PermissionMode, SceneOverride: input.SceneOverride, Model: input.Model, FallbackModel: input.FallbackModel, ProviderID: input.ProviderID, Thinking: input.Thinking, MaxModelCalls: input.MaxModelCalls, MaxTokens: input.MaxTokens, AgentID: input.AgentID, TeamDefinitionID: input.TeamDefinitionID, TeamRoles: input.TeamRoles, ContextAssetIDs: input.ContextAssetIDs, ContextMemoryIDs: input.ContextMemoryIDs, SkillID: input.SkillID, SkillArgs: input.SkillArgs}
	if mode == core.ModeAuto {
		taskInput.Mode = core.ModeSingleAgent
	}
	task, taskErr := s.CreateTask(ctx, projectID, taskInput, correlationID, idempotencyKey)
	if taskErr != nil {
		return core.ProjectTurnResult{}, taskErr
	}
	if classification.CallID != "" && task.ConversationID != "" {
		if recordErr := s.repository.RecordConversationModelCalls(ctx, s.ScopeFor(ctx), projectID, task.ConversationID, []core.ConversationModelCall{{Purpose: "classification:" + classificationReason, Model: classification}}); recordErr != nil {
			return core.ProjectTurnResult{}, recordErr
		}
	}
	conversation, conversationErr := s.repository.GetConversation(ctx, s.ScopeFor(ctx), task.ConversationID)
	if conversationErr != nil {
		return core.ProjectTurnResult{}, conversationErr
	}
	messages, messagesErr := s.repository.ListMessages(ctx, s.ScopeFor(ctx), task.ConversationID, 0, 200)
	if messagesErr != nil {
		return core.ProjectTurnResult{}, messagesErr
	}
	return core.ProjectTurnResult{Route: TurnRouteTask, Conversation: conversation, Messages: messages, Task: &task}, nil
}

func inputWithTurnDefaults(input core.ProjectTurnInput, content, conversationID, visibility string) core.ProjectTurnInput {
	input.Content, input.ConversationID, input.Visibility = content, conversationID, visibility
	return input
}

func isTaskRecoveryIntent(content string) bool {
	if len([]rune(content)) > 64 {
		return false
	}
	normalized := strings.ToLower(strings.TrimSpace(content))
	replacer := strings.NewReplacer(
		" ", "", "\t", "", "\r", "", "\n", "",
		"，", "", ",", "", "。", "", ".", "", "；", "", ";", "",
		"：", "", ":", "", "！", "", "!", "",
	)
	normalized = replacer.Replace(normalized)
	switch normalized {
	case "继续", "继续任务", "继续执行", "继续原任务",
		"重试", "重试任务", "重新执行", "重新执行任务",
		"重新规划任务继续", "重新规划并继续", "重新规划任务并继续",
		"重新归属任务继续", "从失败处继续", "从上次停止的位置继续",
		"resume", "resumetask", "retry", "retrytask", "continue", "continuetask":
		return true
	default:
		return false
	}
}

func (s *Service) completeProjectTurn(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int, model, fallback, providerID string) (core.ModelResult, error) {
	if s.advisor == nil {
		return core.ModelResult{}, &ModelTurnUnavailableError{Err: errors.New("no model provider is configured")}
	}
	var result core.ModelResult
	var err error
	if turnModel, ok := s.advisor.(ProviderProjectTurnModel); ok {
		result, err = turnModel.CompleteTurnWithModelRef(ctx, systemPrompt, userPrompt, maxOutputTokens, core.ModelRef{ProviderID: providerID, ModelID: model}, fallback)
	} else if turnModel, ok := s.advisor.(ProjectTurnModel); ok {
		result, err = turnModel.CompleteTurn(ctx, systemPrompt, userPrompt, maxOutputTokens, model, fallback)
	} else {
		result, err = s.advisor.Complete(ctx, systemPrompt, userPrompt, maxOutputTokens)
	}
	if err != nil {
		return core.ModelResult{}, &ModelTurnUnavailableError{Err: err}
	}
	return result, nil
}

func (s *Service) classifyProjectTurn(ctx context.Context, project core.Project, content, model, fallback, providerID string) (core.ModelResult, error) {
	system := "You route a user's project message. Return JSON only: {\"route\":\"chat\"} or {\"route\":\"task\"}. Use task when the user asks to change files, run commands, research, create artifacts, or perform multiple steps. Use chat for explanation, brainstorming, or direct answers."
	return s.completeProjectTurn(ctx, system, "Project scene: "+project.Scene+"\nUser message:\n"+content, 1024, model, fallback, providerID)
}

func parseTurnRoute(content string) (string, string) {
	normalized := strings.ToLower(strings.TrimSpace(content))
	var payload struct {
		Route string `json:"route"`
	}
	if json.Unmarshal([]byte(normalized), &payload) == nil {
		switch payload.Route {
		case TurnRouteChat:
			return TurnRouteChat, "model_chat"
		case TurnRouteTask:
			return TurnRouteTask, "model_task"
		}
	}
	if strings.Contains(normalized, "task") && !strings.Contains(normalized, "chat") {
		return TurnRouteTask, "model_task_text"
	}
	if strings.Contains(normalized, "chat") && !strings.Contains(normalized, "task") {
		return TurnRouteChat, "model_chat_text"
	}
	return "", ""
}

func (s *Service) answerProjectTurn(ctx context.Context, project core.Project, conversationID, content, model, fallback, providerID string) (core.ModelResult, error) {
	system, messages, err := s.projectTurnAnswerPrompt(ctx, project, conversationID, content, model, fallback)
	if err != nil {
		return core.ModelResult{}, err
	}
	if typed, ok := s.advisor.(TypedProviderProjectTurnModel); ok {
		result, typedErr := typed.CompleteMessagesWithModelRef(ctx, system, messages, 2000, core.ModelRef{ProviderID: providerID, ModelID: model}, fallback)
		if typedErr != nil {
			return core.ModelResult{}, &ModelTurnUnavailableError{Err: typedErr}
		}
		return result, nil
	}
	if typed, ok := s.advisor.(TypedProjectTurnModel); ok {
		result, typedErr := typed.CompleteMessages(ctx, system, messages, 2000, model, fallback)
		if typedErr != nil {
			return core.ModelResult{}, &ModelTurnUnavailableError{Err: typedErr}
		}
		return result, nil
	}
	return s.completeProjectTurn(ctx, system, RenderLegacyTranscript(messages), 2000, model, fallback, providerID)
}

func (s *Service) answerProjectTurnStream(ctx context.Context, project core.Project, conversationID, content, model, fallback, providerID string, onDelta func(string) error) (core.ModelResult, error) {
	system, messages, err := s.projectTurnAnswerPrompt(ctx, project, conversationID, content, model, fallback)
	if err != nil {
		return core.ModelResult{}, err
	}
	// If the advisor supports chat-mode tool use and a workspace executor is
	// configured, allow the model to call read-only tools to investigate before
	// producing its final streamed answer.
	if chatModel, ok := s.advisor.(ChatToolStreamModel); ok && s.chatToolExec != nil && project.ID != "" {
		return s.answerWithChatTools(ctx, chatModel, system, messages, project, model, fallback, providerID, onDelta)
	}
	return s.answerProjectTurnStreamDirect(ctx, system, messages, model, fallback, providerID, onDelta)
}

// answerWithChatTools runs a bounded loop: the model may call read-only tools
// to investigate the workspace, and each tool result is fed back as an
// assistant+tool message pair. After at most 3 tool rounds the model must
// produce a final streamed answer without tools.
func (s *Service) answerWithChatTools(ctx context.Context, chatModel ChatToolStreamModel, system string, messages []core.ModelMessage, project core.Project, model, fallback, providerID string, onDelta func(string) error) (core.ModelResult, error) {
	toolDefs := chatReadonlyToolDefinitions()
	requested := core.ModelRef{ProviderID: providerID, ModelID: model}
	const maxToolRounds = 3
	for round := 0; round < maxToolRounds; round++ {
		result, streamErr := chatModel.StreamMessagesWithTools(ctx, system, messages, 2000, requested, fallback, toolDefs, onDelta)
		if streamErr != nil {
			return core.ModelResult{}, &ModelTurnUnavailableError{Err: streamErr}
		}
		if len(result.ToolCalls) == 0 {
			return result, nil
		}
		// Execute each tool call and append the results to the conversation.
		messages = append(messages, core.ModelMessage{Role: core.MessageRoleAssistant, Content: result.ContentBlocks})
		for _, call := range result.ToolCalls {
			output, execErr := s.chatToolExec.ExecuteChatTool(ctx, project.ID, call)
			if execErr != nil {
				output = "Error: " + execErr.Error()
			}
			messages = append(messages, core.ModelMessage{
				Role:       core.MessageRoleTool,
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content: []core.ContentBlock{{
					Type:       core.ContentBlockToolResult,
					ToolResult: &core.ToolResultBlock{ToolCallID: call.ID, ToolName: call.Name, Content: output},
				}},
			})
		}
	}
	// Final round: force a streamed answer without tools.
	return s.answerProjectTurnStreamDirect(ctx, system, messages, model, fallback, providerID, onDelta)
}

func (s *Service) answerProjectTurnStreamDirect(ctx context.Context, system string, messages []core.ModelMessage, model, fallback, providerID string, onDelta func(string) error) (core.ModelResult, error) {
	if streamModel, ok := s.advisor.(TypedProviderProjectTurnStreamModel); ok {
		result, streamErr := streamModel.StreamMessagesWithModelRef(ctx, system, messages, 2000, core.ModelRef{ProviderID: providerID, ModelID: model}, fallback, onDelta)
		if streamErr != nil {
			return core.ModelResult{}, &ModelTurnUnavailableError{Err: streamErr}
		}
		return result, nil
	}
	if streamModel, ok := s.advisor.(TypedProjectTurnStreamModel); ok {
		result, streamErr := streamModel.StreamMessages(ctx, system, messages, 2000, model, fallback, onDelta)
		if streamErr != nil {
			return core.ModelResult{}, &ModelTurnUnavailableError{Err: streamErr}
		}
		return result, nil
	}
	if streamModel, ok := s.advisor.(ProjectTurnStreamModel); ok {
		result, streamErr := streamModel.StreamTurn(ctx, system, RenderLegacyTranscript(messages), 2000, model, fallback, onDelta)
		if streamErr != nil {
			return core.ModelResult{}, &ModelTurnUnavailableError{Err: streamErr}
		}
		return result, nil
	}
	result, err := s.completeProjectTurn(ctx, system, RenderLegacyTranscript(messages), 2000, model, fallback, providerID)
	if err != nil {
		return core.ModelResult{}, err
	}
	if result.Content != "" {
		if err := onDelta(result.Content); err != nil {
			return core.ModelResult{}, err
		}
	}
	return result, nil
}

// chatReadonlyToolDefinitions returns the minimal tool schemas available to the
// chat mode. All are read-only and scoped to the project workspace.
func chatReadonlyToolDefinitions() []ChatToolDefinition {
	return []ChatToolDefinition{
		{Name: core.ToolWorkspaceListFiles, Description: "List files under a project-relative directory.", Parameters: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"path": map[string]any{"type": "string", "maxLength": 500}, "recursive": map[string]any{"type": "boolean"}}}},
		{Name: core.ToolWorkspaceReadFile, Description: "Read a UTF-8 text file in the project workspace.", Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}, "offset_line": map[string]any{"type": "integer", "minimum": 0}, "limit_lines": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}}}},
		{Name: core.ToolWorkspaceFindFiles, Description: "Find project-relative file names using a glob pattern.", Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"pattern"}, "properties": map[string]any{"pattern": map[string]any{"type": "string", "minLength": 1, "maxLength": 200}, "path": map[string]any{"type": "string", "maxLength": 500}}}},
		{Name: core.ToolWorkspaceGrep, Description: "Search workspace files for literal text.", Parameters: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 500}, "path": map[string]any{"type": "string", "maxLength": 500}, "glob": map[string]any{"type": "string", "maxLength": 200}}}},
	}
}

func (s *Service) projectTurnAnswerPrompt(ctx context.Context, project core.Project, conversationID, content, model, fallback string) (string, []core.ModelMessage, error) {
	messages, err := s.conversationTranscriptForPrompt(ctx, conversationID, content, model, fallback)
	if err != nil {
		return "", nil, err
	}
	system := `# Identity
You are an interactive agent inside a private Oktapod project. You help users by:
- Answering questions about the project, workspace, and system state
- Exploring files with read-only tools
- Explaining code and architecture
- Advising on next steps

# Available read-only tools
- workspace_list_files: list files in a project directory
- workspace_read_file: read file contents
- workspace_find_files: find files by glob pattern
- workspace_grep: search file contents

Use tools proactively to investigate before answering. Do not ask the user to look up information you can check yourself.

For file modifications, running commands, or multi-step tasks, tell the user to send it as a task and the agent will execute with full tool access.`
	if s.workspaceRoot != "" {
		system += "\n\n# Environment\nWorkspace: " + s.workspaceRoot
	}
	system += "\nProject: " + project.Name + " (scene: " + project.Scene + ")"
	if model != "" {
		system += "\nModel: " + model
	}
	return system, messages, nil
}

func normalizeIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// ForkTask creates a new, independently executable Task from an existing
// task. It never mutates the source Run and records the lineage in the new
// run's immutable configuration snapshot.
func (s *Service) ForkTask(ctx context.Context, taskID, correlationID, idempotencyKey string) (core.Task, error) {
	source, err := s.repository.GetTask(ctx, s.ScopeFor(ctx), taskID)
	if err != nil {
		return core.Task{}, err
	}
	input := core.CreateTaskInput{
		Title:      "Fork: " + source.Title,
		Goal:       source.Goal,
		Acceptance: source.Acceptance,
		Mode:       source.Mode,
		Visibility: source.Visibility,
	}
	if err := core.ValidateCreateTask(input); err != nil {
		return core.Task{}, err
	}
	idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Task{}, err
	}
	request := struct {
		SourceTaskID string `json:"source_task_id"`
	}{SourceTaskID: taskID}
	snapshot := map[string]any{
		"source":         "fork",
		"source_task_id": source.ID,
		"source_run_id":  source.RunID,
	}
	if sourceRun, runErr := s.repository.GetRunContext(ctx, source.RunID); runErr == nil {
		var sourceConfig map[string]any
		if json.Unmarshal([]byte(sourceRun.ConfigSnapshotJSON), &sourceConfig) == nil {
			if scene, ok := sourceConfig["scene"]; ok {
				snapshot["scene"] = scene
			}
			if policy, ok := sourceConfig["scene_policy"]; ok {
				snapshot["scene_policy"] = policy
			}
		}
	}
	return s.repository.CreateTask(ctx, s.ScopeFor(ctx), source.ProjectID, input, snapshot, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func classifyTaskMode(input core.CreateTaskInput) (string, string) {
	goal := strings.ToLower(input.Goal)
	if strings.TrimSpace(input.Acceptance) != "" {
		return core.ModeSingleAgent, "explicit_acceptance_requires_task"
	}
	complexMarkers := []string{
		"修改文件", "创建文件", "删除文件", "运行测试", "执行命令", "定时", "工作区", "仓库", "代码", "多步骤", "调研并", "分析并",
		"edit file", "create file", "delete file", "run test", "execute command", "schedule", "workspace", "repository", "multi-step",
	}
	for _, marker := range complexMarkers {
		if strings.Contains(goal, marker) {
			return core.ModeSingleAgent, "deterministic_complex_marker:" + marker
		}
	}
	if len([]rune(goal)) > 300 {
		return core.ModeSingleAgent, "deterministic_length_threshold"
	}
	return core.ModeAnswer, "deterministic_simple_answer"
}

func (s *Service) GetTask(ctx context.Context, taskID string) (core.Task, error) {
	if err := s.authorize(ctx, "read", "task", taskID); err != nil {
		return core.Task{}, err
	}
	return s.repository.GetTask(ctx, s.ScopeFor(ctx), taskID)
}

func (s *Service) ControlTask(ctx context.Context, taskID string, input core.ControlTaskInput, correlationID, idempotencyKey string) (core.Task, error) {
	input.Command = strings.ToLower(strings.TrimSpace(input.Command))
	if err := core.ValidateControlTask(input); err != nil {
		return core.Task{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Task{}, err
	}
	request := struct {
		TaskID string                `json:"task_id"`
		Input  core.ControlTaskInput `json:"input"`
	}{TaskID: taskID, Input: input}
	return s.repository.ControlTask(ctx, s.ScopeFor(ctx), taskID, input, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) AcceptTaskOutcome(ctx context.Context, taskID, note, correlationID, idempotencyKey string) (core.AcceptedOutcome, error) {
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 1000 {
		return core.AcceptedOutcome{}, &core.ValidationError{Field: "note", Message: "must not exceed 1000 characters"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.AcceptedOutcome{}, err
	}
	request := struct {
		TaskID string `json:"task_id"`
		Note   string `json:"note"`
	}{TaskID: taskID, Note: note}
	idempotency := core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)}
	return retryTransientSQLiteValue(ctx, func() (core.AcceptedOutcome, error) {
		return s.repository.AcceptTaskOutcome(ctx, s.ScopeFor(ctx), taskID, note, correlationID, idempotency)
	})
}

func transientSQLiteBusy(err error) bool {
	message := strings.ToUpper(err.Error())
	return strings.Contains(message, "SQLITE_BUSY") || strings.Contains(message, "DATABASE IS LOCKED")
}

func retryTransientSQLiteValue[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		value, err := operation()
		if err == nil || !transientSQLiteBusy(err) {
			return value, err
		}
		lastErr = err
		timer := time.NewTimer(time.Duration(10*(1<<attempt)) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, lastErr
}

func (s *Service) ListTasks(ctx context.Context, projectID string) ([]core.Task, error) {
	return s.repository.ListTasks(ctx, s.ScopeFor(ctx), projectID, 100)
}

func (s *Service) ListConversations(ctx context.Context, projectID string) ([]core.Conversation, error) {
	return s.repository.ListConversations(ctx, s.ScopeFor(ctx), projectID, 100)
}

func (s *Service) DeleteConversation(ctx context.Context, conversationID, correlationID string) error {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return &core.ValidationError{Field: "conversation_id", Message: "is required"}
	}
	return s.repository.DeleteConversation(ctx, s.ScopeFor(ctx), conversationID, correlationID)
}

func (s *Service) ListMessages(ctx context.Context, conversationID string, after int64) ([]core.Message, error) {
	if after < 0 {
		return nil, &core.ValidationError{Field: "after", Message: "must not be negative"}
	}
	return s.repository.ListMessages(ctx, s.ScopeFor(ctx), conversationID, after, 100)
}

func (s *Service) ListProjectActivity(ctx context.Context, projectID, before string) ([]core.ProjectActivity, error) {
	before = strings.TrimSpace(before)
	if before != "" {
		if _, err := time.Parse(time.RFC3339Nano, before); err != nil {
			return nil, &core.ValidationError{Field: "before", Message: "must be an RFC3339 timestamp"}
		}
	}
	return s.repository.ListProjectActivity(ctx, s.ScopeFor(ctx), projectID, before, 100)
}

func (s *Service) CreatePlanItem(ctx context.Context, projectID string, input core.CreatePlanItemInput, idempotencyKey string) (core.PlanItem, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Description = strings.TrimSpace(input.Description)
	if err := core.ValidateCreatePlanItem(input); err != nil {
		return core.PlanItem{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.PlanItem{}, err
	}
	request := struct {
		ProjectID string                   `json:"project_id"`
		Input     core.CreatePlanItemInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.CreatePlanItem(ctx, s.ScopeFor(ctx), projectID, input, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListPlanItems(ctx context.Context, projectID string) ([]core.PlanItem, error) {
	return s.repository.ListPlanItems(ctx, s.ScopeFor(ctx), projectID, 500)
}

func (s *Service) MovePlanItem(ctx context.Context, planItemID string, input core.MovePlanItemInput, idempotencyKey string) (core.PlanItem, error) {
	input.Status = strings.ToLower(strings.TrimSpace(input.Status))
	if err := core.ValidateMovePlanItem(input); err != nil {
		return core.PlanItem{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.PlanItem{}, err
	}
	request := struct {
		PlanItemID string                 `json:"plan_item_id"`
		Input      core.MovePlanItemInput `json:"input"`
	}{PlanItemID: planItemID, Input: input}
	return s.repository.MovePlanItem(ctx, s.ScopeFor(ctx), planItemID, input, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) CreateAssetFolder(ctx context.Context, projectID string, input core.CreateAssetFolderInput, idempotencyKey string) (core.Asset, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ParentID = strings.TrimSpace(input.ParentID)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if err := core.ValidateCreateAssetFolder(input); err != nil {
		return core.Asset{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Asset{}, err
	}
	request := struct {
		ProjectID string                      `json:"project_id"`
		Input     core.CreateAssetFolderInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.CreateAssetFolder(ctx, s.ScopeFor(ctx), projectID, input, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) UploadAsset(ctx context.Context, projectID string, input core.UploadAssetInput, content io.Reader) (core.Asset, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ParentID = strings.TrimSpace(input.ParentID)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	input.MediaType = strings.TrimSpace(input.MediaType)
	if input.MediaType == "" {
		input.MediaType = "application/octet-stream"
	}
	if err := core.ValidateUploadAsset(input, MaxAssetFileBytes); err != nil {
		return core.Asset{}, err
	}
	if content == nil {
		return core.Asset{}, &core.ValidationError{Field: "file", Message: "is required"}
	}
	return s.repository.UploadAsset(ctx, s.ScopeFor(ctx), projectID, input, content, MaxAssetFileBytes, ProjectAssetQuota)
}

func (s *Service) ListAssets(ctx context.Context, projectID, parentID, query, kind string) (core.AssetCollection, error) {
	parentID = strings.TrimSpace(parentID)
	query = strings.TrimSpace(query)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if len([]rune(query)) > 200 {
		return core.AssetCollection{}, &core.ValidationError{Field: "q", Message: "must not exceed 200 characters"}
	}
	if kind != "" && kind != core.AssetKindFile && kind != core.AssetKindFolder {
		return core.AssetCollection{}, &core.ValidationError{Field: "kind", Message: "must be file or folder"}
	}
	return s.repository.ListAssets(ctx, s.ScopeFor(ctx), projectID, parentID, query, kind, 200, ProjectAssetQuota)
}

func (s *Service) OpenAssetContent(ctx context.Context, assetID string) (core.Asset, io.ReadCloser, error) {
	return s.repository.OpenAssetContent(ctx, s.ScopeFor(ctx), assetID)
}

func (s *Service) OpenArtifactContent(ctx context.Context, artifactID string) (core.Artifact, io.ReadCloser, error) {
	return s.repository.OpenArtifactContent(ctx, s.ScopeFor(ctx), artifactID)
}

func (s *Service) PublishArtifact(ctx context.Context, artifactID string, input core.PublishArtifactInput, idempotencyKey string) (core.Asset, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.ParentID = strings.TrimSpace(input.ParentID)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	if err := core.ValidatePublishArtifact(input); err != nil {
		return core.Asset{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Asset{}, err
	}
	request := struct {
		ArtifactID string                    `json:"artifact_id"`
		Input      core.PublishArtifactInput `json:"input"`
	}{ArtifactID: artifactID, Input: input}
	return s.repository.PublishArtifact(ctx, s.ScopeFor(ctx), artifactID, input, ProjectAssetQuota, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListRunEvents(ctx context.Context, runID string, after int64) ([]core.RunEvent, error) {
	return s.repository.ListRunEvents(ctx, s.ScopeFor(ctx), runID, after, 200)
}

func (s *Service) GetRunUsage(ctx context.Context, runID string) (core.RunUsage, error) {
	return s.repository.GetRunUsage(ctx, s.ScopeFor(ctx), runID)
}

func (s *Service) GetProjectUsage(ctx context.Context, projectID string, since time.Time) (core.ProjectUsage, error) {
	if strings.TrimSpace(projectID) == "" {
		return core.ProjectUsage{}, &core.ValidationError{Field: "project_id", Message: "is required"}
	}
	if since.IsZero() {
		since = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}
	if since.After(time.Now().UTC()) {
		return core.ProjectUsage{}, &core.ValidationError{Field: "since", Message: "must not be in the future"}
	}
	if time.Since(since) > 366*24*time.Hour {
		since = time.Now().UTC().Add(-366 * 24 * time.Hour)
	}
	return s.repository.GetProjectUsage(ctx, s.ScopeFor(ctx), projectID, since.UTC())
}

func (s *Service) UpdateRunBudget(ctx context.Context, runID string, input core.UpdateRunBudgetInput, correlationID string) (core.RunUsage, error) {
	if input.MaxModelCalls < 0 || input.MaxModelCalls > 1000 || input.MaxTokens < 0 || input.MaxTokens > 10_000_000 {
		return core.RunUsage{}, &core.ValidationError{Field: "budget", Message: "model calls must be 0..1000 and tokens must be 0..10000000; zero means unlimited"}
	}
	if err := s.repository.UpdateRunBudget(ctx, s.ScopeFor(ctx), runID, input, correlationID); err != nil {
		return core.RunUsage{}, err
	}
	return s.repository.GetRunUsage(ctx, s.ScopeFor(ctx), runID)
}

func (s *Service) CreateAdvisorReport(ctx context.Context, runID string, input core.CreateAdvisorInput, correlationID string) (core.AdvisorReport, error) {
	input.TriggerReason = strings.TrimSpace(input.TriggerReason)
	if input.TriggerReason == "" || len([]rune(input.TriggerReason)) > 500 {
		return core.AdvisorReport{}, &core.ValidationError{Field: "trigger_reason", Message: "is required and must not exceed 500 characters"}
	}
	report, err := s.repository.CreateAdvisorReport(ctx, s.ScopeFor(ctx), runID, input, correlationID)
	if err != nil || !input.Approve || s.advisor == nil {
		return report, err
	}
	if usage, usageErr := s.repository.GetRunUsage(ctx, s.ScopeFor(ctx), runID); usageErr == nil {
		if (usage.BudgetMaxCalls > 0 && usage.RemainingCalls < 1) || (usage.BudgetMaxTokens > 0 && usage.RemainingTokens < report.EstimatedTokens) {
			return report, &core.ForbiddenError{Message: "Advisor review exceeds the remaining Run budget; increase the budget explicitly before approving"}
		}
	}
	prompt := "Advisor 触发原因：" + report.TriggerReason + "\n仅基于以下已提交证据复核，不得批准安全动作或扩大权限：\n" + strings.Join(report.Evidence, "\n")
	modelResult, modelErr := s.advisor.Complete(ctx, "你是 Oktapod Advisor。只复核证据完整性、冲突和验收风险，输出简短可执行建议；不得伪造执行结果。", prompt, 1536)
	if finalizer, ok := s.repository.(AdvisorReportFinalizer); ok {
		return finalizer.FinalizeAdvisorReport(ctx, s.ScopeFor(ctx), report.ID, modelResult, modelErr)
	}
	return report, modelErr
}

func (s *Service) ListRunArtifacts(ctx context.Context, runID string) ([]core.Artifact, error) {
	return s.repository.ListRunArtifacts(ctx, s.ScopeFor(ctx), runID)
}

func (s *Service) GetAgentTeamRoom(ctx context.Context, runID string) (core.AgentTeamRoom, error) {
	return s.repository.GetAgentTeamRoom(ctx, s.ScopeFor(ctx), runID)
}

func (s *Service) CreateSchedule(ctx context.Context, projectID string, input core.CreateScheduleInput, idempotencyKey string) (core.Schedule, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.CronExpression = strings.TrimSpace(input.CronExpression)
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.Goal = strings.TrimSpace(input.Goal)
	input.Acceptance = strings.TrimSpace(input.Acceptance)
	input.Mode = strings.ToLower(strings.TrimSpace(input.Mode))
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	input.MisfirePolicy = strings.ToLower(strings.TrimSpace(input.MisfirePolicy))
	input.OverlapPolicy = strings.ToLower(strings.TrimSpace(input.OverlapPolicy))
	if input.MisfirePolicy == "" {
		input.MisfirePolicy = core.ScheduleMisfireAsk
	}
	if input.OverlapPolicy == "" {
		input.OverlapPolicy = core.ScheduleOverlapForbid
	}
	if input.MisfireGraceSeconds == 0 {
		input.MisfireGraceSeconds = 300
	}
	if err := core.ValidateCreateSchedule(input); err != nil {
		return core.Schedule{}, err
	}
	location, err := time.LoadLocation(input.Timezone)
	if err != nil {
		return core.Schedule{}, &core.ValidationError{Field: "timezone", Message: "must be a valid IANA timezone"}
	}
	scheduleParser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	schedule, err := scheduleParser.Parse(input.CronExpression)
	if err != nil {
		return core.Schedule{}, &core.ValidationError{Field: "cron_expression", Message: "must be a valid cron expression"}
	}
	now := time.Now().UTC()
	next := schedule.Next(now.In(location)).UTC()
	idempotencyKey, err = normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Schedule{}, err
	}
	request := struct {
		ProjectID string                   `json:"project_id"`
		Input     core.CreateScheduleInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.CreateSchedule(ctx, s.ScopeFor(ctx), projectID, input, next, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListSchedules(ctx context.Context, projectID string) ([]core.Schedule, error) {
	return s.repository.ListSchedules(ctx, s.ScopeFor(ctx), projectID, 100)
}

func (s *Service) SetScheduleStatus(ctx context.Context, scheduleID, status, idempotencyKey string) (core.Schedule, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != core.ScheduleStatusEnabled && status != core.ScheduleStatusDisabled {
		return core.Schedule{}, &core.ValidationError{Field: "status", Message: "must be enabled or disabled"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Schedule{}, err
	}
	request := struct {
		ScheduleID string `json:"schedule_id"`
		Status     string `json:"status"`
	}{ScheduleID: scheduleID, Status: status}
	return s.repository.SetScheduleStatus(ctx, s.ScopeFor(ctx), scheduleID, status, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) CreateCapability(ctx context.Context, projectID string, input core.CreateCapabilityInput, idempotencyKey string) (core.Capability, error) {
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.Name = strings.TrimSpace(input.Name)
	input.Version = strings.TrimSpace(input.Version)
	input.ManifestJSON = strings.TrimSpace(input.ManifestJSON)
	input.ScopeType = strings.ToLower(strings.TrimSpace(input.ScopeType))
	input.SourceFilename = strings.TrimSpace(input.SourceFilename)
	if input.ScopeType == "" {
		input.ScopeType = core.CapabilityScopeGlobal
	}
	if strings.TrimSpace(input.BundleBase64) != "" {
		bundle, bundleErr := normalizeCapabilityBundle(input.BundleBase64, input.SourceFilename, input.Kind, input.Name, input.Version)
		if bundleErr != nil {
			return core.Capability{}, &core.ValidationError{Field: "bundle_base64", Message: bundleErr.Error()}
		}
		input.Kind = bundle.Kind
		input.Name = bundle.Name
		input.Version = bundle.Version
		input.ManifestJSON = bundle.ManifestJSON
		input.BundleData = bundle.Data
		input.BundleHash = bundle.Hash
		input.BundleBase64 = ""
	}
	if input.Kind == core.CapabilityKindSkill {
		normalized, name, normalizeErr := normalizeSkillManifest(input.ManifestJSON, input.Name)
		if normalizeErr != nil {
			return core.Capability{}, &core.ValidationError{Field: "manifest_json", Message: normalizeErr.Error()}
		}
		input.ManifestJSON = normalized
		input.Name = name
	}
	if input.Version == "" {
		input.Version = "0.1.0"
	}
	if err := core.ValidateCreateCapability(input); err != nil {
		return core.Capability{}, err
	}
	contentHash := input.BundleHash
	if contentHash == "" {
		hash := sha256.Sum256([]byte(input.ManifestJSON))
		contentHash = "sha256:" + hex.EncodeToString(hash[:])
	}
	status := core.CapabilityStatusEnabled
	riskLevel := "low"
	// Security-sensitive manifests remain visible for review but cannot be activated.
	var manifest map[string]any
	_ = json.Unmarshal([]byte(input.ManifestJSON), &manifest)
	signaturePresent, signatureVerified, _, signatureErr := verifyCapabilityManifestSignature(input.ManifestJSON, s.capabilityKeys)
	if signatureErr != nil {
		return core.Capability{}, &core.ValidationError{Field: "manifest_json._oktapod_signature", Message: signatureErr.Error()}
	}
	if signaturePresent && !signatureVerified {
		status = core.CapabilityStatusQuarantined
		riskLevel = "untrusted-signature"
	}
	for _, key := range []string{"permissions", "scopes", "tools"} {
		if value, ok := manifest[key]; ok && value != nil {
			if riskLevel != "untrusted-signature" {
				riskLevel = "review"
			}
			status = core.CapabilityStatusQuarantined
		}
	}
	if bundleMetadata, ok := manifest["_oktapod_bundle"].(map[string]any); ok {
		if executable, _ := bundleMetadata["has_executable"].(bool); executable {
			status = core.CapabilityStatusQuarantined
			if riskLevel != "untrusted-signature" {
				riskLevel = "review"
			}
		}
	}
	if input.Kind == core.CapabilityKindMCP {
		// MCP is never connected or exposed on import; an owner must explicitly
		// review the endpoint/auth/tool manifest first.
		status = core.CapabilityStatusQuarantined
		if riskLevel != "untrusted-signature" {
			riskLevel = "review"
		}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Capability{}, err
	}
	request := struct {
		ProjectID      string `json:"project_id"`
		Kind           string `json:"kind"`
		Name           string `json:"name"`
		Version        string `json:"version"`
		ManifestJSON   string `json:"manifest_json"`
		ScopeType      string `json:"scope_type"`
		SourceFilename string `json:"source_filename"`
		BundleHash     string `json:"bundle_hash"`
	}{ProjectID: projectID, Kind: input.Kind, Name: input.Name, Version: input.Version, ManifestJSON: input.ManifestJSON, ScopeType: input.ScopeType, SourceFilename: input.SourceFilename, BundleHash: input.BundleHash}
	return s.repository.CreateCapability(ctx, s.ScopeFor(ctx), projectID, input, contentHash, status, riskLevel, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListCapabilities(ctx context.Context, projectID string) ([]core.Capability, error) {
	return s.repository.ListCapabilities(ctx, s.ScopeFor(ctx), projectID, 100)
}

func (s *Service) SetCapabilityStatus(ctx context.Context, capabilityID, status, idempotencyKey string) (core.Capability, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != core.CapabilityStatusEnabled && status != core.CapabilityStatusDisabled && status != core.CapabilityStatusQuarantined {
		return core.Capability{}, &core.ValidationError{Field: "status", Message: "must be enabled, disabled, or quarantined"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Capability{}, err
	}
	request := struct {
		CapabilityID string `json:"capability_id"`
		Status       string `json:"status"`
	}{CapabilityID: capabilityID, Status: status}
	return s.repository.SetCapabilityStatus(ctx, s.ScopeFor(ctx), capabilityID, status, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

// ReviewCapability is the only path that can move a quarantined capability
// into an executable state. The decision and reviewer note are persisted by
// the repository so the permission gate remains auditable and idempotent.
func (s *Service) ReviewCapability(ctx context.Context, capabilityID string, input core.ReviewCapabilityInput, correlationID, idempotencyKey string) (core.Capability, error) {
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Note = strings.TrimSpace(input.Note)
	if err := core.ValidateReviewCapability(input); err != nil {
		return core.Capability{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Capability{}, err
	}
	request := struct {
		CapabilityID string                     `json:"capability_id"`
		Input        core.ReviewCapabilityInput `json:"input"`
	}{CapabilityID: capabilityID, Input: input}
	return s.repository.ReviewCapability(ctx, s.ScopeFor(ctx), capabilityID, input, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ConnectMCP(ctx context.Context, capabilityID, correlationID string) (core.MCPHealth, error) {
	return s.repository.ConnectMCP(ctx, s.ScopeFor(ctx), capabilityID, correlationID)
}

func (s *Service) DisconnectMCP(ctx context.Context, capabilityID, correlationID string) (core.MCPHealth, error) {
	return s.repository.DisconnectMCP(ctx, s.ScopeFor(ctx), capabilityID, correlationID)
}

func (s *Service) GetMCPHealth(ctx context.Context, capabilityID string) (core.MCPHealth, error) {
	return s.repository.GetMCPHealth(ctx, s.ScopeFor(ctx), capabilityID)
}

func (s *Service) CreateMemory(ctx context.Context, projectID string, input core.CreateMemoryInput, idempotencyKey string) (core.Memory, error) {
	input.Kind = strings.TrimSpace(input.Kind)
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	input.SourceRefsJSON = normalizeMemorySourceRefs(input.SourceRefsJSON)
	if err := core.ValidateCreateMemory(input); err != nil {
		return core.Memory{}, err
	}
	if reason := sensitiveMemoryReason(input.Content); reason != "" {
		return core.Memory{}, &core.ValidationError{Field: "content", Message: "memory candidate contains sensitive material; store a SecretRef instead (" + reason + ")"}
	}
	hash := sha256.Sum256([]byte(input.Kind + "\x00" + input.Title + "\x00" + input.Content + "\x00" + input.SourceRefsJSON))
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Memory{}, err
	}
	request := struct {
		ProjectID string                 `json:"project_id"`
		Input     core.CreateMemoryInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.CreateMemory(ctx, s.ScopeFor(ctx), projectID, input, "sha256:"+hex.EncodeToString(hash[:]), core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func sensitiveMemoryReason(content string) string {
	value := strings.ToLower(content)
	markers := []struct {
		marker string
		reason string
	}{
		{"api_key=", "api key"},
		{"apikey:", "api key"},
		{"authorization: bearer ", "bearer token"},
		{"-----begin private key-----", "private key"},
		{"sk-", "provider key"},
		{"fe_oa_", "provider key"},
		{"password=", "password"},
		{"password:", "password"},
	}
	for _, item := range markers {
		if strings.Contains(value, item.marker) {
			return item.reason
		}
	}
	return ""
}

func normalizeMemorySourceRefs(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "[]"
	}
	var refs []string
	if err := json.Unmarshal([]byte(value), &refs); err != nil {
		return value
	}
	for index := range refs {
		refs[index] = strings.TrimSpace(refs[index])
	}
	normalized, err := json.Marshal(refs)
	if err != nil {
		return value
	}
	return string(normalized)
}

func (s *Service) ListMemories(ctx context.Context, projectID string, queries ...string) ([]core.Memory, error) {
	query := ""
	if len(queries) > 0 {
		query = queries[0]
	}
	query = strings.TrimSpace(query)
	if len([]rune(query)) > 200 {
		return nil, &core.ValidationError{Field: "q", Message: "must not exceed 200 characters"}
	}
	return s.repository.ListMemories(ctx, s.ScopeFor(ctx), projectID, query, 100)
}

func (s *Service) UpdateMemory(ctx context.Context, memoryID string, input core.UpdateMemoryInput, idempotencyKey string) (core.Memory, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Content = strings.TrimSpace(input.Content)
	input.Visibility = strings.ToLower(strings.TrimSpace(input.Visibility))
	input.SourceRefsJSON = normalizeMemorySourceRefs(input.SourceRefsJSON)
	if err := core.ValidateCreateMemory(core.CreateMemoryInput{Kind: "manual", Title: input.Title, Content: input.Content, Visibility: input.Visibility, SourceRefsJSON: input.SourceRefsJSON}); err != nil {
		return core.Memory{}, err
	}
	if reason := sensitiveMemoryReason(input.Content); reason != "" {
		return core.Memory{}, &core.ValidationError{Field: "content", Message: "memory candidate contains sensitive material; store a SecretRef instead (" + reason + ")"}
	}
	hash := sha256.Sum256([]byte(input.Title + "\x00" + input.Content + "\x00" + input.SourceRefsJSON))
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Memory{}, err
	}
	request := struct {
		MemoryID string                 `json:"memory_id"`
		Input    core.UpdateMemoryInput `json:"input"`
	}{MemoryID: memoryID, Input: input}
	return s.repository.UpdateMemory(ctx, s.ScopeFor(ctx), strings.TrimSpace(memoryID), input, "sha256:"+hex.EncodeToString(hash[:]), core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) DeleteMemory(ctx context.Context, memoryID, correlationID, idempotencyKey string) error {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return &core.ValidationError{Field: "memory_id", Message: "is required"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return err
	}
	return s.repository.DeleteMemory(ctx, s.ScopeFor(ctx), memoryID, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(struct {
		MemoryID string `json:"memory_id"`
	}{MemoryID: memoryID})})
}

func (s *Service) SetMemoryStatus(ctx context.Context, memoryID, status, idempotencyKey string) (core.Memory, error) {
	memoryID = strings.TrimSpace(memoryID)
	status = strings.ToLower(strings.TrimSpace(status))
	if memoryID == "" {
		return core.Memory{}, &core.ValidationError{Field: "memory_id", Message: "is required"}
	}
	if status != core.MemoryStatusActive && status != core.MemoryStatusInactive {
		return core.Memory{}, &core.ValidationError{Field: "status", Message: "must be active or inactive"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Memory{}, err
	}
	request := struct {
		MemoryID string `json:"memory_id"`
		Status   string `json:"status"`
	}{MemoryID: memoryID, Status: status}
	return s.repository.SetMemoryStatus(ctx, s.ScopeFor(ctx), memoryID, status, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

// PromoteMemoryToSkill turns a user-approved memory into a quarantined,
// content-addressed Skill draft. It never enables or executes the new
// capability automatically; an owner must review and enable it first.
func (s *Service) PromoteMemoryToSkill(ctx context.Context, memoryID, idempotencyKey string) (core.Capability, error) {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return core.Capability{}, &core.ValidationError{Field: "memory_id", Message: "is required"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Capability{}, err
	}
	request := struct {
		MemoryID string `json:"memory_id"`
	}{MemoryID: memoryID}
	return s.repository.PromoteMemoryToSkill(ctx, s.ScopeFor(ctx), memoryID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

// CreateDailySummary performs a deterministic, zero-model-call daily digest.
// It only reads scope-filtered committed activity, so private events never leak.
func (s *Service) CreateDailySummary(ctx context.Context, projectID, idempotencyKey string) (core.Memory, error) {
	return s.CreateDailySummaryForDate(ctx, projectID, time.Now().UTC().Format("2006-01-02"), idempotencyKey)
}

// CreateDailySummaryForDate is the durable entry point used by the daily
// summary runner. The date is explicit so a process that crosses midnight can
// close the previous day's activity without relying on wall-clock timing.
func (s *Service) CreateDailySummaryForDate(ctx context.Context, projectID, date, idempotencyKey string) (core.Memory, error) {
	parsedDate, err := time.Parse("2006-01-02", strings.TrimSpace(date))
	if err != nil {
		return core.Memory{}, &core.ValidationError{Field: "date", Message: "must use YYYY-MM-DD"}
	}
	if parsedDate.After(time.Now().UTC().Truncate(24 * time.Hour)) {
		return core.Memory{}, &core.ValidationError{Field: "date", Message: "cannot be in the future"}
	}
	activity, err := s.repository.ListProjectActivity(ctx, s.ScopeFor(ctx), projectID, "", 500)
	if err != nil {
		return core.Memory{}, err
	}
	today := parsedDate.Format("2006-01-02")
	// A deterministic title is the durable uniqueness key for a project/day.
	// This protects the automatic runner from duplicate summaries after
	// process restart even when the caller uses a fresh idempotency key.
	existing, lookupErr := s.repository.ListMemories(ctx, s.ScopeFor(ctx), projectID, "Daily Summary "+today, 20)
	if lookupErr != nil {
		return core.Memory{}, lookupErr
	}
	for _, memory := range existing {
		if memory.Kind == "daily_summary" && memory.Title == "Daily Summary "+today {
			return memory, nil
		}
	}
	counts := map[string]int{}
	todayCount := 0
	sourceRefs := make([]string, 0, 32)
	for _, event := range activity {
		if event.OccurredAt.UTC().Format("2006-01-02") == today {
			todayCount++
			counts[event.EventType]++
			if len(sourceRefs) < 32 && strings.TrimSpace(event.ID) != "" {
				sourceRefs = append(sourceRefs, event.ID)
			}
		}
	}
	content := "日期: " + today + "\n已提交事件: " + strconv.Itoa(todayCount) + "\n事件分布:\n"
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		content += "- " + key + ": " + strconv.Itoa(counts[key]) + "\n"
	}
	repeated := repeatedActivitySuggestions(counts)
	content += "重复模式（达到 3 次才建议沉淀）:\n"
	if len(repeated) == 0 {
		content += "- 无\n"
	} else {
		for _, suggestion := range repeated {
			content += "- " + suggestion + "\n"
		}
	}
	project, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), projectID)
	if err != nil {
		return core.Memory{}, err
	}
	visibility := core.VisibilityPrivate
	if project.Kind == core.ProjectKindTeam {
		visibility = core.VisibilityProject
	}
	refsJSON, _ := json.Marshal(sourceRefs)
	created, createErr := s.CreateMemory(ctx, projectID, core.CreateMemoryInput{Kind: "daily_summary", Title: "Daily Summary " + today, Content: content, Visibility: visibility, SourceRefsJSON: string(refsJSON)}, idempotencyKey)
	if createErr == nil {
		return created, nil
	}
	// A concurrent runner may win the unique project/date insert between the
	// lookup above and CreateMemory. Re-read the deterministic row so the
	// losing runner remains idempotent instead of surfacing a false failure.
	if existing, lookupErr := s.repository.ListMemories(ctx, s.ScopeFor(ctx), projectID, "Daily Summary "+today, 20); lookupErr == nil {
		for _, memory := range existing {
			if memory.Kind == "daily_summary" && memory.Title == "Daily Summary "+today {
				return memory, nil
			}
		}
	}
	return core.Memory{}, createErr
}

// repeatedActivitySuggestions turns an observed event frequency into a
// deterministic candidate for a rule, workflow, or Skill. It deliberately
// emits a suggestion only after three occurrences and never calls a model;
// publishing still requires the explicit memory-to-Skill review flow.
func repeatedActivitySuggestions(counts map[string]int) []string {
	keys := make([]string, 0)
	for key, count := range counts {
		if count >= 3 && strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	suggestions := make([]string, 0, len(keys))
	for _, key := range keys {
		suggestions = append(suggestions, key+" 出现 "+strconv.Itoa(counts[key])+" 次，可评估沉淀为确定性规则/Workflow/Skill")
	}
	return suggestions
}

func (s *Service) CreateAgentDefinition(ctx context.Context, projectID string, input core.CreateAgentInput, idempotencyKey string) (core.AgentDefinition, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Version = strings.TrimSpace(input.Version)
	input.Instructions = strings.TrimSpace(input.Instructions)
	input.Model = strings.TrimSpace(input.Model)
	input.FallbackModel = strings.TrimSpace(input.FallbackModel)
	input.ConfigJSON = strings.TrimSpace(input.ConfigJSON)
	if input.ConfigJSON == "" {
		input.ConfigJSON = "{}"
	}
	if err := core.ValidateCreateAgent(input); err != nil {
		return core.AgentDefinition{}, err
	}
	status := core.CapabilityStatusEnabled
	var config map[string]any
	_ = json.Unmarshal([]byte(input.ConfigJSON), &config)
	if value, ok := config["requires_approval"]; ok && value == true {
		status = core.CapabilityStatusQuarantined
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.AgentDefinition{}, err
	}
	request := struct {
		ProjectID string                `json:"project_id"`
		Input     core.CreateAgentInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.CreateAgentDefinition(ctx, s.ScopeFor(ctx), projectID, input, status, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListAgentDefinitions(ctx context.Context, projectID string) ([]core.AgentDefinition, error) {
	return s.repository.ListAgentDefinitions(ctx, s.ScopeFor(ctx), projectID, 100)
}

func (s *Service) SetAgentDefinitionStatus(ctx context.Context, agentID, status, idempotencyKey string) (core.AgentDefinition, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != core.CapabilityStatusEnabled && status != core.CapabilityStatusDisabled && status != core.CapabilityStatusQuarantined {
		return core.AgentDefinition{}, &core.ValidationError{Field: "status", Message: "must be enabled, disabled, or quarantined"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.AgentDefinition{}, err
	}
	request := struct {
		AgentID string `json:"agent_id"`
		Status  string `json:"status"`
	}{AgentID: agentID, Status: status}
	return s.repository.SetAgentDefinitionStatus(ctx, s.ScopeFor(ctx), agentID, status, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) CreateTeamDefinition(ctx context.Context, projectID string, input core.CreateTeamDefinitionInput, idempotencyKey string) (core.TeamDefinition, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Version = strings.TrimSpace(input.Version)
	if err := core.ValidateCreateTeamDefinition(input); err != nil {
		return core.TeamDefinition{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.TeamDefinition{}, err
	}
	request := struct {
		ProjectID string                         `json:"project_id"`
		Input     core.CreateTeamDefinitionInput `json:"input"`
	}{ProjectID: projectID, Input: input}
	return s.repository.CreateTeamDefinition(ctx, s.ScopeFor(ctx), projectID, input, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListTeamDefinitions(ctx context.Context, projectID string) ([]core.TeamDefinition, error) {
	return s.repository.ListTeamDefinitions(ctx, s.ScopeFor(ctx), projectID, 100)
}

func (s *Service) SetTeamDefinitionStatus(ctx context.Context, teamID, status, idempotencyKey string) (core.TeamDefinition, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != core.CapabilityStatusEnabled && status != core.CapabilityStatusDisabled && status != core.CapabilityStatusQuarantined {
		return core.TeamDefinition{}, &core.ValidationError{Field: "status", Message: "must be enabled, disabled, or quarantined"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.TeamDefinition{}, err
	}
	request := struct {
		TeamID string `json:"team_id"`
		Status string `json:"status"`
	}{TeamID: teamID, Status: status}
	return s.repository.SetTeamDefinitionStatus(ctx, s.ScopeFor(ctx), teamID, status, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListApprovals(ctx context.Context, status string) ([]core.Approval, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = core.ApprovalPending
	}
	if status != "all" && status != core.ApprovalPending && status != core.ApprovalApproved && status != core.ApprovalRejected {
		return nil, &core.ValidationError{Field: "status", Message: "must be pending, approved, rejected, or all"}
	}
	return s.repository.ListApprovals(ctx, s.ScopeFor(ctx), status, 100)
}

func (s *Service) GetActiveRunPlan(ctx context.Context, runID string) (core.RunPlanRevision, error) {
	return s.repository.GetActiveRunPlan(ctx, s.ScopeFor(ctx), strings.TrimSpace(runID))
}

func (s *Service) ListHumanActions(ctx context.Context, status, conversationID, runID string) ([]core.HumanAction, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = core.HumanActionWaitingUser
	}
	switch status {
	case "all", core.HumanActionReviewing, core.HumanActionWaitingUser, core.HumanActionApproved,
		core.HumanActionRejected, core.HumanActionAnswered, core.HumanActionExpired, core.HumanActionSuperseded:
	default:
		return nil, &core.ValidationError{Field: "status", Message: "is not a human action status"}
	}
	return s.repository.ListHumanActions(ctx, s.ScopeFor(ctx), status, strings.TrimSpace(conversationID), strings.TrimSpace(runID), 100)
}

func (s *Service) DecideHumanAction(ctx context.Context, actionID string, input core.DecideHumanActionInput, correlationID, idempotencyKey string) (core.HumanAction, error) {
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	input.Note = strings.TrimSpace(input.Note)
	if input.Scope == "" {
		input.Scope = core.ApprovalScopeOnce
	}
	switch input.Decision {
	case core.ApprovalApproved, core.ApprovalRejected, core.SideEffectDecisionConfirmApplied, core.SideEffectDecisionConfirmNotApplied, core.HumanActionAnswered:
	default:
		return core.HumanAction{}, &core.ValidationError{Field: "decision", Message: "is not supported"}
	}
	if input.Version < 1 {
		return core.HumanAction{}, &core.ValidationError{Field: "version", Message: "must be at least 1"}
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.HumanAction{}, err
	}
	request := struct {
		ActionID string                      `json:"action_id"`
		Input    core.DecideHumanActionInput `json:"input"`
	}{ActionID: actionID, Input: input}
	return s.repository.DecideHumanAction(ctx, s.ScopeFor(ctx), actionID, input, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListSideEffectReceipts(ctx context.Context, status string) ([]core.SideEffectReceipt, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = core.SideEffectReceiptUnknown
	}
	if status != "all" && status != core.SideEffectReceiptStarted && status != core.SideEffectReceiptSucceeded && status != core.SideEffectReceiptFailed && status != core.SideEffectReceiptUnknown {
		return nil, &core.ValidationError{Field: "status", Message: "must be started, succeeded, failed, unknown, or all"}
	}
	return s.repository.ListSideEffectReceipts(ctx, s.ScopeFor(ctx), status, 100)
}

// ListInbox aggregates human-action summaries across approvals and task
// recovery/input states. It never embeds frozen parameters or private task
// content; consumers must follow the linked resource after authorization.
func (s *Service) ListInbox(ctx context.Context) ([]core.InboxItem, error) {
	approvals, err := s.repository.ListApprovals(ctx, s.ScopeFor(ctx), core.ApprovalPending, 100)
	if err != nil {
		return nil, err
	}
	projects, err := s.repository.ListProjects(ctx, s.ScopeFor(ctx), 100)
	if err != nil {
		return nil, err
	}
	items := make([]core.InboxItem, 0, len(approvals))
	for _, approval := range approvals {
		items = append(items, core.InboxItem{ID: approval.ID, Kind: "approval", ProjectID: approval.ProjectID, TaskID: approval.TaskID, Title: approval.ToolName, Reason: approval.Reason, Status: approval.Status, UpdatedAt: approval.RequestedAt})
	}
	receipts, err := s.repository.ListSideEffectReceipts(ctx, s.ScopeFor(ctx), core.SideEffectReceiptUnknown, 100)
	if err != nil {
		return nil, err
	}
	for _, receipt := range receipts {
		items = append(items, core.InboxItem{ID: receipt.ID, Kind: "side_effect_unknown", ProjectID: receipt.ProjectID, TaskID: receipt.TaskID, Title: receipt.ToolName, Reason: "外部副作用结果未知，需要人工核查", Status: receipt.Status, UpdatedAt: receipt.StartedAt})
	}
	for _, project := range projects {
		tasks, listErr := s.repository.ListTasks(ctx, s.ScopeFor(ctx), project.ID, 100)
		if listErr != nil {
			return nil, listErr
		}
		for _, task := range tasks {
			kind, reason := "", ""
			switch task.Status {
			case core.StatusWaitingInput:
				kind, reason = "task_input", "任务需要补充输入"
			case core.StatusBlocked, core.StatusFailed:
				kind, reason = "task_recovery", taskInboxReason(task)
			}
			if kind == "" {
				continue
			}
			items = append(items, core.InboxItem{ID: task.ID, Kind: kind, ProjectID: task.ProjectID, TaskID: task.ID, Title: task.Title, Reason: reason, Status: task.Status, UpdatedAt: task.UpdatedAt})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].UpdatedAt.After(items[j].UpdatedAt) })
	return items, nil
}

func taskInboxReason(task core.Task) string {
	switch strings.TrimSpace(task.ErrorCode) {
	case "BUDGET_RESERVATION_EXCEEDED":
		return "本次运行的 Token 或模型调用配额不足，系统已保留执行进度。增加本次 Run 的配额后可从检查点继续。"
	case "MODEL_CREDENTIAL_MISSING":
		return "当前没有可用的模型配置或凭据，任务无法继续调用模型。"
	case "SIDE_EFFECT_STATUS_UNKNOWN":
		return "系统无法确认外部操作是否完成，需要先核查结果再决定是否继续。"
	}
	if summary := strings.TrimSpace(task.ResultSummary); summary != "" {
		return summary
	}
	if code := strings.TrimSpace(task.ErrorCode); code != "" {
		return "任务在执行过程中停止，请打开任务查看原因和可用处理方式。"
	}
	return "任务需要人工处理后才能继续。"
}

// ListInboxHistory returns handled human-action notifications. The repository
// applies the same project/task visibility rules as the live inbox.
func (s *Service) ListInboxHistory(ctx context.Context, limit int) ([]core.InboxHistoryItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repository.ListInboxHistory(ctx, s.ScopeFor(ctx), limit)
}

func (s *Service) ResolveSideEffect(ctx context.Context, receiptID string, input core.ResolveSideEffectInput, correlationID, idempotencyKey string) (core.SideEffectReceipt, error) {
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Note = strings.TrimSpace(input.Note)
	if err := core.ValidateResolveSideEffect(input); err != nil {
		return core.SideEffectReceipt{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.SideEffectReceipt{}, err
	}
	request := struct {
		ReceiptID string                      `json:"receipt_id"`
		Input     core.ResolveSideEffectInput `json:"input"`
	}{ReceiptID: receiptID, Input: input}
	return s.repository.ResolveSideEffect(ctx, s.ScopeFor(ctx), receiptID, input, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) DecideApproval(ctx context.Context, approvalID string, input core.DecideApprovalInput, correlationID, idempotencyKey string) (core.Approval, error) {
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.Scope == "" {
		input.Scope = core.ApprovalScopeOnce
	}
	input.Note = strings.TrimSpace(input.Note)
	if err := core.ValidateDecideApproval(input); err != nil {
		return core.Approval{}, err
	}
	idempotencyKey, err := normalizeIdempotencyKey(idempotencyKey)
	if err != nil {
		return core.Approval{}, err
	}
	request := struct {
		ApprovalID string                   `json:"approval_id"`
		Input      core.DecideApprovalInput `json:"input"`
	}{ApprovalID: approvalID, Input: input}
	return s.repository.DecideApproval(ctx, s.ScopeFor(ctx), approvalID, input, correlationID, core.Idempotency{Key: idempotencyKey, RequestHash: requestHash(request)})
}

func (s *Service) ListProjectApprovalRules(ctx context.Context, projectID string) ([]core.ApprovalRule, error) {
	if strings.TrimSpace(projectID) == "" {
		return nil, &core.ValidationError{Field: "project_id", Message: "is required"}
	}
	return s.repository.ListProjectApprovalRules(ctx, s.ScopeFor(ctx), projectID)
}

func (s *Service) RevokeProjectApprovalRule(ctx context.Context, projectID, ruleID, correlationID string) error {
	if strings.TrimSpace(projectID) == "" {
		return &core.ValidationError{Field: "project_id", Message: "is required"}
	}
	if strings.TrimSpace(ruleID) == "" {
		return &core.ValidationError{Field: "rule_id", Message: "is required"}
	}
	return s.repository.RevokeProjectApprovalRule(ctx, s.ScopeFor(ctx), projectID, ruleID, correlationID)
}

func (s *Service) ListConversationQueue(ctx context.Context, conversationID, status string) ([]core.ConversationQueueItem, error) {
	return s.repository.ListConversationQueue(ctx, s.ScopeFor(ctx), conversationID, status)
}

func (s *Service) PromoteConversationQueueToSteering(ctx context.Context, conversationID, queueItemID string) (core.ConversationQueueItem, error) {
	item, err := s.repository.PromoteConversationQueueItemToSteering(ctx, s.ScopeFor(ctx), conversationID, queueItemID)
	if err != nil {
		return core.ConversationQueueItem{}, err
	}
	return item, nil
}

func (s *Service) CancelConversationQueueItem(ctx context.Context, conversationID, queueItemID string) (core.ConversationQueueItem, error) {
	scope := s.ScopeFor(ctx)
	if _, err := s.repository.GetConversation(ctx, scope, conversationID); err != nil {
		return core.ConversationQueueItem{}, err
	}
	return s.repository.CancelConversationQueueItem(ctx, scope, conversationID, queueItemID)
}

func titleFromGoal(goal string) string {
	runes := []rune(strings.TrimSpace(goal))
	if len(runes) <= 48 {
		return string(runes)
	}
	return string(runes[:48]) + "…"
}

func requestHash(value any) string {
	body, _ := json.Marshal(value)
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func normalizeIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) < 8 || len(value) > 128 {
		return "", &core.ValidationError{Field: "Idempotency-Key", Message: "must contain 8 to 128 characters"}
	}
	return value, nil
}
