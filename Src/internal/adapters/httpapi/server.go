package httpapi

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi/generated"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type correlationKey struct{}

type Server struct {
	service   *application.Service
	logger    *slog.Logger
	activeSSE atomic.Int64
}

type mcpOAuthHTMLResponse string

func (response mcpOAuthHTMLResponse) VisitCompleteMCPOAuthResponse(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(http.StatusOK)
	_, err := io.WriteString(w, string(response))
	return err
}

func int64Ptr(value int64) *int64    { return &value }
func boolPtr(value bool) *bool       { return &value }
func stringPtr(value string) *string { return &value }

func NewHandler(service *application.Service, logger *slog.Logger) http.Handler {
	return newHandler(service, logger, nil, true)
}

func NewHandlerWithUI(service *application.Service, logger *slog.Logger, ui http.Handler) http.Handler {
	return newHandler(service, logger, ui, true)
}

// NewTrustedHandler is for in-process contract tests that exercise API shape
// with an already trusted service scope. Product servers must use NewHandler
// or NewHandlerWithUI so authentication cannot depend on UI availability.
func NewTrustedHandler(service *application.Service, logger *slog.Logger) http.Handler {
	return newHandler(service, logger, nil, false)
}

func newHandler(service *application.Service, logger *slog.Logger, ui http.Handler, enforceSession bool) http.Handler {
	server := &Server{service: service, logger: logger}
	principalResolver := sessionPrincipalResolver{
		service: service,
		adminToken: func() string {
			return strings.TrimSpace(os.Getenv("OKTAPOD_TEAM_ADMIN_TOKEN"))
		},
	}
	router := chi.NewRouter()
	router.Use(middleware.RealIP)
	router.Use(sessionAuthenticationMiddleware(service, principalResolver, enforceSession))
	router.Use(server.correlationMiddleware)
	router.Use(securityHeadersMiddleware)
	router.Use(middleware.Recoverer)
	router.Use(middleware.Timeout(3 * time.Minute))
	router.Use(requestBodyLimitMiddleware)
	router.Use(server.logMiddleware)
	router.Post("/api/v1/auth/login", server.localLogin)
	router.Post("/api/v1/auth/register", server.localRegister)
	router.Post("/api/v1/auth/logout", server.localLogout)
	strict := generated.NewStrictHandlerWithOptions(server, nil, generated.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  server.requestError,
		ResponseErrorHandlerFunc: server.responseError,
	})
	generated.HandlerWithOptions(strict, generated.ChiServerOptions{
		BaseURL:          "/api/v1",
		BaseRouter:       router,
		ErrorHandlerFunc: server.requestError,
	})
	// Invitation acceptance is a public bearer-token flow. Register this
	// boundary after the generated routes so chi dispatches it before the
	// generated handler and the one-time session can be sealed into the normal
	// HttpOnly/CSRF cookie pair.
	router.Post("/api/v1/invitations/accept", server.acceptInvitationHTTP)
	router.Get("/api/v1/runs/{run_id}/events/stream", server.streamRunEvents)
	router.Get("/api/v1/conversations/{conversation_id}/messages/stream", server.streamConversationMessages)
	router.Post("/api/v1/projects/{project_id}/turns/stream", server.streamProjectTurn)
	router.Get("/api/v1/artifacts/{artifact_id}/preview", server.previewArtifactContent)
	router.Get("/api/v1/assets/{asset_id}/preview", server.previewAssetContent)
	if ui == nil {
		router.Get("/", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "Oktapod", "api": "/api/v1", "status": "running"})
		})
	} else {
		router.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				server.writeProblem(w, r, http.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found", "the API route does not exist")
				return
			}
			ui.ServeHTTP(w, r)
		}))
	}
	return router
}

func (s *Server) previewArtifactContent(w http.ResponseWriter, r *http.Request) {
	artifactID := chi.URLParam(r, "artifact_id")
	if _, err := uuid.Parse(artifactID); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_ARTIFACT_ID", "Invalid artifact ID", "artifact_id must be a UUID")
		return
	}
	artifact, content, err := s.service.OpenArtifactContent(r.Context(), artifactID)
	if err != nil {
		s.writeProblem(w, r, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "Artifact not found", err.Error())
		return
	}
	writeInlinePreview(w, content, artifact.Name, artifact.MediaType, artifact.SizeBytes, artifact.ContentHash)
}

func (s *Server) previewAssetContent(w http.ResponseWriter, r *http.Request) {
	assetID := chi.URLParam(r, "asset_id")
	if _, err := uuid.Parse(assetID); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_ASSET_ID", "Invalid asset ID", "asset_id must be a UUID")
		return
	}
	asset, content, err := s.service.OpenAssetContent(r.Context(), assetID)
	if err != nil {
		s.writeProblem(w, r, http.StatusNotFound, "ASSET_NOT_FOUND", "Asset not found", err.Error())
		return
	}
	writeInlinePreview(w, content, asset.Name, asset.MediaType, asset.SizeBytes, asset.ContentHash)
}

func writeInlinePreview(w http.ResponseWriter, content io.ReadCloser, name, mediaType string, size int64, contentHash string) {
	defer content.Close()
	previewType := strings.ToLower(strings.TrimSpace(mediaType))
	if strings.HasSuffix(strings.ToLower(name), ".md") || previewType == "text/markdown" {
		previewType = "text/plain; charset=utf-8"
	} else if strings.HasPrefix(previewType, "text/") || previewType == "application/json" || previewType == "application/pdf" || strings.HasPrefix(previewType, "image/") {
		if strings.HasPrefix(previewType, "text/") && !strings.Contains(previewType, "charset=") {
			previewType += "; charset=utf-8"
		}
	} else {
		previewType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", previewType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(name)}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("X-Content-SHA256", contentHash)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, content)
}

func (s *Server) acceptInvitationHTTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil || len(strings.TrimSpace(input.Token)) < 32 {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_INVITATION_TOKEN", "Invalid invitation token", "token must be a valid one-time invitation token")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	invitation, err := s.service.AcceptProjectInvitation(r.Context(), strings.TrimSpace(input.Token), correlationID(r.Context()), idempotencyKey)
	if err != nil {
		s.writeProblem(w, r, http.StatusConflict, "INVITATION_ACCEPT_FAILED", "Invitation could not be accepted", err.Error())
		return
	}
	response, err := projectInvitationResponse(invitation)
	if err != nil {
		s.writeProblem(w, r, http.StatusInternalServerError, "INVITATION_RESPONSE_FAILED", "Invitation response failed", err.Error())
		return
	}
	if invitation.SessionToken != "" {
		csrf := uuid.New().String()
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: invitation.SessionToken, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(8 * time.Hour)})
		http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: csrf, Path: "/", HttpOnly: false, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(8 * time.Hour)})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(response)
}

type sessionPrincipalResolver struct {
	service    *application.Service
	adminToken func() string
}

func (r sessionPrincipalResolver) ResolvePrincipal(ctx context.Context, credential application.PrincipalCredential) (application.Principal, error) {
	if credential.Secret == "" {
		return application.Principal{}, application.ErrPrincipalRequired
	}
	if credential.Method == application.AuthenticationBearer {
		expected := r.adminToken()
		if expected != "" && subtle.ConstantTimeCompare([]byte(credential.Secret), []byte(expected)) == 1 {
			scope := r.service.Scope()
			return application.Principal{OrganizationID: scope.OrganizationID, UserID: scope.UserID, AuthenticationMethod: application.AuthenticationAdminToken, Administrator: true}, nil
		}
	}
	session, err := r.service.ResolveAuthSession(ctx, credential.Secret)
	if err != nil || session.OrganizationID != r.service.Scope().OrganizationID {
		return application.Principal{}, application.ErrPrincipalRequired
	}
	return application.Principal{OrganizationID: session.OrganizationID, UserID: session.UserID, AuthenticationMethod: credential.Method}, nil
}

func sessionAuthenticationMiddleware(service *application.Service, resolver application.PrincipalResolver, enforceSession bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/api/v1/") || r.URL.Path == "/api/v1/auth/login" || r.URL.Path == "/api/v1/auth/register" || r.URL.Path == "/api/v1/invitations/accept" || (r.URL.Path == "/api/v1/health" && enforceSession) {
				next.ServeHTTP(w, r)
				return
			}
			if !enforceSession {
				scope := service.Scope()
				principal := application.Principal{OrganizationID: scope.OrganizationID, UserID: scope.UserID, AuthenticationMethod: application.AuthenticationTrusted, Administrator: true}
				next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), principal)))
				return
			}
			provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if provided != "" {
				principal, err := resolver.ResolvePrincipal(r.Context(), application.PrincipalCredential{Method: application.AuthenticationBearer, Secret: provided})
				if err == nil {
					next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), principal)))
					return
				}
			}
			if cookie, err := r.Cookie(sessionCookieName); err == nil && strings.TrimSpace(cookie.Value) != "" {
				principal, err := resolver.ResolvePrincipal(r.Context(), application.PrincipalCredential{Method: application.AuthenticationSession, Secret: cookie.Value})
				if err == nil {
					if requiresCSRF(r.Method) && !csrfMatches(r) {
						writeProblemStatic(w, r, http.StatusForbidden, "CSRF_REQUIRED", "CSRF token required", "send X-CSRF-Token matching the oktapod_csrf cookie")
						return
					}
					next.ServeHTTP(w, r.WithContext(application.WithPrincipal(r.Context(), principal)))
					return
				}
			}
			writeProblemStatic(w, r, http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication required", "sign in with a valid local session")
		})
	}
}

const sessionCookieName = "oktapod_session"
const csrfCookieName = "oktapod_csrf"

func requiresCSRF(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func csrfMatches(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(strings.TrimSpace(r.Header.Get("X-CSRF-Token")))) == 1
}

func (s *Server) localLogin(w http.ResponseWriter, r *http.Request) {
	var input core.LocalLoginInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON", "request body must be valid JSON")
		return
	}
	session, err := s.service.AuthenticateLocal(r.Context(), input)
	if err != nil {
		writeProblemStatic(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid credentials", "username or password is incorrect")
		return
	}
	setSessionCookies(w, session)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"user_id": session.UserID, "display_name": session.DisplayName, "expires_at": session.ExpiresAt})
}

func (s *Server) localRegister(w http.ResponseWriter, r *http.Request) {
	var input core.LocalRegisterInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&input); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "Invalid JSON", "request body must be valid JSON")
		return
	}
	session, err := s.service.RegisterLocalUser(r.Context(), input)
	if err != nil {
		if errors.Is(err, core.ErrUsernameTaken) {
			writeProblemStatic(w, r, http.StatusConflict, "USERNAME_TAKEN", "Username unavailable", "choose another username")
			return
		}
		var validation *core.ValidationError
		if errors.As(err, &validation) {
			writeProblemStatic(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "Registration is invalid", validation.Error())
			return
		}
		s.writeProblem(w, r, http.StatusInternalServerError, "REGISTRATION_FAILED", "Registration failed", err.Error())
		return
	}
	setSessionCookies(w, session)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"user_id": session.UserID, "display_name": session.DisplayName, "expires_at": session.ExpiresAt})
}

func setSessionCookies(w http.ResponseWriter, session core.AuthSession) {
	csrf := uuid.New().String()
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: session.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt})
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: csrf, Path: "/", HttpOnly: false, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt})
}

func (s *Server) localLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if session, resolveErr := s.service.ResolveAuthSession(r.Context(), cookie.Value); resolveErr == nil {
			_ = s.service.RevokeAuthSession(r.Context(), session.ID)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func writeProblemStatic(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "https://oktapod.dev/problems/" + strings.ToLower(code), "title": title, "status": status, "detail": detail, "code": code, "correlation_id": correlationIDFromRequest(r)})
}

func correlationIDFromRequest(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-Request-ID")); value != "" {
		return value
	}
	return "unknown"
}

func (s *Server) Health(ctx context.Context, _ generated.HealthRequestObject) (generated.HealthResponseObject, error) {
	bootstrap, err := s.service.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	return generated.Health200JSONResponse{
		Status:   generated.HealthStatusOk,
		Profile:  generated.HealthProfile(bootstrap.Profile),
		Database: s.service.Database(),
	}, nil
}

func (s *Server) GetDiagnostics(ctx context.Context, _ generated.GetDiagnosticsRequestObject) (generated.GetDiagnosticsResponseObject, error) {
	diagnostics, err := s.service.Diagnostics(ctx)
	if err != nil {
		return nil, err
	}
	status := generated.DiagnosticsStatusOk
	if diagnostics.DatabaseStatus != "ok" {
		status = generated.DiagnosticsStatusDegraded
	}
	response := generated.GetDiagnostics200JSONResponse{
		Status:   status,
		Version:  diagnostics.Version,
		Profile:  diagnostics.Profile,
		Database: generated.DatabaseDiagnostics{Name: diagnostics.Database, Status: generated.DatabaseDiagnosticsStatus(diagnostics.DatabaseStatus)},
		Providers: generated.ProviderDiagnostics{
			Total:        diagnostics.ProviderCount,
			Active:       diagnostics.ActiveProviderCount,
			StaleCatalog: diagnostics.StaleCatalogCount,
		},
		Worker: generated.WorkerDiagnostics{
			QueuedRuns:       diagnostics.Worker.QueuedRuns,
			RunningRuns:      diagnostics.Worker.RunningRuns,
			WaitingApprovals: diagnostics.Worker.WaitingApprovals,
			ActiveLeases:     diagnostics.Worker.ActiveLeases,
		},
		Sse: generated.SseDiagnostics{ActiveConnections: s.activeSSE.Load()},
		Logs: generated.DiagnosticLogPolicy{
			Available: false,
			Redacted:  true,
			Detail:    "Raw process logs are not exposed by the read-only diagnostics API.",
		},
		Maintenance: generated.MaintenanceDiagnostics{
			BackupEndpointAvailable:  false,
			RestoreEndpointAvailable: false,
			Detail:                   "Backup and restore remain separate privileged operator workflows.",
		},
		AutoReview: generated.AutoReviewDiagnostics{Configured: diagnostics.AutoReview.Configured, Available: diagnostics.AutoReview.Available, Reason: diagnostics.AutoReview.Reason},
	}
	if diagnostics.Storage != nil {
		response.Storage = &generated.StorageDiagnostics{DataRoot: diagnostics.Storage.DataRoot, WorkspaceRoot: diagnostics.Storage.WorkspaceRoot}
	}
	return response, nil
}

func (s *Server) GetBootstrap(ctx context.Context, _ generated.GetBootstrapRequestObject) (generated.GetBootstrapResponseObject, error) {
	state, err := s.service.Bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	actorID, err := uuid.Parse(state.Actor.ID)
	if err != nil {
		return nil, err
	}
	orgID, err := uuid.Parse(state.Organization.ID)
	if err != nil {
		return nil, err
	}
	return generated.GetBootstrap200JSONResponse{
		Profile:         generated.BootstrapProfile(state.Profile),
		ModelEndpoint:   state.ModelEndpoint,
		ModelConfigured: state.ModelConfigured,
		User:            generated.Actor{Id: actorID, Username: &state.Actor.Username, DisplayName: state.Actor.DisplayName},
		Organization:    generated.Organization{Id: orgID, Name: state.Organization.Name},
		Capabilities: generated.RuntimeCapabilities{
			Os: state.Capabilities.OS, Sandbox: state.Capabilities.Sandbox,
			Filesystem: state.Capabilities.Filesystem, Command: state.Capabilities.Command,
			Network: state.Capabilities.Network, Browser: state.Capabilities.Browser,
			Mcp: state.Capabilities.MCP,
		},
	}, nil
}

func (s *Server) ListModels(ctx context.Context, _ generated.ListModelsRequestObject) (generated.ListModelsResponseObject, error) {
	models, err := s.service.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]generated.ModelDescriptor, 0, len(models))
	for _, model := range models {
		item := generated.ModelDescriptor{
			Id: model.ID, Vision: boolPtr(model.Vision),
			StructuredOutput: boolPtr(model.StructuredOutput), Reasoning: boolPtr(model.Reasoning),
			ContextWindow: int64Ptr(model.ContextWindow), ProviderId: stringPtr(model.ProviderID),
			ProviderName: stringPtr(model.ProviderName), Stale: boolPtr(model.Stale), Default: boolPtr(model.Default),
		}
		if model.Source != "" {
			source := generated.ModelDescriptorSource(model.Source)
			item.Source = &source
		}
		// 缺少能力元数据表示“未知”，不能序列化成 false 并让 UI 误判为不支持。
		if model.ToolCallingKnown {
			item.ToolCalling = boolPtr(model.ToolCalling)
		}
		items = append(items, item)
	}
	defaultModel, defaultErr := s.service.DefaultModel(ctx, models)
	if defaultErr != nil {
		defaultModel = ""
	}
	return generated.ListModels200JSONResponse{Items: items, DefaultModel: defaultModel}, nil
}

func (s *Server) ListProviders(ctx context.Context, _ generated.ListProvidersRequestObject) (generated.ListProvidersResponseObject, error) {
	providers, err := s.service.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]generated.ProviderInstance, 0, len(providers))
	for _, provider := range providers {
		items = append(items, providerInstanceResponse(provider))
	}
	return generated.ListProviders200JSONResponse{Items: items}, nil
}

func (s *Server) CreateProvider(ctx context.Context, request generated.CreateProviderRequestObject) (generated.CreateProviderResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	provider, err := s.service.SaveProvider(ctx, "", saveProviderInput(*request.Body))
	if err != nil {
		return nil, err
	}
	return generated.CreateProvider201JSONResponse(providerInstanceResponse(provider)), nil
}

func (s *Server) GetProvider(ctx context.Context, request generated.GetProviderRequestObject) (generated.GetProviderResponseObject, error) {
	provider, err := s.service.GetProvider(ctx, request.ProviderId)
	if err != nil {
		return nil, err
	}
	return generated.GetProvider200JSONResponse(providerInstanceResponse(provider)), nil
}

func (s *Server) UpdateProvider(ctx context.Context, request generated.UpdateProviderRequestObject) (generated.UpdateProviderResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	provider, err := s.service.SaveProvider(ctx, request.ProviderId, saveProviderInput(*request.Body))
	if err != nil {
		return nil, err
	}
	return generated.UpdateProvider200JSONResponse(providerInstanceResponse(provider)), nil
}

func (s *Server) DeleteProvider(ctx context.Context, request generated.DeleteProviderRequestObject) (generated.DeleteProviderResponseObject, error) {
	if err := s.service.DeleteProvider(ctx, request.ProviderId); err != nil {
		return nil, err
	}
	return generated.DeleteProvider204Response{}, nil
}

func (s *Server) TestProvider(ctx context.Context, request generated.TestProviderRequestObject) (generated.TestProviderResponseObject, error) {
	result, err := s.service.TestProvider(ctx, request.ProviderId)
	if err != nil {
		return nil, err
	}
	return generated.TestProvider200JSONResponse{
		ProviderId: result.ProviderID, Stage: result.Stage,
		LatencyMillis: result.LatencyMillis, CatalogStatus: result.CatalogStatus,
		ModelCount: result.ModelCount, RedactedError: result.RedactedError,
		CompletedAt: result.CompletedAt,
	}, nil
}

func (s *Server) ListProviderModels(ctx context.Context, request generated.ListProviderModelsRequestObject) (generated.ListProviderModelsResponseObject, error) {
	models, err := s.service.ListProviderModels(ctx, request.ProviderId)
	if err != nil {
		return nil, err
	}
	items := make([]generated.ProviderModelOption, 0, len(models))
	for _, model := range models {
		var capabilities map[string]any
		_ = json.Unmarshal([]byte(model.CapabilitiesJSON), &capabilities)
		items = append(items, providerModelOptionResponse(model, capabilities))
	}
	return generated.ListProviderModels200JSONResponse{Items: items}, nil
}

func (s *Server) RefreshProviderModels(ctx context.Context, request generated.RefreshProviderModelsRequestObject) (generated.RefreshProviderModelsResponseObject, error) {
	models, err := s.service.RefreshProviderModels(ctx, request.ProviderId)
	if err != nil {
		return nil, err
	}
	return generated.RefreshProviderModels200JSONResponse{Items: providerModelOptionResponses(models)}, nil
}

func (s *Server) SaveProviderModels(ctx context.Context, request generated.SaveProviderModelsRequestObject) (generated.SaveProviderModelsResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	activate := true
	if request.Body.Activate != nil {
		activate = *request.Body.Activate
	}
	models, err := s.service.SaveProviderModels(ctx, request.ProviderId, application.SaveProviderModelsInput{ModelIDs: request.Body.ModelIds, DefaultModelID: request.Body.DefaultModelId, Activate: activate})
	if err != nil {
		return nil, err
	}
	return generated.SaveProviderModels200JSONResponse{Items: providerModelOptionResponses(models)}, nil
}

func providerModelOptionResponses(models []core.ProviderModelOption) []generated.ProviderModelOption {
	items := make([]generated.ProviderModelOption, 0, len(models))
	for _, model := range models {
		var capabilities map[string]any
		_ = json.Unmarshal([]byte(model.CapabilitiesJSON), &capabilities)
		items = append(items, providerModelOptionResponse(model, capabilities))
	}
	return items
}

func providerModelOptionResponse(model core.ProviderModelOption, capabilities map[string]any) generated.ProviderModelOption {
	if capabilities == nil {
		capabilities = map[string]any{}
	}
	return generated.ProviderModelOption{
		ProviderId: model.ProviderID, ModelId: model.ModelID,
		Source: generated.ProviderModelOptionSource(model.Source), Enabled: model.Enabled, Default: model.Default,
		Capabilities: capabilities, ContextWindow: model.ContextWindow, MaxOutput: model.MaxOutput,
		FetchedAt: model.FetchedAt, ExpiresAt: model.ExpiresAt, CatalogVersion: model.CatalogVersion, Stale: model.Stale,
	}
}

func providerInstanceResponse(provider core.ProviderInstance) generated.ProviderInstance {
	return generated.ProviderInstance{
		ProviderId: provider.ProviderID, DisplayName: provider.DisplayName,
		ProtocolFamily: generated.ProviderInstanceProtocolFamily(provider.ProtocolFamily),
		Endpoint:       provider.Endpoint, DefaultModelId: provider.DefaultModelID,
		Status: generated.ProviderInstanceStatus(provider.Status), IsDefault: provider.IsDefault,
		CredentialConfigured: provider.CredentialExists,
		CreatedAt:            provider.CreatedAt, UpdatedAt: provider.UpdatedAt,
	}
}

func saveProviderInput(input generated.SaveProviderRequest) application.SaveProviderInput {
	status := ""
	if input.Status != nil {
		status = string(*input.Status)
	}
	defaultModel := ""
	if input.DefaultModelId != nil {
		defaultModel = *input.DefaultModelId
	}
	isDefault := input.IsDefault != nil && *input.IsDefault
	return application.SaveProviderInput{
		DisplayName: input.DisplayName, ProtocolFamily: string(input.ProtocolFamily),
		Endpoint: input.Endpoint, DefaultModelID: defaultModel,
		Status: status, IsDefault: isDefault, Credential: input.Credential,
	}
}

func (s *Server) CreateAuthSession(ctx context.Context, request generated.CreateAuthSessionRequestObject) (generated.CreateAuthSessionResponseObject, error) {
	if s.service.Profile() == core.ProfileTeam && !application.IsAdmin(ctx) {
		return nil, &core.ForbiddenError{Message: "only the Team administrator can issue sessions"}
	}
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	expires := int64(8 * 60 * 60)
	if request.Body.ExpiresInSeconds != nil {
		expires = int64(*request.Body.ExpiresInSeconds)
	}
	session, err := s.service.CreateAuthSession(ctx, core.CreateAuthSessionInput{UserID: request.Body.UserId.String(), ExpiresInSeconds: expires})
	if err != nil {
		return nil, err
	}
	response, err := authSessionResponse(session)
	if err != nil {
		return nil, err
	}
	return generated.CreateAuthSession201JSONResponse(response), nil
}

func (s *Server) RevokeAuthSession(ctx context.Context, request generated.RevokeAuthSessionRequestObject) (generated.RevokeAuthSessionResponseObject, error) {
	if s.service.Profile() == core.ProfileTeam && !application.IsAdmin(ctx) {
		return nil, &core.ForbiddenError{Message: "only the Team administrator can revoke sessions"}
	}
	if err := s.service.RevokeAuthSession(ctx, request.SessionId.String()); err != nil {
		return nil, err
	}
	return generated.RevokeAuthSession204Response{}, nil
}

func authSessionResponse(session core.AuthSession) (generated.AuthSession, error) {
	id, err := uuid.Parse(session.ID)
	if err != nil {
		return generated.AuthSession{}, err
	}
	organizationID, err := uuid.Parse(session.OrganizationID)
	if err != nil {
		return generated.AuthSession{}, err
	}
	userID, err := uuid.Parse(session.UserID)
	if err != nil {
		return generated.AuthSession{}, err
	}
	return generated.AuthSession{Id: id, OrganizationId: organizationID, UserId: userID, DisplayName: session.DisplayName, Token: session.Token, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt}, nil
}

func (s *Server) ListProjects(ctx context.Context, _ generated.ListProjectsRequestObject) (generated.ListProjectsResponseObject, error) {
	projects, err := s.service.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]generated.Project, 0, len(projects))
	for _, project := range projects {
		item, convertErr := projectResponse(project)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjects200JSONResponse{Items: items}, nil
}

func (s *Server) CreateProject(ctx context.Context, request generated.CreateProjectRequestObject) (generated.CreateProjectResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	description := ""
	if request.Body.Description != nil {
		description = *request.Body.Description
	}
	defaultModel := ""
	if request.Body.DefaultModel != nil {
		defaultModel = *request.Body.DefaultModel
	}
	fallbackModel := ""
	if request.Body.FallbackModel != nil {
		fallbackModel = *request.Body.FallbackModel
	}
	modelProviderID := ""
	if request.Body.ModelProviderId != nil {
		modelProviderID = *request.Body.ModelProviderId
	}
	project, err := s.service.CreateProject(ctx, core.CreateProjectInput{
		Name:            request.Body.Name,
		Description:     description,
		Kind:            string(request.Body.Kind),
		Scene:           string(request.Body.Scene),
		ModelProviderID: modelProviderID,
		DefaultModel:    defaultModel,
		FallbackModel:   fallbackModel,
	}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := projectResponse(project)
	if err != nil {
		return nil, err
	}
	return generated.CreateProject201JSONResponse(response), nil
}

func (s *Server) ListProjectMembers(ctx context.Context, request generated.ListProjectMembersRequestObject) (generated.ListProjectMembersResponseObject, error) {
	members, err := s.service.ListProjectMembers(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.ProjectMember, 0, len(members))
	for _, member := range members {
		item, convertErr := projectMemberResponse(member)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectMembers200JSONResponse{Items: items}, nil
}

func (s *Server) AddProjectMember(ctx context.Context, request generated.AddProjectMemberRequestObject) (generated.AddProjectMemberResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	member, err := s.service.AddProjectMember(ctx, request.ProjectId.String(), core.AddProjectMemberInput{
		Email: string(request.Body.Email), DisplayName: request.Body.DisplayName, Role: string(request.Body.Role),
	}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := projectMemberResponse(member)
	if err != nil {
		return nil, err
	}
	return generated.AddProjectMember201JSONResponse(response), nil
}

func (s *Server) RemoveProjectMember(ctx context.Context, request generated.RemoveProjectMemberRequestObject) (generated.RemoveProjectMemberResponseObject, error) {
	if err := s.service.RemoveProjectMember(ctx, request.ProjectId.String(), request.UserId.String(), correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey)); err != nil {
		return nil, err
	}
	return generated.RemoveProjectMember204Response{}, nil
}

func (s *Server) ListProjectInvitations(ctx context.Context, request generated.ListProjectInvitationsRequestObject) (generated.ListProjectInvitationsResponseObject, error) {
	invitations, err := s.service.ListProjectInvitations(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.ProjectInvitation, 0, len(invitations))
	for _, invitation := range invitations {
		item, convertErr := projectInvitationResponse(invitation)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectInvitations200JSONResponse{Items: items}, nil
}

func (s *Server) CreateProjectInvitation(ctx context.Context, request generated.CreateProjectInvitationRequestObject) (generated.CreateProjectInvitationResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	expiresIn := int64(0)
	if request.Body.ExpiresIn != nil {
		expiresIn = *request.Body.ExpiresIn
	}
	invitation, err := s.service.CreateProjectInvitation(ctx, request.ProjectId.String(), core.CreateProjectInvitationInput{Email: string(request.Body.Email), DisplayName: request.Body.DisplayName, Role: string(request.Body.Role), ExpiresIn: expiresIn}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := projectInvitationResponse(invitation)
	if err != nil {
		return nil, err
	}
	return generated.CreateProjectInvitation201JSONResponse(response), nil
}

func (s *Server) AcceptProjectInvitation(ctx context.Context, request generated.AcceptProjectInvitationRequestObject) (generated.AcceptProjectInvitationResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	invitation, err := s.service.AcceptProjectInvitation(ctx, request.Body.Token, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := projectInvitationResponse(invitation)
	if err != nil {
		return nil, err
	}
	return generated.AcceptProjectInvitation200JSONResponse(response), nil
}

func (s *Server) RevokeProjectInvitation(ctx context.Context, request generated.RevokeProjectInvitationRequestObject) (generated.RevokeProjectInvitationResponseObject, error) {
	if err := s.service.RevokeProjectInvitation(ctx, request.InvitationId.String(), correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey)); err != nil {
		return nil, err
	}
	return generated.RevokeProjectInvitation204Response{}, nil
}

func (s *Server) GetProject(ctx context.Context, request generated.GetProjectRequestObject) (generated.GetProjectResponseObject, error) {
	project, err := s.service.GetProject(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	response, err := projectResponse(project)
	if err != nil {
		return nil, err
	}
	return generated.GetProject200JSONResponse(response), nil
}

func (s *Server) UpdateProjectSettings(ctx context.Context, request generated.UpdateProjectSettingsRequestObject) (generated.UpdateProjectSettingsResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	input := core.UpdateProjectSettingsInput{}
	if request.Body.ModelProviderId != nil {
		value := *request.Body.ModelProviderId
		input.ModelProviderID = &value
	}
	if request.Body.DefaultModel != nil {
		value := *request.Body.DefaultModel
		input.DefaultModel = &value
	}
	if request.Body.FallbackModel != nil {
		value := *request.Body.FallbackModel
		input.FallbackModel = &value
	}
	input.MaxModelCalls = request.Body.MaxModelCalls
	input.MaxTokens = request.Body.MaxTokens
	project, err := s.service.UpdateProjectSettings(ctx, request.ProjectId.String(), input, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := projectResponse(project)
	if err != nil {
		return nil, err
	}
	return generated.UpdateProjectSettings200JSONResponse(response), nil
}

func (s *Server) ListProjectTasks(ctx context.Context, request generated.ListProjectTasksRequestObject) (generated.ListProjectTasksResponseObject, error) {
	tasks, err := s.service.ListTasks(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.Task, 0, len(tasks))
	for _, task := range tasks {
		item, convertErr := taskResponse(task)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectTasks200JSONResponse{Items: items}, nil
}

func (s *Server) ListProjectConversations(ctx context.Context, request generated.ListProjectConversationsRequestObject) (generated.ListProjectConversationsResponseObject, error) {
	conversations, err := s.service.ListConversations(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.Conversation, 0, len(conversations))
	for _, conversation := range conversations {
		item, convertErr := conversationResponse(conversation)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectConversations200JSONResponse{Items: items}, nil
}

func (s *Server) DeleteConversation(ctx context.Context, request generated.DeleteConversationRequestObject) (generated.DeleteConversationResponseObject, error) {
	if err := s.service.DeleteConversation(ctx, request.ConversationId.String(), correlationID(ctx)); err != nil {
		return nil, err
	}
	return generated.DeleteConversation204Response{}, nil
}

func (s *Server) RenameConversation(ctx context.Context, request generated.RenameConversationRequestObject) (generated.RenameConversationResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	conversation, err := s.service.RenameConversation(ctx, request.ConversationId.String(), request.Body.Title)
	if err != nil {
		return nil, err
	}
	response, err := conversationResponse(conversation)
	if err != nil {
		return nil, err
	}
	return generated.RenameConversation200JSONResponse(response), nil
}

func (s *Server) ListConversationMessages(ctx context.Context, request generated.ListConversationMessagesRequestObject) (generated.ListConversationMessagesResponseObject, error) {
	after := int64(0)
	if request.Params.After != nil {
		after = *request.Params.After
	}
	messages, err := s.service.ListMessages(ctx, request.ConversationId.String(), after)
	if err != nil {
		return nil, err
	}
	items := make([]generated.Message, 0, len(messages))
	next := after
	for _, message := range messages {
		item, convertErr := messageResponse(message)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
		if message.ServerSeq > next {
			next = message.ServerSeq
		}
	}
	return generated.ListConversationMessages200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) ListSessionEntries(ctx context.Context, request generated.ListSessionEntriesRequestObject) (generated.ListSessionEntriesResponseObject, error) {
	after := int64(0)
	if request.Params.AfterEntrySeq != nil {
		after = *request.Params.AfterEntrySeq
	}
	limit := 200
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	page, err := s.service.ListSessionEntries(ctx, request.ConversationId.String(), after, limit)
	if err != nil {
		return nil, err
	}
	items := make([]generated.SessionEntry, 0, len(page.Entries))
	for _, entry := range page.Entries {
		item, convertErr := sessionEntryResponse(entry)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListSessionEntries200JSONResponse{Items: items, ActiveLeafEntryId: page.ActiveLeafEntryID, NextEntrySeq: page.NextEntrySeq}, nil
}

func (s *Server) GetSessionEntryPath(ctx context.Context, request generated.GetSessionEntryPathRequestObject) (generated.GetSessionEntryPathResponseObject, error) {
	path, err := s.service.GetSessionEntryPath(ctx, request.EntryId)
	if err != nil {
		return nil, err
	}
	items := make([]generated.SessionEntry, 0, len(path))
	for _, entry := range path {
		item, convertErr := sessionEntryResponse(entry)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.GetSessionEntryPath200JSONResponse{Items: items}, nil
}

func (s *Server) NavigateSession(ctx context.Context, request generated.NavigateSessionRequestObject) (generated.NavigateSessionResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	result, err := s.service.NavigateSession(ctx, request.ConversationId.String(), core.NavigateSessionInput{
		EntryID:             request.Body.EntryId,
		ExpectedLeafEntryID: request.Body.ExpectedLeafEntryId,
	})
	if err != nil {
		return nil, err
	}
	conversation, err := conversationResponse(result.Conversation)
	if err != nil {
		return nil, err
	}
	entry, err := sessionEntryResponse(result.Entry)
	if err != nil {
		return nil, err
	}
	return generated.NavigateSession200JSONResponse{Conversation: conversation, Entry: entry, ComposerText: result.ComposerText}, nil
}

func (s *Server) ForkSession(ctx context.Context, request generated.ForkSessionRequestObject) (generated.ForkSessionResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	title := ""
	if request.Body.Title != nil {
		title = *request.Body.Title
	}
	conversation, err := s.service.ForkSession(ctx, request.ConversationId.String(), core.ForkSessionInput{EntryID: request.Body.EntryId, Title: title})
	if err != nil {
		return nil, err
	}
	response, err := conversationResponse(conversation)
	if err != nil {
		return nil, err
	}
	return generated.ForkSession201JSONResponse(response), nil
}

func (s *Server) CloneSession(ctx context.Context, request generated.CloneSessionRequestObject) (generated.CloneSessionResponseObject, error) {
	title := ""
	if request.Body != nil && request.Body.Title != nil {
		title = *request.Body.Title
	}
	conversation, err := s.service.CloneSession(ctx, request.ConversationId.String(), title)
	if err != nil {
		return nil, err
	}
	response, err := conversationResponse(conversation)
	if err != nil {
		return nil, err
	}
	return generated.CloneSession201JSONResponse(response), nil
}

func (s *Server) LabelSessionEntry(ctx context.Context, request generated.LabelSessionEntryRequestObject) (generated.LabelSessionEntryResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	entry, err := s.service.LabelSessionEntry(ctx, request.ConversationId.String(), request.EntryId, core.LabelSessionEntryInput{Label: request.Body.Label, ExpectedLeafEntryID: request.Body.ExpectedLeafEntryId})
	if err != nil {
		return nil, err
	}
	response, err := sessionEntryResponse(entry)
	if err != nil {
		return nil, err
	}
	return generated.LabelSessionEntry201JSONResponse(response), nil
}

func (s *Server) AppendBranchSummary(ctx context.Context, request generated.AppendBranchSummaryRequestObject) (generated.AppendBranchSummaryResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	summary := ""
	if request.Body.Summary != nil {
		summary = *request.Body.Summary
	}
	entry, err := s.service.AppendBranchSummary(ctx, request.ConversationId.String(), core.BranchSummaryInput{Summary: summary, ExpectedLeafEntryID: request.Body.ExpectedLeafEntryId})
	if err != nil {
		return nil, err
	}
	response, err := sessionEntryResponse(entry)
	if err != nil {
		return nil, err
	}
	return generated.AppendBranchSummary201JSONResponse(response), nil
}

func (s *Server) ListConversationQueue(ctx context.Context, request generated.ListConversationQueueRequestObject) (generated.ListConversationQueueResponseObject, error) {
	status := "pending"
	if request.Params.Status != nil {
		status = string(*request.Params.Status)
	}
	items, err := s.service.ListConversationQueue(ctx, request.ConversationId.String(), status)
	if err != nil {
		return nil, err
	}
	response := make([]generated.ConversationQueueItem, 0, len(items))
	for _, item := range items {
		converted, convertErr := conversationQueueItemResponse(item)
		if convertErr != nil {
			return nil, convertErr
		}
		response = append(response, converted)
	}
	return generated.ListConversationQueue200JSONResponse{Items: response}, nil
}

func (s *Server) CancelConversationQueueItem(ctx context.Context, request generated.CancelConversationQueueItemRequestObject) (generated.CancelConversationQueueItemResponseObject, error) {
	item, err := s.service.CancelConversationQueueItem(ctx, request.ConversationId.String(), request.QueueItemId.String())
	if err != nil {
		return nil, err
	}
	response, err := conversationQueueItemResponse(item)
	if err != nil {
		return nil, err
	}
	return generated.CancelConversationQueueItem200JSONResponse{Item: response}, nil
}

func (s *Server) SteerConversation(ctx context.Context, request generated.SteerConversationRequestObject) (generated.SteerConversationResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	item, err := s.service.PromoteConversationQueueToSteering(ctx, request.ConversationId.String(), request.Body.QueueItemId.String())
	if err != nil {
		return nil, err
	}
	response, err := conversationQueueItemResponse(item)
	if err != nil {
		return nil, err
	}
	return generated.SteerConversation200JSONResponse{Item: response}, nil
}

func (s *Server) AppendConversationMessage(ctx context.Context, request generated.AppendConversationMessageRequestObject) (generated.AppendConversationMessageResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	input := core.CreateTaskInput{Goal: request.Body.Content, Mode: core.ModeAuto, PermissionMode: core.PermissionModeDefault}
	if request.Body.Acceptance != nil {
		input.Acceptance = *request.Body.Acceptance
	}
	if request.Body.Mode != nil {
		input.Mode = string(*request.Body.Mode)
	}
	if request.Body.Visibility != nil {
		input.Visibility = string(*request.Body.Visibility)
	}
	if request.Body.PermissionMode != nil {
		input.PermissionMode = string(*request.Body.PermissionMode)
	}
	if request.Body.Scene != nil {
		input.SceneOverride = string(*request.Body.Scene)
	}
	if request.Body.Model != nil {
		input.Model = *request.Body.Model
	}
	if request.Body.FallbackModel != nil {
		input.FallbackModel = *request.Body.FallbackModel
	}
	if request.Body.ProviderId != nil {
		input.ProviderID = *request.Body.ProviderId
	}
	if request.Body.Thinking != nil {
		input.Thinking = string(*request.Body.Thinking)
	}
	if request.Body.MaxModelCalls != nil {
		input.MaxModelCalls = *request.Body.MaxModelCalls
	}
	if request.Body.MaxTokens != nil {
		input.MaxTokens = *request.Body.MaxTokens
	}
	if request.Body.AgentId != nil {
		input.AgentID = request.Body.AgentId.String()
	}
	if request.Body.TeamDefinitionId != nil {
		input.TeamDefinitionID = request.Body.TeamDefinitionId.String()
	}
	if request.Body.TeamRoles != nil {
		input.TeamRoles = make([]core.TeamRole, 0, len(*request.Body.TeamRoles))
		for _, role := range *request.Body.TeamRoles {
			input.TeamRoles = append(input.TeamRoles, core.TeamRole{Role: role.Role, Name: role.Name})
		}
	}
	if request.Body.ContextAssetIds != nil {
		input.ContextAssetIDs = make([]string, 0, len(*request.Body.ContextAssetIds))
		for _, id := range *request.Body.ContextAssetIds {
			input.ContextAssetIDs = append(input.ContextAssetIDs, id.String())
		}
	}
	if request.Body.ContextMemoryIds != nil {
		input.ContextMemoryIDs = make([]string, 0, len(*request.Body.ContextMemoryIds))
		for _, id := range *request.Body.ContextMemoryIds {
			input.ContextMemoryIDs = append(input.ContextMemoryIDs, id.String())
		}
	}
	if request.Body.SkillId != nil {
		input.SkillID = request.Body.SkillId.String()
	}
	if request.Body.SkillArgs != nil {
		input.SkillArgs = *request.Body.SkillArgs
	}
	task, err := s.service.AppendConversationMessage(ctx, request.ConversationId.String(), input, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := taskResponse(task)
	if err != nil {
		return nil, err
	}
	return generated.AppendConversationMessage202JSONResponse(response), nil
}

func (s *Server) SubmitProjectTurn(ctx context.Context, request generated.SubmitProjectTurnRequestObject) (generated.SubmitProjectTurnResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	input := projectTurnInput(request.Body)
	result, err := s.service.SubmitProjectTurn(ctx, request.ProjectId.String(), input, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := projectTurnResponse(result)
	if err != nil {
		return nil, err
	}
	return generated.SubmitProjectTurn200JSONResponse(response), nil
}

func (s *Server) ImportProjectTranscript(ctx context.Context, request generated.ImportProjectTranscriptRequestObject) (generated.ImportProjectTranscriptResponseObject, error) {
	input := core.ImportTranscriptInput{SourceFormat: string(request.Body.SourceFormat), Conversations: make([]core.PortableTranscriptConversation, 0, len(request.Body.Conversations))}
	for _, source := range request.Body.Conversations {
		conversation := core.PortableTranscriptConversation{Title: source.Title, Messages: make([]core.PortableTranscriptMessage, 0, len(source.Messages))}
		for _, message := range source.Messages {
			conversation.Messages = append(conversation.Messages, core.PortableTranscriptMessage{Role: string(message.Role), Content: message.Content, CreatedAt: message.CreatedAt})
		}
		input.Conversations = append(input.Conversations, conversation)
	}
	result, err := s.service.ImportTranscript(ctx, request.ProjectId.String(), input, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	projectID, err := uuid.Parse(result.ProjectID)
	if err != nil {
		return nil, err
	}
	conversationIDs := make([]uuid.UUID, 0, len(result.ConversationIDs))
	for _, value := range result.ConversationIDs {
		id, parseErr := uuid.Parse(value)
		if parseErr != nil {
			return nil, parseErr
		}
		conversationIDs = append(conversationIDs, id)
	}
	return generated.ImportProjectTranscript201JSONResponse{
		ProjectId: projectID, ConversationIds: conversationIDs, MessageCount: result.MessageCount,
	}, nil
}

func projectTurnInput(body *generated.ProjectTurnRequest) core.ProjectTurnInput {
	input := core.ProjectTurnInput{Content: body.Content, Mode: core.ModeAuto, Visibility: core.VisibilityPrivate, PermissionMode: core.PermissionModeDefault}
	if body.ConversationId != nil {
		input.ConversationID = body.ConversationId.String()
	}
	if body.Acceptance != nil {
		input.Acceptance = *body.Acceptance
	}
	if body.Mode != nil {
		input.Mode = string(*body.Mode)
	}
	if body.Visibility != nil {
		input.Visibility = string(*body.Visibility)
	}
	if body.PermissionMode != nil {
		input.PermissionMode = string(*body.PermissionMode)
	}
	if body.Scene != nil {
		input.SceneOverride = string(*body.Scene)
	}
	if body.Model != nil {
		input.Model = *body.Model
	}
	if body.FallbackModel != nil {
		input.FallbackModel = *body.FallbackModel
	}
	if body.ProviderId != nil {
		input.ProviderID = *body.ProviderId
	}
	if body.Thinking != nil {
		input.Thinking = string(*body.Thinking)
	}
	if body.MaxModelCalls != nil {
		input.MaxModelCalls = *body.MaxModelCalls
	}
	if body.MaxTokens != nil {
		input.MaxTokens = *body.MaxTokens
	}
	if body.AgentId != nil {
		input.AgentID = body.AgentId.String()
	}
	if body.TeamDefinitionId != nil {
		input.TeamDefinitionID = body.TeamDefinitionId.String()
	}
	if body.TeamRoles != nil {
		input.TeamRoles = make([]core.TeamRole, 0, len(*body.TeamRoles))
		for _, role := range *body.TeamRoles {
			input.TeamRoles = append(input.TeamRoles, core.TeamRole{Role: role.Role, Name: role.Name})
		}
	}
	if body.ContextAssetIds != nil {
		for _, id := range *body.ContextAssetIds {
			input.ContextAssetIDs = append(input.ContextAssetIDs, id.String())
		}
	}
	if body.ContextMemoryIds != nil {
		for _, id := range *body.ContextMemoryIds {
			input.ContextMemoryIDs = append(input.ContextMemoryIDs, id.String())
		}
	}
	if body.SkillId != nil {
		input.SkillID = body.SkillId.String()
	}
	if body.SkillArgs != nil {
		input.SkillArgs = *body.SkillArgs
	}
	if body.QueueKind != nil {
		input.QueueKind = string(*body.QueueKind)
	}
	return input
}

func projectTurnResponse(result core.ProjectTurnResult) (generated.ProjectTurnResponse, error) {
	conversation, err := conversationResponse(result.Conversation)
	if err != nil {
		return generated.ProjectTurnResponse{}, err
	}
	messages := make([]generated.Message, 0, len(result.Messages))
	for _, item := range result.Messages {
		message, convertErr := messageResponse(item)
		if convertErr != nil {
			return generated.ProjectTurnResponse{}, convertErr
		}
		messages = append(messages, message)
	}
	response := generated.ProjectTurnResponse{Route: generated.ProjectTurnResponseRoute(result.Route), Conversation: conversation, Messages: messages}
	if result.Task != nil {
		task, convertErr := taskResponse(*result.Task)
		if convertErr != nil {
			return generated.ProjectTurnResponse{}, convertErr
		}
		response.Task = &task
	}
	if result.Queued {
		response.Queued = &result.Queued
	}
	if result.QueueItem != nil {
		item, err := conversationQueueItemResponse(*result.QueueItem)
		if err != nil {
			return generated.ProjectTurnResponse{}, err
		}
		response.QueueItem = &item
	}
	if result.ResourceInstallDraft != nil {
		draft, convertErr := resourceInstallDraftResponse(*result.ResourceInstallDraft)
		if convertErr != nil {
			return generated.ProjectTurnResponse{}, convertErr
		}
		response.ResourceInstallDraft = &draft
	}
	return response, nil
}

func conversationQueueItemResponse(item core.ConversationQueueItem) (generated.ConversationQueueItem, error) {
	id, err := uuid.Parse(item.ID)
	if err != nil {
		return generated.ConversationQueueItem{}, err
	}
	conversationID, err := uuid.Parse(item.ConversationID)
	if err != nil {
		return generated.ConversationQueueItem{}, err
	}
	activeTaskID, err := uuid.Parse(item.ActiveTaskID)
	if err != nil {
		return generated.ConversationQueueItem{}, err
	}
	return generated.ConversationQueueItem{
		Id:             id,
		ConversationId: conversationID,
		ActiveTaskId:   activeTaskID,
		Content:        item.Content,
		Kind:           generated.ConversationQueueItemKind(item.Kind),
		Status:         generated.ConversationQueueItemStatus(item.Status),
		CreatedAt:      item.CreatedAt,
	}, nil
}

func (s *Server) streamProjectTurn(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeProblem(w, r, http.StatusNotImplemented, "STREAMING_UNAVAILABLE", "Streaming unavailable", "the HTTP server does not support response flushing")
		return
	}
	projectID := chi.URLParam(r, "project_id")
	if _, err := uuid.Parse(projectID); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_PROJECT_ID", "Invalid project ID", "project_id must be a UUID")
		return
	}
	var body generated.ProjectTurnRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&body); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_JSON", "Invalid request", "request body must be valid JSON")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	s.activeSSE.Add(1)
	defer s.activeSSE.Add(-1)
	send := func(event string, payload any) error {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	result, err := s.service.SubmitProjectTurnStream(r.Context(), projectID, projectTurnInput(&body), correlationID(r.Context()), strings.TrimSpace(r.Header.Get("Idempotency-Key")), func(route string) error {
		return send("route", map[string]string{"route": route})
	}, func(delta string) error {
		return send("assistant_delta", map[string]string{"delta": delta})
	})
	if err != nil {
		code := "TURN_STREAM_ERROR"
		if _, ok := err.(*application.ModelTurnUnavailableError); ok {
			code = "MODEL_TURN_UNAVAILABLE"
		}
		_ = send("error", map[string]string{"code": code, "detail": "模型响应中断，请重试", "correlation_id": correlationID(r.Context())})
		return
	}
	response, err := projectTurnResponse(result)
	if err != nil {
		_ = send("error", map[string]string{"code": "TURN_RESPONSE_ERROR", "detail": "会话结果无法保存", "correlation_id": correlationID(r.Context())})
		return
	}
	_ = send("done", response)
}

func (s *Server) ListProjectActivity(ctx context.Context, request generated.ListProjectActivityRequestObject) (generated.ListProjectActivityResponseObject, error) {
	before := ""
	if request.Params.Before != nil {
		before = request.Params.Before.Format(time.RFC3339Nano)
	}
	activity, err := s.service.ListProjectActivity(ctx, request.ProjectId.String(), before)
	if err != nil {
		return nil, err
	}
	items := make([]generated.ProjectActivity, 0, len(activity))
	for _, event := range activity {
		item, convertErr := projectActivityResponse(event)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectActivity200JSONResponse{Items: items}, nil
}

func (s *Server) ListProjectPlanItems(ctx context.Context, request generated.ListProjectPlanItemsRequestObject) (generated.ListProjectPlanItemsResponseObject, error) {
	planItems, err := s.service.ListPlanItems(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.PlanItem, 0, len(planItems))
	for _, planItem := range planItems {
		item, convertErr := planItemResponse(planItem)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectPlanItems200JSONResponse{Items: items}, nil
}

func (s *Server) CreatePlanItem(ctx context.Context, request generated.CreatePlanItemRequestObject) (generated.CreatePlanItemResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	description := ""
	if request.Body.Description != nil {
		description = *request.Body.Description
	}
	planItem, err := s.service.CreatePlanItem(ctx, request.ProjectId.String(), core.CreatePlanItemInput{
		Title: request.Body.Title, Description: description, Visibility: string(request.Body.Visibility),
	}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := planItemResponse(planItem)
	if err != nil {
		return nil, err
	}
	return generated.CreatePlanItem201JSONResponse(response), nil
}

func (s *Server) MovePlanItem(ctx context.Context, request generated.MovePlanItemRequestObject) (generated.MovePlanItemResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	planItem, err := s.service.MovePlanItem(ctx, request.PlanItemId.String(), core.MovePlanItemInput{Status: string(request.Body.Status)}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := planItemResponse(planItem)
	if err != nil {
		return nil, err
	}
	return generated.MovePlanItem200JSONResponse(response), nil
}

func (s *Server) CreateAssetFolder(ctx context.Context, request generated.CreateAssetFolderRequestObject) (generated.CreateAssetFolderResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	parentID := ""
	if request.Body.ParentId != nil {
		parentID = request.Body.ParentId.String()
	}
	asset, err := s.service.CreateAssetFolder(ctx, request.ProjectId.String(), core.CreateAssetFolderInput{
		Name: request.Body.Name, ParentID: parentID, Visibility: string(request.Body.Visibility),
	}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := assetResponse(asset)
	if err != nil {
		return nil, err
	}
	return generated.CreateAssetFolder201JSONResponse(response), nil
}

func (s *Server) ListProjectAssets(ctx context.Context, request generated.ListProjectAssetsRequestObject) (generated.ListProjectAssetsResponseObject, error) {
	parentID := ""
	query := ""
	kind := ""
	if request.Params.ParentId != nil {
		parentID = request.Params.ParentId.String()
	}
	if request.Params.Q != nil {
		query = *request.Params.Q
	}
	if request.Params.Kind != nil {
		kind = string(*request.Params.Kind)
	}
	collection, err := s.service.ListAssets(ctx, request.ProjectId.String(), parentID, query, kind)
	if err != nil {
		return nil, err
	}
	items := make([]generated.Asset, 0, len(collection.Items))
	for _, asset := range collection.Items {
		item, convertErr := assetResponse(asset)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectAssets200JSONResponse{Items: items, VisibleBytes: collection.VisibleBytes, QuotaBytes: collection.QuotaBytes}, nil
}

func (s *Server) UploadProjectAsset(ctx context.Context, request generated.UploadProjectAssetRequestObject) (generated.UploadProjectAssetResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	form, err := request.Body.ReadForm(64 * 1024)
	if err != nil {
		return nil, err
	}
	defer form.RemoveAll()
	files := form.File["file"]
	if len(files) != 1 {
		return nil, &core.ValidationError{Field: "file", Message: "exactly one file is required"}
	}
	visibility := firstFormValue(form.Value["visibility"])
	parentID := firstFormValue(form.Value["parent_id"])
	fileHeader := files[0]
	file, err := fileHeader.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()
	mediaType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if mediaType == "" || mediaType == "application/octet-stream" {
		prefix := make([]byte, 512)
		read, readErr := file.Read(prefix)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		mediaType = http.DetectContentType(prefix[:read])
	}
	asset, err := s.service.UploadAsset(ctx, request.ProjectId.String(), core.UploadAssetInput{
		Name: fileHeader.Filename, ParentID: parentID, Visibility: visibility, MediaType: mediaType, SizeHint: fileHeader.Size,
	}, file)
	if err != nil {
		return nil, err
	}
	response, err := assetResponse(asset)
	if err != nil {
		return nil, err
	}
	return generated.UploadProjectAsset201JSONResponse(response), nil
}

func (s *Server) DownloadAssetContent(ctx context.Context, request generated.DownloadAssetContentRequestObject) (generated.DownloadAssetContentResponseObject, error) {
	asset, content, err := s.service.OpenAssetContent(ctx, request.AssetId.String())
	if err != nil {
		return nil, err
	}
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": asset.Name})
	contentLength := asset.SizeBytes
	contentHash := asset.ContentHash
	return generated.DownloadAssetContent200ApplicationoctetStreamResponse{
		Body: content, ContentLength: asset.SizeBytes,
		Headers: generated.DownloadAssetContent200ResponseHeaders{ContentDisposition: disposition, ContentLength: contentLength, XContentSHA256: contentHash},
	}, nil
}

func (s *Server) DownloadArtifactContent(ctx context.Context, request generated.DownloadArtifactContentRequestObject) (generated.DownloadArtifactContentResponseObject, error) {
	artifact, content, err := s.service.OpenArtifactContent(ctx, request.ArtifactId.String())
	if err != nil {
		return nil, err
	}
	disposition := mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(artifact.Name)})
	return generated.DownloadArtifactContent200ApplicationoctetStreamResponse{
		Body: content, ContentLength: artifact.SizeBytes,
		Headers: generated.DownloadArtifactContent200ResponseHeaders{ContentDisposition: disposition, ContentLength: artifact.SizeBytes, XContentSHA256: artifact.ContentHash},
	}, nil
}

func (s *Server) ListProjectSchedules(ctx context.Context, request generated.ListProjectSchedulesRequestObject) (generated.ListProjectSchedulesResponseObject, error) {
	schedules, err := s.service.ListSchedules(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.Schedule, 0, len(schedules))
	for _, schedule := range schedules {
		item, convertErr := scheduleResponse(schedule)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectSchedules200JSONResponse{Items: items}, nil
}

func (s *Server) CreateSchedule(ctx context.Context, request generated.CreateScheduleRequestObject) (generated.CreateScheduleResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	acceptance := ""
	if request.Body.Acceptance != nil {
		acceptance = *request.Body.Acceptance
	}
	input := core.CreateScheduleInput{
		Name: request.Body.Name, CronExpression: request.Body.CronExpression, Timezone: request.Body.Timezone,
		Goal: request.Body.Goal, Acceptance: acceptance, Mode: string(request.Body.Mode), Visibility: string(request.Body.Visibility),
	}
	if request.Body.MisfirePolicy != nil {
		input.MisfirePolicy = string(*request.Body.MisfirePolicy)
	}
	if request.Body.OverlapPolicy != nil {
		input.OverlapPolicy = string(*request.Body.OverlapPolicy)
	}
	if request.Body.MisfireGraceSeconds != nil {
		input.MisfireGraceSeconds = *request.Body.MisfireGraceSeconds
	}
	schedule, err := s.service.CreateSchedule(ctx, request.ProjectId.String(), input, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := scheduleResponse(schedule)
	if err != nil {
		return nil, err
	}
	return generated.CreateSchedule201JSONResponse(response), nil
}

func (s *Server) SetScheduleStatus(ctx context.Context, request generated.SetScheduleStatusRequestObject) (generated.SetScheduleStatusResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	schedule, err := s.service.SetScheduleStatus(ctx, request.ScheduleId.String(), string(request.Body.Status), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := scheduleResponse(schedule)
	if err != nil {
		return nil, err
	}
	return generated.SetScheduleStatus200JSONResponse(response), nil
}

func (s *Server) ListProjectCapabilities(ctx context.Context, request generated.ListProjectCapabilitiesRequestObject) (generated.ListProjectCapabilitiesResponseObject, error) {
	capabilities, err := s.service.ListCapabilities(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.Capability, 0, len(capabilities))
	for _, capability := range capabilities {
		item, convertErr := capabilityResponse(capability)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectCapabilities200JSONResponse{Items: items}, nil
}

func (s *Server) CreateCapability(ctx context.Context, request generated.CreateCapabilityRequestObject) (generated.CreateCapabilityResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	version := ""
	if request.Body.Version != nil {
		version = *request.Body.Version
	}
	scopeType := ""
	if request.Body.ScopeType != nil {
		scopeType = string(*request.Body.ScopeType)
	}
	sourceFilename := ""
	if request.Body.SourceFilename != nil {
		sourceFilename = *request.Body.SourceFilename
	}
	bundleBase64 := ""
	if request.Body.BundleBase64 != nil {
		bundleBase64 = *request.Body.BundleBase64
	}
	credential := ""
	if request.Body.Credential != nil {
		credential = *request.Body.Credential
	}
	credentials := map[string]string(nil)
	if request.Body.Credentials != nil {
		credentials = *request.Body.Credentials
	}
	capability, err := s.service.CreateCapability(ctx, request.ProjectId.String(), core.CreateCapabilityInput{Kind: string(request.Body.Kind), Name: request.Body.Name, Version: version, ManifestJSON: request.Body.ManifestJson, ScopeType: scopeType, SourceFilename: sourceFilename, BundleBase64: bundleBase64, Credential: credential, Credentials: credentials}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := capabilityResponse(capability)
	if err != nil {
		return nil, err
	}
	return generated.CreateCapability201JSONResponse(response), nil
}

func (s *Server) ResolveResourceInstallDraft(ctx context.Context, request generated.ResolveResourceInstallDraftRequestObject) (generated.ResolveResourceInstallDraftResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	draft, err := s.service.ResolveMCPInstallDraft(ctx, request.ProjectId.String(), request.Body.SourceInput)
	if err != nil {
		return nil, err
	}
	response, err := resourceInstallDraftResponse(draft)
	if err != nil {
		return nil, err
	}
	return generated.ResolveResourceInstallDraft200JSONResponse(response), nil
}

func (s *Server) SetCapabilityStatus(ctx context.Context, request generated.SetCapabilityStatusRequestObject) (generated.SetCapabilityStatusResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	capability, err := s.service.SetCapabilityStatus(ctx, request.CapabilityId.String(), string(request.Body.Status), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := capabilityResponse(capability)
	if err != nil {
		return nil, err
	}
	return generated.SetCapabilityStatus200JSONResponse(response), nil
}

func (s *Server) DeleteCapability(ctx context.Context, request generated.DeleteCapabilityRequestObject) (generated.DeleteCapabilityResponseObject, error) {
	if err := s.service.DeleteCapability(ctx, request.CapabilityId.String(), correlationID(ctx)); err != nil {
		return nil, err
	}
	return generated.DeleteCapability204Response{}, nil
}

func (s *Server) ReviewCapability(ctx context.Context, request generated.ReviewCapabilityRequestObject) (generated.ReviewCapabilityResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	note := ""
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	capability, err := s.service.ReviewCapability(ctx, request.CapabilityId.String(), core.ReviewCapabilityInput{Decision: string(request.Body.Decision), Note: note}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := capabilityResponse(capability)
	if err != nil {
		return nil, err
	}
	return generated.ReviewCapability200JSONResponse(response), nil
}

func (s *Server) ConnectMCP(ctx context.Context, request generated.ConnectMCPRequestObject) (generated.ConnectMCPResponseObject, error) {
	health, err := s.service.ConnectMCP(ctx, request.CapabilityId.String(), correlationID(ctx))
	if err != nil {
		return nil, err
	}
	response, err := mcpHealthResponse(health)
	if err != nil {
		return nil, err
	}
	return generated.ConnectMCP200JSONResponse(response), nil
}

func (s *Server) StartMCPOAuth(ctx context.Context, request generated.StartMCPOAuthRequestObject) (generated.StartMCPOAuthResponseObject, error) {
	session, err := s.service.StartMCPOAuth(ctx, request.CapabilityId.String())
	if err != nil {
		return nil, err
	}
	response, err := mcpOAuthSessionResponse(session)
	if err != nil {
		return nil, err
	}
	return generated.StartMCPOAuth200JSONResponse(response), nil
}

func (s *Server) GetMCPOAuthStatus(ctx context.Context, request generated.GetMCPOAuthStatusRequestObject) (generated.GetMCPOAuthStatusResponseObject, error) {
	session, err := s.service.GetMCPOAuthStatus(ctx, request.CapabilityId.String(), request.SessionId.String())
	if err != nil {
		return nil, err
	}
	response, err := mcpOAuthSessionResponse(session)
	if err != nil {
		return nil, err
	}
	return generated.GetMCPOAuthStatus200JSONResponse(response), nil
}

func (s *Server) CancelMCPOAuth(ctx context.Context, request generated.CancelMCPOAuthRequestObject) (generated.CancelMCPOAuthResponseObject, error) {
	session, err := s.service.CancelMCPOAuth(ctx, request.CapabilityId.String(), request.SessionId.String())
	if err != nil {
		return nil, err
	}
	response, err := mcpOAuthSessionResponse(session)
	if err != nil {
		return nil, err
	}
	return generated.CancelMCPOAuth200JSONResponse(response), nil
}

func (s *Server) CompleteMCPOAuth(ctx context.Context, request generated.CompleteMCPOAuthRequestObject) (generated.CompleteMCPOAuthResponseObject, error) {
	status := core.MCPOAuthStatusCanceled
	if request.Params.Error != nil {
		session, err := s.service.CancelMCPOAuthByState(ctx, request.Params.State)
		if err != nil {
			return nil, err
		}
		status = session.Status
	} else {
		code := ""
		issuer := ""
		if request.Params.Code != nil {
			code = *request.Params.Code
		}
		if request.Params.Iss != nil {
			issuer = *request.Params.Iss
		}
		session, err := s.service.CompleteMCPOAuth(ctx, request.Params.State, code, issuer)
		if err != nil {
			return nil, err
		}
		status = session.Status
	}
	message := "授权已取消。你可以关闭此窗口，MCP 配置和项目文件没有被删除。"
	if status == core.MCPOAuthStatusConnected {
		message = "账号连接成功。你可以关闭此窗口并返回 Oktapod。"
	} else if status == core.MCPOAuthStatusFailed {
		message = "账号授权已返回，但 MCP 验证失败。请关闭此窗口并在资源中心查看修复提示。"
	}
	body := "<!doctype html><html lang=\"zh-CN\"><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><meta name=\"color-scheme\" content=\"light dark\"><title>Oktapod MCP 授权</title><style>:root{font-family:Inter,\"Segoe UI\",sans-serif;color:#172033;background:#f5f7fb}body{display:grid;min-height:100vh;margin:0;place-items:center}main{max-width:430px;margin:24px;padding:28px;border:1px solid #d8deea;border-top:4px solid #2455e6;border-radius:10px;background:#fff;box-shadow:0 18px 50px #07145418}h1{margin:0 0 12px;font-size:21px}p{margin:0;color:#5d6677;font-size:14px;line-height:1.7}@media(prefers-color-scheme:dark){:root{color:#edf2ff;background:#111722}main{border-color:#313b4c;background:#171e2a;box-shadow:none}p{color:#b7c1d4}}</style><body><main><h1>Oktapod MCP 授权</h1><p>" + message + "</p></main></body></html>"
	return mcpOAuthHTMLResponse(body), nil
}

func (s *Server) DisconnectMCP(ctx context.Context, request generated.DisconnectMCPRequestObject) (generated.DisconnectMCPResponseObject, error) {
	health, err := s.service.DisconnectMCP(ctx, request.CapabilityId.String(), correlationID(ctx))
	if err != nil {
		return nil, err
	}
	response, err := mcpHealthResponse(health)
	if err != nil {
		return nil, err
	}
	return generated.DisconnectMCP200JSONResponse(response), nil
}

func (s *Server) GetMCPHealth(ctx context.Context, request generated.GetMCPHealthRequestObject) (generated.GetMCPHealthResponseObject, error) {
	health, err := s.service.GetMCPHealth(ctx, request.CapabilityId.String())
	if err != nil {
		return nil, err
	}
	response, err := mcpHealthResponse(health)
	if err != nil {
		return nil, err
	}
	return generated.GetMCPHealth200JSONResponse(response), nil
}

func mcpOAuthSessionResponse(session core.MCPOAuthSession) (generated.MCPOAuthSession, error) {
	id, err := uuid.Parse(session.ID)
	if err != nil {
		return generated.MCPOAuthSession{}, err
	}
	capabilityID, err := uuid.Parse(session.CapabilityID)
	if err != nil {
		return generated.MCPOAuthSession{}, err
	}
	response := generated.MCPOAuthSession{
		Id: id, CapabilityId: capabilityID, Status: generated.MCPOAuthSessionStatus(session.Status),
		ExpiresAt: session.ExpiresAt, UpdatedAt: session.UpdatedAt,
	}
	if session.AuthorizationURL != "" {
		response.AuthorizationUrl = &session.AuthorizationURL
	}
	if session.LastError != "" {
		response.LastError = &session.LastError
	}
	return response, nil
}

func (s *Server) ListProjectMemories(ctx context.Context, request generated.ListProjectMemoriesRequestObject) (generated.ListProjectMemoriesResponseObject, error) {
	query := ""
	if request.Params.Q != nil {
		query = *request.Params.Q
	}
	memories, err := s.service.ListMemories(ctx, request.ProjectId.String(), query)
	if err != nil {
		return nil, err
	}
	items := make([]generated.Memory, 0, len(memories))
	for _, memory := range memories {
		item, convertErr := memoryResponse(memory)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectMemories200JSONResponse{Items: items}, nil
}

func (s *Server) CreateMemory(ctx context.Context, request generated.CreateMemoryRequestObject) (generated.CreateMemoryResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	sourceRefsJSON, err := marshalMemorySourceRefs(request.Body.SourceRefs)
	if err != nil {
		return nil, err
	}
	memory, err := s.service.CreateMemory(ctx, request.ProjectId.String(), core.CreateMemoryInput{Kind: request.Body.Kind, Title: request.Body.Title, Content: request.Body.Content, Visibility: string(request.Body.Visibility), SourceRefsJSON: sourceRefsJSON}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := memoryResponse(memory)
	if err != nil {
		return nil, err
	}
	return generated.CreateMemory201JSONResponse(response), nil
}

func (s *Server) UpdateMemory(ctx context.Context, request generated.UpdateMemoryRequestObject) (generated.UpdateMemoryResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	sourceRefsJSON, err := marshalMemorySourceRefs(&request.Body.SourceRefs)
	if err != nil {
		return nil, err
	}
	memory, err := s.service.UpdateMemory(ctx, request.MemoryId.String(), core.UpdateMemoryInput{Title: request.Body.Title, Content: request.Body.Content, Visibility: string(request.Body.Visibility), SourceRefsJSON: sourceRefsJSON}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := memoryResponse(memory)
	if err != nil {
		return nil, err
	}
	return generated.UpdateMemory200JSONResponse(response), nil
}

func (s *Server) SetMemoryStatus(ctx context.Context, request generated.SetMemoryStatusRequestObject) (generated.SetMemoryStatusResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	memory, err := s.service.SetMemoryStatus(ctx, request.MemoryId.String(), string(request.Body.Status), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := memoryResponse(memory)
	if err != nil {
		return nil, err
	}
	return generated.SetMemoryStatus200JSONResponse(response), nil
}

func (s *Server) DeleteMemory(ctx context.Context, request generated.DeleteMemoryRequestObject) (generated.DeleteMemoryResponseObject, error) {
	if err := s.service.DeleteMemory(ctx, request.MemoryId.String(), correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey)); err != nil {
		return nil, err
	}
	return generated.DeleteMemory204Response{}, nil
}

func (s *Server) CreateDailySummary(ctx context.Context, request generated.CreateDailySummaryRequestObject) (generated.CreateDailySummaryResponseObject, error) {
	memory, err := s.service.CreateDailySummary(ctx, request.ProjectId.String(), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := memoryResponse(memory)
	if err != nil {
		return nil, err
	}
	return generated.CreateDailySummary201JSONResponse(response), nil
}

func (s *Server) PromoteMemoryToSkill(ctx context.Context, request generated.PromoteMemoryToSkillRequestObject) (generated.PromoteMemoryToSkillResponseObject, error) {
	capability, err := s.service.PromoteMemoryToSkill(ctx, request.MemoryId.String(), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := capabilityResponse(capability)
	if err != nil {
		return nil, err
	}
	return generated.PromoteMemoryToSkill201JSONResponse(response), nil
}

func (s *Server) ListProjectAgents(ctx context.Context, request generated.ListProjectAgentsRequestObject) (generated.ListProjectAgentsResponseObject, error) {
	agents, err := s.service.ListAgentDefinitions(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.AgentDefinition, 0, len(agents))
	for _, agent := range agents {
		item, convertErr := agentDefinitionResponse(agent)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectAgents200JSONResponse{Items: items}, nil
}

func (s *Server) CreateAgentDefinition(ctx context.Context, request generated.CreateAgentDefinitionRequestObject) (generated.CreateAgentDefinitionResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	input := core.CreateAgentInput{Name: request.Body.Name, Version: request.Body.Version, Instructions: request.Body.Instructions}
	if request.Body.Model != nil {
		input.Model = *request.Body.Model
	}
	if request.Body.FallbackModel != nil {
		input.FallbackModel = *request.Body.FallbackModel
	}
	if request.Body.ConfigJson != nil {
		input.ConfigJSON = *request.Body.ConfigJson
	}
	agent, err := s.service.CreateAgentDefinition(ctx, request.ProjectId.String(), input, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := agentDefinitionResponse(agent)
	if err != nil {
		return nil, err
	}
	return generated.CreateAgentDefinition201JSONResponse(response), nil
}

func (s *Server) SetAgentDefinitionStatus(ctx context.Context, request generated.SetAgentDefinitionStatusRequestObject) (generated.SetAgentDefinitionStatusResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	agent, err := s.service.SetAgentDefinitionStatus(ctx, request.AgentId.String(), string(request.Body.Status), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := agentDefinitionResponse(agent)
	if err != nil {
		return nil, err
	}
	return generated.SetAgentDefinitionStatus200JSONResponse(response), nil
}

func (s *Server) DeleteAgentDefinition(ctx context.Context, request generated.DeleteAgentDefinitionRequestObject) (generated.DeleteAgentDefinitionResponseObject, error) {
	if err := s.service.DeleteAgentDefinition(ctx, request.AgentId.String(), correlationID(ctx)); err != nil {
		return nil, err
	}
	return generated.DeleteAgentDefinition204Response{}, nil
}

func (s *Server) ListProjectTeamDefinitions(ctx context.Context, request generated.ListProjectTeamDefinitionsRequestObject) (generated.ListProjectTeamDefinitionsResponseObject, error) {
	teams, err := s.service.ListTeamDefinitions(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.TeamDefinition, 0, len(teams))
	for _, team := range teams {
		item, convertErr := teamDefinitionResponse(team)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectTeamDefinitions200JSONResponse{Items: items}, nil
}

func (s *Server) CreateTeamDefinition(ctx context.Context, request generated.CreateTeamDefinitionRequestObject) (generated.CreateTeamDefinitionResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	roles := make([]core.TeamRole, 0, len(request.Body.Roles))
	for _, role := range request.Body.Roles {
		roles = append(roles, core.TeamRole{Role: role.Role, Name: role.Name})
	}
	config := "{}"
	if request.Body.ConfigJson != nil {
		config = *request.Body.ConfigJson
	}
	team, err := s.service.CreateTeamDefinition(ctx, request.ProjectId.String(), core.CreateTeamDefinitionInput{Name: request.Body.Name, Version: request.Body.Version, Roles: roles, ConfigJSON: config}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := teamDefinitionResponse(team)
	if err != nil {
		return nil, err
	}
	return generated.CreateTeamDefinition201JSONResponse(response), nil
}

func (s *Server) SetTeamDefinitionStatus(ctx context.Context, request generated.SetTeamDefinitionStatusRequestObject) (generated.SetTeamDefinitionStatusResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	team, err := s.service.SetTeamDefinitionStatus(ctx, request.TeamId.String(), string(request.Body.Status), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := teamDefinitionResponse(team)
	if err != nil {
		return nil, err
	}
	return generated.SetTeamDefinitionStatus200JSONResponse(response), nil
}

func (s *Server) GetProjectUsage(ctx context.Context, request generated.GetProjectUsageRequestObject) (generated.GetProjectUsageResponseObject, error) {
	since := time.Time{}
	if request.Params.Since != nil {
		since = request.Params.Since.UTC()
	}
	usage, err := s.service.GetProjectUsage(ctx, request.ProjectId.String(), since)
	if err != nil {
		return nil, err
	}
	projectID, err := uuid.Parse(usage.ProjectID)
	if err != nil {
		return nil, err
	}
	return generated.GetProjectUsage200JSONResponse(generated.ProjectUsage{
		ProjectId: projectID, ModelCalls: usage.ModelCalls, InputTokens: usage.InputTokens,
		OutputTokens: usage.OutputTokens, CachedTokens: usage.CachedTokens, ReasoningTokens: usage.ReasoningTokens,
		EstimatedCalls: usage.EstimatedCalls, LatencyMs: usage.LatencyMS,
		CostStatus: generated.ProjectUsageCostStatus(usage.CostStatus), Currency: usage.Currency, CostDecimal: usage.CostDecimal,
		ByDay: usageBucketResponses(usage.ByDay), ByModel: usageBucketResponses(usage.ByModel),
		ByMember: usageBucketResponses(usage.ByMember), ByAgent: usageBucketResponses(usage.ByAgent),
	}), nil
}

func usageBucketResponses(items []core.UsageBucket) []generated.UsageBucket {
	responses := make([]generated.UsageBucket, 0, len(items))
	for _, item := range items {
		responses = append(responses, generated.UsageBucket{Key: item.Key, Label: item.Label, ModelCalls: item.ModelCalls, InputTokens: item.InputTokens, OutputTokens: item.OutputTokens, CachedTokens: item.CachedTokens, ReasoningTokens: item.ReasoningTokens, EstimatedCalls: item.EstimatedCalls, LatencyMs: item.LatencyMS, CostDecimal: item.CostDecimal})
	}
	return responses
}

func (s *Server) PublishArtifact(ctx context.Context, request generated.PublishArtifactRequestObject) (generated.PublishArtifactResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	parentID := ""
	if request.Body.ParentId != nil {
		parentID = request.Body.ParentId.String()
	}
	asset, err := s.service.PublishArtifact(ctx, request.ArtifactId.String(), core.PublishArtifactInput{
		Name: request.Body.Name, ParentID: parentID, Visibility: string(request.Body.Visibility),
	}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := assetResponse(asset)
	if err != nil {
		return nil, err
	}
	return generated.PublishArtifact201JSONResponse(response), nil
}

func (s *Server) SaveArtifactAsProjectFile(ctx context.Context, request generated.SaveArtifactAsProjectFileRequestObject) (generated.SaveArtifactAsProjectFileResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	parentID := ""
	if request.Body.ParentId != nil {
		parentID = request.Body.ParentId.String()
	}
	asset, err := s.service.PublishArtifact(ctx, request.ArtifactId.String(), core.PublishArtifactInput{
		Name: request.Body.Name, ParentID: parentID, Visibility: string(request.Body.Visibility),
	}, optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := assetResponse(asset)
	if err != nil {
		return nil, err
	}
	return generated.SaveArtifactAsProjectFile201JSONResponse(response), nil
}

func (s *Server) CreateTask(ctx context.Context, request generated.CreateTaskRequestObject) (generated.CreateTaskResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	title := ""
	acceptance := ""
	if request.Body.Title != nil {
		title = *request.Body.Title
	}
	if request.Body.Acceptance != nil {
		acceptance = *request.Body.Acceptance
	}
	createInput := core.CreateTaskInput{
		Title:          title,
		Goal:           request.Body.Goal,
		Acceptance:     acceptance,
		Mode:           string(request.Body.Mode),
		Visibility:     string(request.Body.Visibility),
		PermissionMode: core.PermissionModeDefault,
	}
	if request.Body.PermissionMode != nil {
		createInput.PermissionMode = string(*request.Body.PermissionMode)
	}
	if request.Body.Scene != nil {
		createInput.SceneOverride = string(*request.Body.Scene)
	}
	if request.Body.Model != nil {
		createInput.Model = *request.Body.Model
	}
	if request.Body.FallbackModel != nil {
		createInput.FallbackModel = *request.Body.FallbackModel
	}
	if request.Body.ProviderId != nil {
		createInput.ProviderID = *request.Body.ProviderId
	}
	if request.Body.Thinking != nil {
		createInput.Thinking = string(*request.Body.Thinking)
	}
	if request.Body.MaxModelCalls != nil {
		createInput.MaxModelCalls = *request.Body.MaxModelCalls
	}
	if request.Body.MaxTokens != nil {
		createInput.MaxTokens = *request.Body.MaxTokens
	}
	if request.Body.AgentId != nil {
		createInput.AgentID = request.Body.AgentId.String()
	}
	if request.Body.TeamDefinitionId != nil {
		createInput.TeamDefinitionID = request.Body.TeamDefinitionId.String()
	}
	if request.Body.TeamRoles != nil {
		createInput.TeamRoles = make([]core.TeamRole, 0, len(*request.Body.TeamRoles))
		for _, role := range *request.Body.TeamRoles {
			createInput.TeamRoles = append(createInput.TeamRoles, core.TeamRole{Role: role.Role, Name: role.Name})
		}
	}
	if request.Body.ContextAssetIds != nil {
		createInput.ContextAssetIDs = make([]string, 0, len(*request.Body.ContextAssetIds))
		for _, id := range *request.Body.ContextAssetIds {
			createInput.ContextAssetIDs = append(createInput.ContextAssetIDs, id.String())
		}
	}
	if request.Body.ContextMemoryIds != nil {
		createInput.ContextMemoryIDs = make([]string, 0, len(*request.Body.ContextMemoryIds))
		for _, id := range *request.Body.ContextMemoryIds {
			createInput.ContextMemoryIDs = append(createInput.ContextMemoryIDs, id.String())
		}
	}
	if request.Body.SkillId != nil {
		createInput.SkillID = request.Body.SkillId.String()
	}
	if request.Body.SkillArgs != nil {
		createInput.SkillArgs = *request.Body.SkillArgs
	}
	task, err := s.service.CreateTask(ctx, request.ProjectId.String(), createInput, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := taskResponse(task)
	if err != nil {
		return nil, err
	}
	return generated.CreateTask202JSONResponse(response), nil
}

func (s *Server) GetTask(ctx context.Context, request generated.GetTaskRequestObject) (generated.GetTaskResponseObject, error) {
	task, err := s.service.GetTask(ctx, request.TaskId.String())
	if err != nil {
		return nil, err
	}
	response, err := taskResponse(task)
	if err != nil {
		return nil, err
	}
	return generated.GetTask200JSONResponse(response), nil
}

func (s *Server) ControlTask(ctx context.Context, request generated.ControlTaskRequestObject) (generated.ControlTaskResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	task, err := s.service.ControlTask(ctx, request.TaskId.String(), core.ControlTaskInput{Command: string(request.Body.Command)}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := taskResponse(task)
	if err != nil {
		return nil, err
	}
	return generated.ControlTask200JSONResponse(response), nil
}

func (s *Server) ForkTask(ctx context.Context, request generated.ForkTaskRequestObject) (generated.ForkTaskResponseObject, error) {
	task, err := s.service.ForkTask(ctx, request.TaskId.String(), correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := taskResponse(task)
	if err != nil {
		return nil, err
	}
	return generated.ForkTask201JSONResponse(response), nil
}

func (s *Server) AcceptTaskOutcome(ctx context.Context, request generated.AcceptTaskOutcomeRequestObject) (generated.AcceptTaskOutcomeResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	note := ""
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	outcome, err := s.service.AcceptTaskOutcome(ctx, request.TaskId.String(), note, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := acceptedOutcomeResponse(outcome)
	if err != nil {
		return nil, err
	}
	return generated.AcceptTaskOutcome201JSONResponse(response), nil
}

func (s *Server) ListRunEvents(ctx context.Context, request generated.ListRunEventsRequestObject) (generated.ListRunEventsResponseObject, error) {
	after := int64(0)
	if request.Params.After != nil {
		after = *request.Params.After
	}
	events, err := s.service.ListRunEvents(ctx, request.RunId.String(), after)
	if err != nil {
		return nil, err
	}
	items, next, err := eventResponses(events, after)
	if err != nil {
		return nil, err
	}
	return generated.ListRunEvents200JSONResponse{Items: items, NextCursor: next}, nil
}

func (s *Server) GetAgentTeamRoom(ctx context.Context, request generated.GetAgentTeamRoomRequestObject) (generated.GetAgentTeamRoomResponseObject, error) {
	room, err := s.service.GetAgentTeamRoom(ctx, request.RunId.String())
	if err != nil {
		return nil, err
	}
	runID, err := uuid.Parse(room.RunID)
	if err != nil {
		return nil, err
	}
	members := make([]generated.AgentTeamMember, 0, len(room.Members))
	for _, member := range room.Members {
		memberID, parseErr := uuid.Parse(member.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		memberRunID, parseErr := uuid.Parse(member.RunID)
		if parseErr != nil {
			return nil, parseErr
		}
		members = append(members, generated.AgentTeamMember{
			Id: memberID, RunId: memberRunID, Role: generated.AgentTeamMemberRole(member.Role),
			DisplayName: member.DisplayName, Status: generated.AgentTeamMemberStatus(member.Status),
			CurrentStepId: member.CurrentStepID, Model: member.Model, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt,
		})
	}
	messages := make([]generated.AgentTeamMessage, 0, len(room.Messages))
	for _, message := range room.Messages {
		messageID, parseErr := uuid.Parse(message.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		messageRunID, parseErr := uuid.Parse(message.RunID)
		if parseErr != nil {
			return nil, parseErr
		}
		senderID, parseErr := uuid.Parse(message.SenderAgentID)
		if parseErr != nil {
			return nil, parseErr
		}
		messages = append(messages, generated.AgentTeamMessage{
			Id: messageID, RunId: messageRunID, SenderAgentId: senderID, ServerSeq: message.ServerSeq,
			MessageType: generated.AgentTeamMessageMessageType(message.MessageType), Content: message.Content,
			ContentHash: message.ContentHash, CreatedAt: message.CreatedAt,
		})
	}
	return generated.GetAgentTeamRoom200JSONResponse(generated.AgentTeamRoom{
		RunId: runID, Members: members, Messages: messages, NextCursor: room.NextCursor,
	}), nil
}

func (s *Server) GetRunUsage(ctx context.Context, request generated.GetRunUsageRequestObject) (generated.GetRunUsageResponseObject, error) {
	usage, err := s.service.GetRunUsage(ctx, request.RunId.String())
	if err != nil {
		return nil, err
	}
	runID, err := uuid.Parse(usage.RunID)
	if err != nil {
		return nil, err
	}
	return generated.GetRunUsage200JSONResponse(generated.RunUsage{
		RunId:           runID,
		ModelCalls:      usage.ModelCalls,
		InputTokens:     usage.InputTokens,
		OutputTokens:    usage.OutputTokens,
		CachedTokens:    usage.CachedTokens,
		ReasoningTokens: usage.ReasoningTokens,
		EstimatedCalls:  usage.EstimatedCalls,
		LatencyMs:       usage.LatencyMS,
		CostStatus:      generated.RunUsageCostStatus(usage.CostStatus),
		Currency:        usage.Currency,
		CostDecimal:     usage.CostDecimal,
		BudgetMaxCalls:  usage.BudgetMaxCalls,
		BudgetMaxTokens: usage.BudgetMaxTokens,
		BudgetStatus:    generated.RunUsageBudgetStatus(usage.BudgetStatus),
		RemainingCalls:  usage.RemainingCalls,
		RemainingTokens: usage.RemainingTokens,
		ReservedCalls:   int64Ptr(usage.ReservedCalls),
		ReservedTokens:  int64Ptr(usage.ReservedTokens),
	}), nil
}

func (s *Server) UpdateRunBudget(ctx context.Context, request generated.UpdateRunBudgetRequestObject) (generated.UpdateRunBudgetResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	approve := false
	if request.Body.Approve != nil {
		approve = *request.Body.Approve
	}
	usage, err := s.service.UpdateRunBudget(ctx, request.RunId.String(), core.UpdateRunBudgetInput{MaxModelCalls: request.Body.MaxModelCalls, MaxTokens: request.Body.MaxTokens, Approve: approve}, correlationID(ctx))
	if err != nil {
		return nil, err
	}
	runID, err := uuid.Parse(usage.RunID)
	if err != nil {
		return nil, err
	}
	return generated.UpdateRunBudget200JSONResponse(generated.RunUsage{RunId: runID, ModelCalls: usage.ModelCalls, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CachedTokens: usage.CachedTokens, ReasoningTokens: usage.ReasoningTokens, EstimatedCalls: usage.EstimatedCalls, LatencyMs: usage.LatencyMS, CostStatus: generated.RunUsageCostStatus(usage.CostStatus), Currency: usage.Currency, CostDecimal: usage.CostDecimal, BudgetMaxCalls: usage.BudgetMaxCalls, BudgetMaxTokens: usage.BudgetMaxTokens, BudgetStatus: generated.RunUsageBudgetStatus(usage.BudgetStatus), RemainingCalls: usage.RemainingCalls, RemainingTokens: usage.RemainingTokens, ReservedCalls: int64Ptr(usage.ReservedCalls), ReservedTokens: int64Ptr(usage.ReservedTokens)}), nil
}

func (s *Server) CreateAdvisorReport(ctx context.Context, request generated.CreateAdvisorReportRequestObject) (generated.CreateAdvisorReportResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	approve := false
	if request.Body.Approve != nil {
		approve = *request.Body.Approve
	}
	report, err := s.service.CreateAdvisorReport(ctx, request.RunId.String(), core.CreateAdvisorInput{TriggerReason: request.Body.TriggerReason, Approve: approve}, correlationID(ctx))
	if err != nil {
		return nil, err
	}
	response, err := advisorReportResponse(report)
	if err != nil {
		return nil, err
	}
	return generated.CreateAdvisorReport201JSONResponse(response), nil
}

func advisorReportResponse(report core.AdvisorReport) (generated.AdvisorReport, error) {
	id, err := uuid.Parse(report.ID)
	if err != nil {
		return generated.AdvisorReport{}, err
	}
	organizationID, err := uuid.Parse(report.OrganizationID)
	if err != nil {
		return generated.AdvisorReport{}, err
	}
	projectID, err := uuid.Parse(report.ProjectID)
	if err != nil {
		return generated.AdvisorReport{}, err
	}
	taskID, err := uuid.Parse(report.TaskID)
	if err != nil {
		return generated.AdvisorReport{}, err
	}
	runID, err := uuid.Parse(report.RunID)
	if err != nil {
		return generated.AdvisorReport{}, err
	}
	requestedBy, err := uuid.Parse(report.RequestedBy)
	if err != nil {
		return generated.AdvisorReport{}, err
	}
	return generated.AdvisorReport{Id: id, OrganizationId: organizationID, ProjectId: projectID, TaskId: taskID, RunId: runID, RequestedBy: requestedBy, TriggerReason: report.TriggerReason, Status: generated.AdvisorReportStatus(report.Status), Summary: report.Summary, Evidence: report.Evidence, EstimatedModelCalls: report.EstimatedModelCalls, EstimatedTokens: report.EstimatedTokens, EstimatedCostDecimal: report.EstimatedCostDecimal, CreatedAt: report.CreatedAt}, nil
}

func (s *Server) ListRunArtifacts(ctx context.Context, request generated.ListRunArtifactsRequestObject) (generated.ListRunArtifactsResponseObject, error) {
	artifacts, err := s.service.ListRunArtifacts(ctx, request.RunId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		item, err := artifactResponse(artifact)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return generated.ListRunArtifacts200JSONResponse{Items: items}, nil
}

func (s *Server) ListWorkspaceFiles(ctx context.Context, request generated.ListWorkspaceFilesRequestObject) (generated.ListWorkspaceFilesResponseObject, error) {
	cursor, limit := 0, application.MaxWorkspaceFilePage
	if request.Params.Cursor != nil {
		cursor = *request.Params.Cursor
	}
	if request.Params.Limit != nil {
		limit = *request.Params.Limit
	}
	page, err := s.service.ListWorkspaceFiles(ctx, request.ProjectId.String(), cursor, limit)
	if err != nil {
		return nil, err
	}
	items := make([]generated.WorkspaceFile, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, generated.WorkspaceFile{Path: item.Path, Kind: generated.WorkspaceFileKind(item.Kind), SizeBytes: item.SizeBytes, ModifiedAt: item.ModifiedAt, GitStatus: item.GitStatus})
	}
	return generated.ListWorkspaceFiles200JSONResponse{Items: items, HasMore: page.HasMore, NextCursor: page.NextCursor}, nil
}

func (s *Server) DownloadWorkspaceFileContent(ctx context.Context, request generated.DownloadWorkspaceFileContentRequestObject) (generated.DownloadWorkspaceFileContentResponseObject, error) {
	content, err := s.service.OpenWorkspaceFile(ctx, request.ProjectId.String(), request.Params.Path)
	if err != nil {
		return nil, err
	}
	disposition := mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(content.Path)})
	return workspaceFileContentResponse{body: content.Body, path: content.Path, mediaType: content.MediaType, disposition: disposition, sizeBytes: content.SizeBytes}, nil
}

type workspaceFileContentResponse struct {
	body        io.ReadCloser
	path        string
	mediaType   string
	disposition string
	sizeBytes   int64
}

func (response workspaceFileContentResponse) VisitDownloadWorkspaceFileContentResponse(w http.ResponseWriter) error {
	defer response.body.Close()
	mediaType := strings.ToLower(strings.TrimSpace(response.mediaType))
	if strings.HasSuffix(strings.ToLower(response.path), ".md") || mediaType == "text/markdown" {
		mediaType = "text/plain; charset=utf-8"
	} else if strings.HasPrefix(mediaType, "text/") && !strings.Contains(mediaType, "charset=") {
		mediaType += "; charset=utf-8"
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", response.disposition)
	w.Header().Set("Content-Length", strconv.FormatInt(response.sizeBytes, 10))
	w.Header().Set("X-Workspace-Path", response.path)
	w.WriteHeader(http.StatusOK)
	_, err := io.Copy(w, response.body)
	return err
}

func (s *Server) ListProjectArtifacts(ctx context.Context, request generated.ListProjectArtifactsRequestObject) (generated.ListProjectArtifactsResponseObject, error) {
	query := application.ProjectArtifactQuery{}
	if request.Params.RunId != nil {
		query.RunID = request.Params.RunId.String()
	}
	if request.Params.ConversationId != nil {
		query.ConversationID = request.Params.ConversationId.String()
	}
	if request.Params.Kind != nil {
		query.Kind = *request.Params.Kind
	}
	if request.Params.Q != nil {
		query.Search = *request.Params.Q
	}
	if request.Params.Limit != nil {
		query.Limit = *request.Params.Limit
	}
	if request.Params.IncludeHistory != nil {
		query.IncludeHistory = *request.Params.IncludeHistory
	}
	artifacts, err := s.service.ListProjectArtifacts(ctx, request.ProjectId.String(), query)
	if err != nil {
		return nil, err
	}
	items := make([]generated.Artifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		item, convertErr := artifactResponse(artifact)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListProjectArtifacts200JSONResponse{Items: items}, nil
}

func (s *Server) CaptureWorkspaceArtifact(ctx context.Context, request generated.CaptureWorkspaceArtifactRequestObject) (generated.CaptureWorkspaceArtifactResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	artifact, err := s.service.CaptureWorkspaceArtifact(ctx, request.ProjectId.String(), application.CaptureWorkspaceArtifactInput{
		RunID: request.Body.RunId.String(), Path: request.Body.Path, Kind: string(request.Body.Kind),
	}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := artifactResponse(artifact)
	if err != nil {
		return nil, err
	}
	return generated.CaptureWorkspaceArtifact201JSONResponse(response), nil
}

func (s *Server) GetRunChanges(ctx context.Context, request generated.GetRunChangesRequestObject) (generated.GetRunChangesResponseObject, error) {
	baseline := "run_start"
	if request.Params.Baseline != nil {
		baseline = string(*request.Params.Baseline)
	}
	changes, err := s.service.GetRunChanges(ctx, request.RunId.String(), baseline)
	if err != nil {
		return nil, err
	}
	projectID, err := uuid.Parse(changes.ProjectID)
	if err != nil {
		return nil, err
	}
	runID, err := uuid.Parse(changes.RunID)
	if err != nil {
		return nil, err
	}
	return generated.GetRunChanges200JSONResponse{
		RunId:          runID,
		ProjectId:      projectID,
		Baseline:       generated.RunChangesBaseline(changes.Baseline),
		BaselineHead:   changes.BaselineHead,
		CurrentHead:    changes.CurrentHead,
		DirtyAtStart:   changes.DirtyAtStart,
		StatusHash:     changes.StatusHash,
		Diff:           changes.Diff,
		Truncated:      changes.Truncated,
		BaselineNotice: changes.BaselineNotice,
	}, nil
}

func (s *Server) ListApprovals(ctx context.Context, request generated.ListApprovalsRequestObject) (generated.ListApprovalsResponseObject, error) {
	status := core.ApprovalPending
	if request.Params.Status != nil {
		status = string(*request.Params.Status)
	}
	approvals, err := s.service.ListApprovals(ctx, status)
	if err != nil {
		return nil, err
	}
	items := make([]generated.Approval, 0, len(approvals))
	for _, approval := range approvals {
		item, convertErr := approvalResponse(approval)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListApprovals200JSONResponse{Items: items}, nil
}

func (s *Server) GetActiveRunPlan(ctx context.Context, request generated.GetActiveRunPlanRequestObject) (generated.GetActiveRunPlanResponseObject, error) {
	plan, err := s.service.GetActiveRunPlan(ctx, request.RunId.String())
	if err != nil {
		return nil, err
	}
	response, err := runPlanRevisionResponse(plan)
	if err != nil {
		return nil, err
	}
	return generated.GetActiveRunPlan200JSONResponse(response), nil
}

func (s *Server) ListHumanActions(ctx context.Context, request generated.ListHumanActionsRequestObject) (generated.ListHumanActionsResponseObject, error) {
	status, conversationID, runID := core.HumanActionWaitingUser, "", ""
	if request.Params.Status != nil {
		status = string(*request.Params.Status)
	}
	if request.Params.ConversationId != nil {
		conversationID = request.Params.ConversationId.String()
	}
	if request.Params.RunId != nil {
		runID = request.Params.RunId.String()
	}
	actions, err := s.service.ListHumanActions(ctx, status, conversationID, runID)
	if err != nil {
		return nil, err
	}
	items := make([]generated.HumanAction, 0, len(actions))
	for _, action := range actions {
		item, convertErr := humanActionResponse(action)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListHumanActions200JSONResponse{Items: items}, nil
}

func (s *Server) DecideHumanAction(ctx context.Context, request generated.DecideHumanActionRequestObject) (generated.DecideHumanActionResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	scope, note, confirmed, answer := core.ApprovalScopeOnce, "", false, any(nil)
	if request.Body.Scope != nil {
		scope = string(*request.Body.Scope)
	}
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	if request.Body.Confirmed != nil {
		confirmed = *request.Body.Confirmed
	}
	if request.Body.Answer != nil {
		answer = *request.Body.Answer
	}
	action, err := s.service.DecideHumanAction(ctx, request.HumanActionId.String(), core.DecideHumanActionInput{
		Decision: string(request.Body.Decision), Scope: scope, Answer: answer,
		Note: note, Confirmed: confirmed, Version: request.Body.Version,
	}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := humanActionResponse(action)
	if err != nil {
		return nil, err
	}
	return generated.DecideHumanAction200JSONResponse(response), nil
}

func (s *Server) ListSideEffectReceipts(ctx context.Context, request generated.ListSideEffectReceiptsRequestObject) (generated.ListSideEffectReceiptsResponseObject, error) {
	status := core.SideEffectReceiptUnknown
	if request.Params.Status != nil {
		status = string(*request.Params.Status)
	}
	receipts, err := s.service.ListSideEffectReceipts(ctx, status)
	if err != nil {
		return nil, err
	}
	items := make([]generated.SideEffectReceipt, 0, len(receipts))
	for _, receipt := range receipts {
		item, convertErr := sideEffectReceiptResponse(receipt)
		if convertErr != nil {
			return nil, convertErr
		}
		items = append(items, item)
	}
	return generated.ListSideEffectReceipts200JSONResponse{Items: items}, nil
}

func (s *Server) ResolveSideEffect(ctx context.Context, request generated.ResolveSideEffectRequestObject) (generated.ResolveSideEffectResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	note := ""
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	receipt, err := s.service.ResolveSideEffect(ctx, request.ReceiptId.String(), core.ResolveSideEffectInput{Decision: string(request.Body.Decision), Note: note}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := sideEffectReceiptResponse(receipt)
	if err != nil {
		return nil, err
	}
	return generated.ResolveSideEffect200JSONResponse(response), nil
}

func (s *Server) ListInbox(ctx context.Context, request generated.ListInboxRequestObject) (generated.ListInboxResponseObject, error) {
	items, err := s.service.ListInbox(ctx)
	if err != nil {
		return nil, err
	}
	response := make([]generated.InboxItem, 0, len(items))
	for _, item := range items {
		id, parseErr := uuid.Parse(item.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		projectID, parseErr := uuid.Parse(item.ProjectID)
		if parseErr != nil {
			return nil, parseErr
		}
		taskID, parseErr := uuid.Parse(item.TaskID)
		if parseErr != nil {
			return nil, parseErr
		}
		response = append(response, generated.InboxItem{
			Id:        id,
			Kind:      generated.InboxItemKind(item.Kind),
			ProjectId: projectID,
			TaskId:    taskID,
			Title:     item.Title,
			Reason:    item.Reason,
			Status:    item.Status,
			UpdatedAt: item.UpdatedAt,
		})
	}
	history, err := s.service.ListInboxHistory(ctx, 50)
	if err != nil {
		return nil, err
	}
	historyResponse := make([]generated.InboxHistoryItem, 0, len(history))
	for _, item := range history {
		id, parseErr := uuid.Parse(item.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		projectID, parseErr := uuid.Parse(item.ProjectID)
		if parseErr != nil {
			return nil, parseErr
		}
		taskID, parseErr := uuid.Parse(item.TaskID)
		if parseErr != nil {
			return nil, parseErr
		}
		historyResponse = append(historyResponse, generated.InboxHistoryItem{Id: id, Kind: generated.InboxHistoryItemKind(item.Kind), ProjectId: projectID, TaskId: taskID, Title: item.Title, Summary: item.Summary, Status: item.Status, OccurredAt: item.OccurredAt})
	}
	return generated.ListInbox200JSONResponse{Items: response, History: &historyResponse}, nil
}

func sideEffectReceiptResponse(receipt core.SideEffectReceipt) (generated.SideEffectReceipt, error) {
	id, err := uuid.Parse(receipt.ID)
	if err != nil {
		return generated.SideEffectReceipt{}, err
	}
	projectID, err := uuid.Parse(receipt.ProjectID)
	if err != nil {
		return generated.SideEffectReceipt{}, err
	}
	taskID, err := uuid.Parse(receipt.TaskID)
	if err != nil {
		return generated.SideEffectReceipt{}, err
	}
	runID, err := uuid.Parse(receipt.RunID)
	if err != nil {
		return generated.SideEffectReceipt{}, err
	}
	stepID, err := uuid.Parse(receipt.StepID)
	if err != nil {
		return generated.SideEffectReceipt{}, err
	}
	toolCallID, err := uuid.Parse(receipt.ToolCallID)
	if err != nil {
		return generated.SideEffectReceipt{}, err
	}
	return generated.SideEffectReceipt{Id: id, ProjectId: projectID, TaskId: taskID, RunId: runID, StepId: stepID, ToolCallId: toolCallID, ToolName: receipt.ToolName, InputHash: receipt.InputHash, Idempotent: receipt.Idempotent, Status: generated.SideEffectReceiptStatus(receipt.Status), ErrorCode: receipt.ErrorCode, StartedAt: receipt.StartedAt, FinishedAt: receipt.FinishedAt}, nil
}

func (s *Server) DecideApproval(ctx context.Context, request generated.DecideApprovalRequestObject) (generated.DecideApprovalResponseObject, error) {
	if request.Body == nil {
		return nil, &core.ValidationError{Field: "body", Message: "is required"}
	}
	note := ""
	if request.Body.Note != nil {
		note = *request.Body.Note
	}
	scope := core.ApprovalScopeOnce
	if request.Body.Scope != nil {
		scope = string(*request.Body.Scope)
	}
	confirmed := request.Body.Confirmed != nil && *request.Body.Confirmed
	approval, err := s.service.DecideApproval(ctx, request.ApprovalId.String(), core.DecideApprovalInput{Decision: string(request.Body.Decision), Scope: scope, Note: note, Confirmed: confirmed}, correlationID(ctx), optionalIdempotencyKey(request.Params.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	response, err := approvalResponse(approval)
	if err != nil {
		return nil, err
	}
	return generated.DecideApproval200JSONResponse(response), nil
}

func (s *Server) ListProjectApprovalRules(ctx context.Context, request generated.ListProjectApprovalRulesRequestObject) (generated.ListProjectApprovalRulesResponseObject, error) {
	rules, err := s.service.ListProjectApprovalRules(ctx, request.ProjectId.String())
	if err != nil {
		return nil, err
	}
	items := make([]generated.ApprovalRule, 0, len(rules))
	for _, rule := range rules {
		id, parseErr := uuid.Parse(rule.ID)
		if parseErr != nil {
			return nil, parseErr
		}
		projectID, parseErr := uuid.Parse(rule.ProjectID)
		if parseErr != nil {
			return nil, parseErr
		}
		approvalID, parseErr := uuid.Parse(rule.CreatedFromApprovalID)
		if parseErr != nil {
			return nil, parseErr
		}
		items = append(items, generated.ApprovalRule{
			Id: id, ProjectId: projectID, ToolName: rule.ToolName, RuleKey: rule.RuleKey,
			CreatedFromApprovalId: approvalID, CreatedBy: rule.CreatedBy, CreatedAt: rule.CreatedAt,
			ExpiresAt: rule.ExpiresAt, RevokedAt: rule.RevokedAt, RevokedBy: rule.RevokedBy,
		})
	}
	return generated.ListProjectApprovalRules200JSONResponse{Items: items}, nil
}

func (s *Server) RevokeProjectApprovalRule(ctx context.Context, request generated.RevokeProjectApprovalRuleRequestObject) (generated.RevokeProjectApprovalRuleResponseObject, error) {
	if err := s.service.RevokeProjectApprovalRule(ctx, request.ProjectId.String(), request.RuleId.String(), correlationID(ctx)); err != nil {
		return nil, err
	}
	return generated.RevokeProjectApprovalRule204Response{}, nil
}

func (s *Server) streamRunEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeProblem(w, r, http.StatusNotImplemented, "STREAMING_UNAVAILABLE", "Streaming unavailable", "the HTTP server does not support response flushing")
		return
	}
	runID := chi.URLParam(r, "run_id")
	if _, err := uuid.Parse(runID); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_RUN_ID", "Invalid run ID", "run_id must be a UUID")
		return
	}
	after := int64(0)
	if header := strings.TrimSpace(r.Header.Get("Last-Event-ID")); header != "" {
		if parsed, err := strconv.ParseInt(header, 10, 64); err == nil && parsed >= 0 {
			after = parsed
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 0 {
			after = parsed
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	s.activeSSE.Add(1)
	defer s.activeSSE.Add(-1)

	poll := time.NewTicker(500 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-poll.C:
			events, err := s.service.ListRunEvents(r.Context(), runID, after)
			if err != nil {
				data, _ := json.Marshal(map[string]any{"code": "EVENT_STREAM_ERROR", "correlation_id": correlationID(r.Context())})
				_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
				flusher.Flush()
				return
			}
			for _, event := range events {
				response, responseErr := eventResponse(event)
				if responseErr != nil {
					data, _ := json.Marshal(map[string]any{"code": "EVENT_SERIALIZATION_ERROR", "correlation_id": correlationID(r.Context())})
					_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
					flusher.Flush()
					return
				}
				data, _ := json.Marshal(response)
				_, _ = fmt.Fprintf(w, "id: %d\nevent: run_event\ndata: %s\n\n", event.StreamSeq, data)
				after = event.StreamSeq
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}

func (s *Server) streamConversationMessages(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeProblem(w, r, http.StatusNotImplemented, "STREAMING_UNAVAILABLE", "Streaming unavailable", "the HTTP server does not support response flushing")
		return
	}
	conversationID := chi.URLParam(r, "conversation_id")
	if _, err := uuid.Parse(conversationID); err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "INVALID_CONVERSATION_ID", "Invalid conversation ID", "conversation_id must be a UUID")
		return
	}
	after := int64(0)
	if header := strings.TrimSpace(r.Header.Get("Last-Event-ID")); header != "" {
		if parsed, err := strconv.ParseInt(header, 10, 64); err == nil && parsed >= 0 {
			after = parsed
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed >= 0 {
			after = parsed
		}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	s.activeSSE.Add(1)
	defer s.activeSSE.Add(-1)

	poll := time.NewTicker(500 * time.Millisecond)
	heartbeat := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-poll.C:
			messages, err := s.service.ListMessages(r.Context(), conversationID, after)
			if err != nil {
				data, _ := json.Marshal(map[string]any{"code": "MESSAGE_STREAM_ERROR", "correlation_id": correlationID(r.Context())})
				_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
				flusher.Flush()
				return
			}
			for _, message := range messages {
				response, responseErr := messageResponse(message)
				if responseErr != nil {
					data, _ := json.Marshal(map[string]any{"code": "MESSAGE_SERIALIZATION_ERROR", "correlation_id": correlationID(r.Context())})
					_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
					flusher.Flush()
					return
				}
				data, _ := json.Marshal(response)
				_, _ = fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", message.ServerSeq, data)
				after = message.ServerSeq
			}
			if len(messages) > 0 {
				flusher.Flush()
			}
		}
	}
}

func projectResponse(project core.Project) (generated.Project, error) {
	id, err := uuid.Parse(project.ID)
	if err != nil {
		return generated.Project{}, err
	}
	orgID, err := uuid.Parse(project.OrganizationID)
	if err != nil {
		return generated.Project{}, err
	}
	description := project.Description
	return generated.Project{Id: id, OrganizationId: orgID, Name: project.Name, Description: &description, Kind: generated.ProjectKind(project.Kind), Scene: generated.ProjectScene(project.Scene), ModelProviderId: project.ModelProviderID, DefaultModel: project.DefaultModel, FallbackModel: project.FallbackModel, MaxModelCalls: project.MaxModelCalls, MaxTokens: project.MaxTokens, CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt}, nil
}

func projectMemberResponse(member core.ProjectMember) (generated.ProjectMember, error) {
	userID, err := uuid.Parse(member.UserID)
	if err != nil {
		return generated.ProjectMember{}, err
	}
	return generated.ProjectMember{UserId: userID, DisplayName: member.DisplayName, Email: member.Email, Role: generated.ProjectMemberRole(member.Role), Status: generated.ProjectMemberStatus(member.Status), CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt}, nil
}

func projectInvitationResponse(invitation core.ProjectInvitation) (generated.ProjectInvitation, error) {
	id, err := uuid.Parse(invitation.ID)
	if err != nil {
		return generated.ProjectInvitation{}, err
	}
	organizationID, err := uuid.Parse(invitation.OrganizationID)
	if err != nil {
		return generated.ProjectInvitation{}, err
	}
	projectID, err := uuid.Parse(invitation.ProjectID)
	if err != nil {
		return generated.ProjectInvitation{}, err
	}
	invitedBy, err := uuid.Parse(invitation.InvitedBy)
	if err != nil {
		return generated.ProjectInvitation{}, err
	}
	item := generated.ProjectInvitation{Id: id, OrganizationId: organizationID, ProjectId: projectID, InvitedBy: invitedBy, Email: openapi_types.Email(invitation.Email), DisplayName: invitation.DisplayName, Role: generated.ProjectInvitationRole(invitation.Role), Status: generated.ProjectInvitationStatus(invitation.Status), ExpiresAt: invitation.ExpiresAt, CreatedAt: invitation.CreatedAt, UpdatedAt: invitation.UpdatedAt}
	if invitation.AcceptedUserID != "" {
		acceptedUserID, parseErr := uuid.Parse(invitation.AcceptedUserID)
		if parseErr != nil {
			return generated.ProjectInvitation{}, parseErr
		}
		item.AcceptedUserId = &acceptedUserID
	}
	if invitation.InviteToken != "" {
		token := invitation.InviteToken
		item.InviteToken = &token
	}
	if invitation.SessionToken != "" {
		sessionToken := invitation.SessionToken
		item.SessionToken = &sessionToken
	}
	return item, nil
}

func taskResponse(task core.Task) (generated.Task, error) {
	id, err := uuid.Parse(task.ID)
	if err != nil {
		return generated.Task{}, err
	}
	projectID, err := uuid.Parse(task.ProjectID)
	if err != nil {
		return generated.Task{}, err
	}
	runID, err := uuid.Parse(task.RunID)
	if err != nil {
		return generated.Task{}, err
	}
	conversationID, err := uuid.Parse(task.ConversationID)
	if err != nil {
		return generated.Task{}, err
	}
	acceptance := task.Acceptance
	errorCode := task.ErrorCode
	resultSummary := task.ResultSummary
	return generated.Task{Id: id, ProjectId: projectID, RunId: runID, ConversationId: conversationID, Title: task.Title, Goal: task.Goal, Acceptance: &acceptance, Mode: generated.TaskMode(task.Mode), Visibility: generated.TaskVisibility(task.Visibility), Status: generated.TaskStatus(task.Status), ErrorCode: &errorCode, ResultSummary: &resultSummary, Accepted: task.AcceptedOutcomeID != "", CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}, nil
}

func scheduleResponse(schedule core.Schedule) (generated.Schedule, error) {
	id, err := uuid.Parse(schedule.ID)
	if err != nil {
		return generated.Schedule{}, err
	}
	organizationID, err := uuid.Parse(schedule.OrganizationID)
	if err != nil {
		return generated.Schedule{}, err
	}
	projectID, err := uuid.Parse(schedule.ProjectID)
	if err != nil {
		return generated.Schedule{}, err
	}
	ownerID, err := uuid.Parse(schedule.OwnerUserID)
	if err != nil {
		return generated.Schedule{}, err
	}
	return generated.Schedule{
		Id: id, OrganizationId: organizationID, ProjectId: projectID, OwnerUserId: ownerID,
		Name: schedule.Name, CronExpression: schedule.CronExpression, Timezone: schedule.Timezone,
		Goal: schedule.Goal, Acceptance: schedule.Acceptance, Mode: generated.ScheduleMode(schedule.Mode),
		Visibility: generated.ScheduleVisibility(schedule.Visibility), Status: generated.ScheduleStatus(schedule.Status),
		MisfirePolicy:       generated.ScheduleMisfirePolicy(schedule.MisfirePolicy),
		OverlapPolicy:       generated.ScheduleOverlapPolicy(schedule.OverlapPolicy),
		MisfireGraceSeconds: schedule.MisfireGraceSeconds,
		NextRunAt:           schedule.NextRunAt, CreatedAt: schedule.CreatedAt, UpdatedAt: schedule.UpdatedAt,
	}, nil
}

func capabilityResponse(capability core.Capability) (generated.Capability, error) {
	id, err := uuid.Parse(capability.ID)
	if err != nil {
		return generated.Capability{}, err
	}
	organizationID, err := uuid.Parse(capability.OrganizationID)
	if err != nil {
		return generated.Capability{}, err
	}
	projectID, err := uuid.Parse(capability.ProjectID)
	if err != nil {
		return generated.Capability{}, err
	}
	ownerID, err := uuid.Parse(capability.OwnerUserID)
	if err != nil {
		return generated.Capability{}, err
	}
	scopeType := capability.ScopeType
	if scopeType == "" {
		scopeType = core.CapabilityScopeProject
	}
	scopeIDValue := capability.ScopeID
	if scopeIDValue == "" {
		scopeIDValue = capability.ProjectID
	}
	scopeID, err := uuid.Parse(scopeIDValue)
	if err != nil {
		return generated.Capability{}, err
	}
	installJobIDValue := capability.InstallJobID
	if installJobIDValue == "" {
		installJobIDValue = capability.ID
	}
	installJobID, err := uuid.Parse(installJobIDValue)
	if err != nil {
		return generated.Capability{}, err
	}
	bundleHash := capability.BundleHash
	if bundleHash == "" {
		bundleHash = capability.ContentHash
	}
	availability := capability.Availability
	if availability == "" {
		availability = core.ResourceAvailabilityDisabled
		if capability.Status == core.CapabilityStatusQuarantined {
			availability = core.ResourceAvailabilityQuarantined
		} else if capability.Status == core.CapabilityStatusEnabled && capability.Kind != core.CapabilityKindMCP {
			availability = core.ResourceAvailabilityAvailable
		} else if capability.Status == core.CapabilityStatusEnabled {
			availability = core.ResourceAvailabilityDegraded
		}
	}
	return generated.Capability{Id: id, OrganizationId: organizationID, ProjectId: projectID, OwnerUserId: ownerID, Kind: generated.CapabilityKind(capability.Kind), Name: capability.Name, Version: capability.Version, ManifestJson: capability.ManifestJSON, ContentHash: capability.ContentHash, Status: generated.CapabilityStatus(capability.Status), Availability: generated.ResourceAvailability(availability), RiskLevel: capability.RiskLevel, PermissionDiff: core.CapabilityPermissionDiff(capability.ManifestJSON), ScopeType: generated.CapabilityScopeType(scopeType), ScopeId: scopeID, SourceFilename: capability.SourceFilename, BundleHash: bundleHash, InstallJobId: installJobID, CredentialConfigured: capability.CredentialExists, CreatedAt: capability.CreatedAt, UpdatedAt: capability.UpdatedAt}, nil
}

func resourceInstallDraftResponse(draft core.ResourceInstallDraft) (generated.ResourceInstallDraft, error) {
	id, err := uuid.Parse(draft.ID)
	if err != nil {
		return generated.ResourceInstallDraft{}, err
	}
	response := generated.ResourceInstallDraft{
		Id: id, Kind: generated.ResourceInstallDraftKind(draft.Kind), SourceInput: draft.SourceInput,
		Status: generated.ResourceInstallDraftStatus(draft.Status), Warnings: append([]string(nil), draft.Warnings...),
		RequiredSecrets: secretBindingResponses(draft.RequiredSecrets), CreatedAt: draft.CreatedAt, UpdatedAt: draft.UpdatedAt,
	}
	if draft.ResolvedName != "" {
		value := draft.ResolvedName
		response.ResolvedName = &value
	}
	if draft.ResolvedVersion != "" {
		value := draft.ResolvedVersion
		response.ResolvedVersion = &value
	}
	if draft.MCPDefinition != nil {
		definition, convertErr := mcpDefinitionResponse(*draft.MCPDefinition)
		if convertErr != nil {
			return generated.ResourceInstallDraft{}, convertErr
		}
		response.McpDefinition = &definition
	}
	if len(draft.Candidates) > 0 {
		candidates := make([]generated.MCPInstallCandidate, 0, len(draft.Candidates))
		for _, candidate := range draft.Candidates {
			definition, convertErr := mcpDefinitionResponse(candidate.Definition)
			if convertErr != nil {
				return generated.ResourceInstallDraft{}, convertErr
			}
			item := generated.MCPInstallCandidate{Name: candidate.Name, Version: candidate.Version, McpDefinition: definition}
			if candidate.Title != "" {
				value := candidate.Title
				item.Title = &value
			}
			if candidate.Description != "" {
				value := candidate.Description
				item.Description = &value
			}
			if candidate.RepositoryURL != "" {
				value := candidate.RepositoryURL
				item.RepositoryUrl = &value
			}
			candidates = append(candidates, item)
		}
		response.Candidates = &candidates
	}
	return response, nil
}

func mcpDefinitionResponse(definition core.MCPDefinitionV1) (generated.MCPDefinitionV1, error) {
	response := generated.MCPDefinitionV1{
		SchemaVersion: generated.MCPDefinitionV1SchemaVersion(definition.SchemaVersion),
	}
	response.Source.Type = generated.MCPDefinitionV1SourceType(definition.Source.Type)
	response.Auth.Type = generated.MCPDefinitionV1AuthType(definition.Auth.Type)
	if definition.Source.Reference != "" {
		value := definition.Source.Reference
		response.Source.Reference = &value
	}
	if definition.Source.Version != "" {
		value := definition.Source.Version
		response.Source.Version = &value
	}
	if definition.Source.Publisher != "" {
		value := definition.Source.Publisher
		response.Source.Publisher = &value
	}
	if definition.Auth.Header != "" {
		value := definition.Auth.Header
		response.Auth.Header = &value
	}
	if definition.Description != "" {
		value := definition.Description
		response.Description = &value
	}
	if len(definition.Headers) > 0 {
		value := make(map[string]string, len(definition.Headers))
		for key, item := range definition.Headers {
			value[key] = item
		}
		response.Headers = &value
	}
	if len(definition.Permissions) > 0 {
		value := append([]string(nil), definition.Permissions...)
		response.Permissions = &value
	}
	if len(definition.SecretBindings) > 0 {
		value := secretBindingResponses(definition.SecretBindings)
		response.SecretBindings = &value
	}
	switch definition.Transport.Type {
	case "stdio":
		transport := generated.MCPDefinitionV1Transport0{Type: generated.MCPDefinitionV1Transport0Type("stdio"), Command: definition.Transport.Command}
		if len(definition.Transport.Args) > 0 {
			args := append([]string(nil), definition.Transport.Args...)
			transport.Args = &args
		}
		if definition.Transport.AuthEnv != "" {
			value := definition.Transport.AuthEnv
			transport.AuthEnv = &value
		}
		if err := response.Transport.FromMCPDefinitionV1Transport0(transport); err != nil {
			return generated.MCPDefinitionV1{}, err
		}
	case "streamable_http":
		transport := generated.MCPDefinitionV1Transport1{Type: generated.MCPDefinitionV1Transport1Type("streamable_http"), Url: definition.Transport.URL}
		if err := response.Transport.FromMCPDefinitionV1Transport1(transport); err != nil {
			return generated.MCPDefinitionV1{}, err
		}
	default:
		return generated.MCPDefinitionV1{}, fmt.Errorf("unsupported MCP transport %q", definition.Transport.Type)
	}
	return response, nil
}

func secretBindingResponses(bindings []core.MCPSecretBinding) []generated.SecretBinding {
	result := make([]generated.SecretBinding, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, generated.SecretBinding{Name: binding.Name, Target: binding.Target, Required: binding.Required})
	}
	return result
}

func mcpHealthResponse(health core.MCPHealth) (generated.MCPHealth, error) {
	id, err := uuid.Parse(health.ID)
	if err != nil {
		return generated.MCPHealth{}, err
	}
	capabilityID, err := uuid.Parse(health.CapabilityID)
	if err != nil {
		return generated.MCPHealth{}, err
	}
	projectID, err := uuid.Parse(health.ProjectID)
	if err != nil {
		return generated.MCPHealth{}, err
	}
	var connectedAt *time.Time
	if !health.ConnectedAt.IsZero() {
		value := health.ConnectedAt
		connectedAt = &value
	}
	return generated.MCPHealth{
		Id: id, CapabilityId: capabilityID, ProjectId: projectID,
		Transport: generated.MCPHealthTransport(health.Transport), Status: generated.MCPHealthStatus(health.Status),
		Protocol: health.Protocol, ToolCount: health.ToolCount, ToolsJson: health.ToolsJSON,
		SchemaHash: health.SchemaHash, ConfigVersion: health.ConfigVersion,
		HealthState: generated.MCPHealthHealthState(health.HealthState),
		LastError:   health.LastError, ConnectedAt: connectedAt, UpdatedAt: health.UpdatedAt,
	}, nil
}

func memoryResponse(memory core.Memory) (generated.Memory, error) {
	id, err := uuid.Parse(memory.ID)
	if err != nil {
		return generated.Memory{}, err
	}
	organizationID, err := uuid.Parse(memory.OrganizationID)
	if err != nil {
		return generated.Memory{}, err
	}
	projectID, err := uuid.Parse(memory.ProjectID)
	if err != nil {
		return generated.Memory{}, err
	}
	ownerID, err := uuid.Parse(memory.OwnerUserID)
	if err != nil {
		return generated.Memory{}, err
	}
	var sourceRefs []string
	if err := json.Unmarshal([]byte(memory.SourceRefsJSON), &sourceRefs); err != nil {
		return generated.Memory{}, &core.ValidationError{Field: "source_refs", Message: "stored source references are invalid"}
	}
	return generated.Memory{Id: id, OrganizationId: organizationID, ProjectId: projectID, OwnerUserId: ownerID, Kind: memory.Kind, Title: memory.Title, Content: memory.Content, SourceHash: memory.SourceHash, SourceRefs: sourceRefs, Visibility: generated.MemoryVisibility(memory.Visibility), Status: generated.MemoryStatus(memory.Status), CreatedAt: memory.CreatedAt, UpdatedAt: memory.UpdatedAt}, nil
}

func marshalMemorySourceRefs(refs *[]string) (string, error) {
	if refs == nil {
		return "[]", nil
	}
	encoded, err := json.Marshal(*refs)
	if err != nil {
		return "", &core.ValidationError{Field: "source_refs", Message: "must be a JSON array of strings"}
	}
	return string(encoded), nil
}

func agentDefinitionResponse(agent core.AgentDefinition) (generated.AgentDefinition, error) {
	id, err := uuid.Parse(agent.ID)
	if err != nil {
		return generated.AgentDefinition{}, err
	}
	organizationID, err := uuid.Parse(agent.OrganizationID)
	if err != nil {
		return generated.AgentDefinition{}, err
	}
	projectID, err := uuid.Parse(agent.ProjectID)
	if err != nil {
		return generated.AgentDefinition{}, err
	}
	ownerID, err := uuid.Parse(agent.OwnerUserID)
	if err != nil {
		return generated.AgentDefinition{}, err
	}
	return generated.AgentDefinition{Id: id, OrganizationId: organizationID, ProjectId: projectID, OwnerUserId: ownerID, Name: agent.Name, Version: agent.Version, Instructions: agent.Instructions, Model: agent.Model, FallbackModel: agent.FallbackModel, ConfigJson: agent.ConfigJSON, Status: generated.AgentDefinitionStatus(agent.Status), CreatedAt: agent.CreatedAt, UpdatedAt: agent.UpdatedAt}, nil
}

func teamDefinitionResponse(team core.TeamDefinition) (generated.TeamDefinition, error) {
	parsedID, err := uuid.Parse(team.ID)
	if err != nil {
		return generated.TeamDefinition{}, err
	}
	id := openapi_types.UUID(parsedID)
	parsedProjectID, err := uuid.Parse(team.ProjectID)
	if err != nil {
		return generated.TeamDefinition{}, err
	}
	projectID := openapi_types.UUID(parsedProjectID)
	parsedOwnerID, err := uuid.Parse(team.OwnerUserID)
	if err != nil {
		return generated.TeamDefinition{}, err
	}
	ownerID := openapi_types.UUID(parsedOwnerID)
	roles := make([]generated.TeamRole, 0, len(team.Roles))
	for _, role := range team.Roles {
		roles = append(roles, generated.TeamRole{Role: role.Role, Name: role.Name})
	}
	return generated.TeamDefinition{Id: id, ProjectId: projectID, OwnerUserId: ownerID, Name: team.Name, Version: team.Version, Status: generated.TeamDefinitionStatus(team.Status), Roles: roles, ConfigJson: team.ConfigJSON, CreatedAt: team.CreatedAt, UpdatedAt: team.UpdatedAt}, nil
}

func conversationResponse(conversation core.Conversation) (generated.Conversation, error) {
	id, err := uuid.Parse(conversation.ID)
	if err != nil {
		return generated.Conversation{}, err
	}
	projectID, err := uuid.Parse(conversation.ProjectID)
	if err != nil {
		return generated.Conversation{}, err
	}
	return generated.Conversation{
		Id: id, ProjectId: projectID, Title: conversation.Title,
		Visibility:        generated.ConversationVisibility(conversation.Visibility),
		MessageCount:      conversation.MessageCount,
		LastMessage:       conversation.LastMessage,
		ActiveLeafEntryId: conversation.ActiveLeafEntryID,
		ParentSessionId:   conversation.ParentSessionID,
		ForkedFromEntryId: conversation.ForkedFromEntryID,
		CreatedAt:         conversation.CreatedAt,
		UpdatedAt:         conversation.UpdatedAt,
	}, nil
}

func sessionEntryResponse(entry core.SessionEntry) (generated.SessionEntry, error) {
	projectID, err := uuid.Parse(entry.ProjectID)
	if err != nil {
		return generated.SessionEntry{}, err
	}
	conversationID, err := uuid.Parse(entry.ConversationID)
	if err != nil {
		return generated.SessionEntry{}, err
	}
	payload := map[string]any{}
	if strings.TrimSpace(entry.PayloadJSON) != "" {
		if err := json.Unmarshal([]byte(entry.PayloadJSON), &payload); err != nil {
			return generated.SessionEntry{}, err
		}
	}
	return generated.SessionEntry{
		Id:               entry.ID,
		ProjectId:        projectID,
		ConversationId:   conversationID,
		ParentEntryId:    entry.ParentEntryID,
		EntrySeq:         entry.EntrySeq,
		Kind:             generated.SessionEntryKind(entry.Kind),
		RefId:            entry.RefID,
		Label:            entry.Label,
		Summary:          entry.Summary,
		Namespace:        entry.Namespace,
		Payload:          payload,
		SchemaVersion:    entry.SchemaVersion,
		ContextInjection: generated.SessionEntryContextInjection(entry.ContextInjection),
		CreatedAt:        entry.CreatedAt,
	}, nil
}

func messageResponse(message core.Message) (generated.Message, error) {
	id, err := uuid.Parse(message.ID)
	if err != nil {
		return generated.Message{}, err
	}
	projectID, err := uuid.Parse(message.ProjectID)
	if err != nil {
		return generated.Message{}, err
	}
	conversationID, err := uuid.Parse(message.ConversationID)
	if err != nil {
		return generated.Message{}, err
	}
	var taskID *openapi_types.UUID
	if message.TaskID != "" {
		parsedTaskID, parseErr := uuid.Parse(message.TaskID)
		if parseErr != nil {
			return generated.Message{}, parseErr
		}
		convertedTaskID := openapi_types.UUID(parsedTaskID)
		taskID = &convertedTaskID
	}
	presentation := generated.MessagePresentation{}
	if len(message.Presentation.Attachments) > 0 {
		attachments := make([]generated.MessageAttachment, 0, len(message.Presentation.Attachments))
		for _, attachment := range message.Presentation.Attachments {
			attachmentID, parseErr := uuid.Parse(attachment.ID)
			if parseErr != nil {
				return generated.Message{}, parseErr
			}
			attachments = append(attachments, generated.MessageAttachment{
				Id: attachmentID, Name: attachment.Name, MediaType: attachment.MediaType,
				SizeBytes: attachment.SizeBytes, ContentHash: attachment.ContentHash,
			})
		}
		presentation.Attachments = &attachments
	}
	if message.Presentation.Skill != nil {
		skillID, parseErr := uuid.Parse(message.Presentation.Skill.ID)
		if parseErr != nil {
			return generated.Message{}, parseErr
		}
		presentation.Skill = &generated.MessageSkillInvocation{
			Id: skillID, Name: message.Presentation.Skill.Name, Version: message.Presentation.Skill.Version,
			Command: message.Presentation.Skill.Command, Args: message.Presentation.Skill.Args,
		}
	}
	if message.Presentation.QueueKind != "" {
		queueKind := generated.MessagePresentationQueueKind(message.Presentation.QueueKind)
		presentation.QueueKind = &queueKind
	}
	return generated.Message{
		Id: id, ProjectId: projectID, ConversationId: conversationID, TaskId: taskID,
		ServerSeq: message.ServerSeq, Role: generated.MessageRole(message.Role),
		Visibility: generated.MessageVisibility(message.Visibility), Content: message.Content,
		Presentation: presentation, CreatedAt: message.CreatedAt,
	}, nil
}

func projectActivityResponse(event core.ProjectActivity) (generated.ProjectActivity, error) {
	id, err := uuid.Parse(event.ID)
	if err != nil {
		return generated.ProjectActivity{}, err
	}
	projectID, err := uuid.Parse(event.ProjectID)
	if err != nil {
		return generated.ProjectActivity{}, err
	}
	runID, err := uuid.Parse(event.RunID)
	if err != nil {
		return generated.ProjectActivity{}, err
	}
	taskID, err := uuid.Parse(event.TaskID)
	if err != nil {
		return generated.ProjectActivity{}, err
	}
	return generated.ProjectActivity{
		Id: id, ProjectId: projectID, RunId: runID, TaskId: taskID,
		TaskTitle: event.TaskTitle, TaskStatus: generated.ProjectActivityTaskStatus(event.TaskStatus),
		Mode: generated.ProjectActivityMode(event.Mode), EventType: event.EventType,
		Visibility: generated.ProjectActivityVisibility(event.Visibility), Payload: event.Payload,
		OccurredAt: event.OccurredAt,
	}, nil
}

func planItemResponse(planItem core.PlanItem) (generated.PlanItem, error) {
	id, err := uuid.Parse(planItem.ID)
	if err != nil {
		return generated.PlanItem{}, err
	}
	projectID, err := uuid.Parse(planItem.ProjectID)
	if err != nil {
		return generated.PlanItem{}, err
	}
	response := generated.PlanItem{
		Id: id, ProjectId: projectID, Title: planItem.Title, Description: planItem.Description,
		Status: generated.PlanItemStatus(planItem.Status), Source: generated.PlanItemSource(planItem.Source),
		Visibility: generated.PlanItemVisibility(planItem.Visibility), Rank: planItem.Rank,
		CreatedAt: planItem.CreatedAt, UpdatedAt: planItem.UpdatedAt,
	}
	if planItem.LinkedTaskID != "" {
		linkedTaskID, err := uuid.Parse(planItem.LinkedTaskID)
		if err != nil {
			return generated.PlanItem{}, err
		}
		response.LinkedTaskId = &linkedTaskID
	}
	return response, nil
}

func eventResponses(events []core.RunEvent, after int64) ([]generated.RunEvent, int64, error) {
	items := make([]generated.RunEvent, 0, len(events))
	next := after
	for _, event := range events {
		response, err := eventResponse(event)
		if err != nil {
			return nil, after, err
		}
		items = append(items, response)
		if event.StreamSeq > next {
			next = event.StreamSeq
		}
	}
	return items, next, nil
}

func eventResponse(event core.RunEvent) (generated.RunEvent, error) {
	id, err := uuid.Parse(event.ID)
	if err != nil {
		return generated.RunEvent{}, err
	}
	runID, err := uuid.Parse(event.RunID)
	if err != nil {
		return generated.RunEvent{}, err
	}
	return generated.RunEvent{
		Id:         id,
		RunId:      runID,
		StreamSeq:  event.StreamSeq,
		EventType:  event.EventType,
		OccurredAt: event.OccurredAt,
		Payload:    event.Payload,
	}, nil
}

func acceptedOutcomeResponse(outcome core.AcceptedOutcome) (generated.AcceptedOutcome, error) {
	id, err := uuid.Parse(outcome.ID)
	if err != nil {
		return generated.AcceptedOutcome{}, err
	}
	projectID, err := uuid.Parse(outcome.ProjectID)
	if err != nil {
		return generated.AcceptedOutcome{}, err
	}
	taskID, err := uuid.Parse(outcome.TaskID)
	if err != nil {
		return generated.AcceptedOutcome{}, err
	}
	runID, err := uuid.Parse(outcome.RunID)
	if err != nil {
		return generated.AcceptedOutcome{}, err
	}
	acceptedBy, err := uuid.Parse(outcome.AcceptedBy)
	if err != nil {
		return generated.AcceptedOutcome{}, err
	}
	return generated.AcceptedOutcome{Id: id, ProjectId: projectID, TaskId: taskID, RunId: runID, AcceptedBy: acceptedBy, Note: outcome.Note, CreatedAt: outcome.CreatedAt}, nil
}

func artifactResponse(artifact core.Artifact) (generated.Artifact, error) {
	id, err := uuid.Parse(artifact.ID)
	if err != nil {
		return generated.Artifact{}, err
	}
	projectID, err := uuid.Parse(artifact.ProjectID)
	if err != nil {
		return generated.Artifact{}, err
	}
	taskID, err := uuid.Parse(artifact.TaskID)
	if err != nil {
		return generated.Artifact{}, err
	}
	runID, err := uuid.Parse(artifact.RunID)
	if err != nil {
		return generated.Artifact{}, err
	}
	stepID, err := uuid.Parse(artifact.StepID)
	if err != nil {
		return generated.Artifact{}, err
	}
	return generated.Artifact{Id: id, ProjectId: projectID, TaskId: taskID, RunId: runID, StepId: stepID, Name: artifact.Name, Kind: artifact.Kind, MediaType: artifact.MediaType, ContentHash: artifact.ContentHash, SizeBytes: artifact.SizeBytes, Visibility: generated.ArtifactVisibility(artifact.Visibility), SourceType: artifact.SourceType, CreatedAt: artifact.CreatedAt}, nil
}

func assetResponse(asset core.Asset) (generated.Asset, error) {
	id, err := uuid.Parse(asset.ID)
	if err != nil {
		return generated.Asset{}, err
	}
	projectID, err := uuid.Parse(asset.ProjectID)
	if err != nil {
		return generated.Asset{}, err
	}
	ownerUserID, err := uuid.Parse(asset.OwnerUserID)
	if err != nil {
		return generated.Asset{}, err
	}
	return generated.Asset{
		Id: id, ProjectId: projectID, OwnerUserId: ownerUserID, ParentId: asset.ParentID,
		Name: asset.Name, Kind: generated.AssetKind(asset.Kind), MediaType: asset.MediaType,
		ContentHash: asset.ContentHash, SizeBytes: asset.SizeBytes, Visibility: generated.AssetVisibility(asset.Visibility),
		SourceType: generated.AssetSourceType(asset.SourceType), SourceArtifactId: asset.SourceArtifactID,
		CreatedAt: asset.CreatedAt, UpdatedAt: asset.UpdatedAt,
	}, nil
}

func approvalResponse(approval core.Approval) (generated.Approval, error) {
	id, err := uuid.Parse(approval.ID)
	if err != nil {
		return generated.Approval{}, err
	}
	projectID, err := uuid.Parse(approval.ProjectID)
	if err != nil {
		return generated.Approval{}, err
	}
	taskID, err := uuid.Parse(approval.TaskID)
	if err != nil {
		return generated.Approval{}, err
	}
	runID, err := uuid.Parse(approval.RunID)
	if err != nil {
		return generated.Approval{}, err
	}
	stepID, err := uuid.Parse(approval.StepID)
	if err != nil {
		return generated.Approval{}, err
	}
	toolCallID, err := uuid.Parse(approval.ToolCallID)
	if err != nil {
		return generated.Approval{}, err
	}
	response := generated.Approval{
		Id: id, ProjectId: projectID, TaskId: taskID, RunId: runID, StepId: stepID,
		ToolCallId: toolCallID, ToolName: approval.ToolName, RiskLevel: generated.ApprovalRiskLevel(approval.RiskLevel),
		Status: generated.ApprovalStatus(approval.Status), Reason: approval.Reason, FrozenParams: approval.FrozenParams,
		InputHash: approval.InputHash, RequestedBy: approval.RequestedBy, RequestedAt: approval.RequestedAt, ExpiresAt: approval.ExpiresAt,
		DecidedAt: approval.DecidedAt,
	}
	if approval.DecidedBy != "" {
		response.DecidedBy = &approval.DecidedBy
	}
	return response, nil
}

func humanActionResponse(action core.HumanAction) (generated.HumanAction, error) {
	id, err := uuid.Parse(action.ID)
	if err != nil {
		return generated.HumanAction{}, err
	}
	projectID, err := uuid.Parse(action.ProjectID)
	if err != nil {
		return generated.HumanAction{}, err
	}
	taskID, err := uuid.Parse(action.TaskID)
	if err != nil {
		return generated.HumanAction{}, err
	}
	runID, err := uuid.Parse(action.RunID)
	if err != nil {
		return generated.HumanAction{}, err
	}
	return generated.HumanAction{
		Id: id, ProjectId: projectID, ConversationId: action.ConversationID,
		TaskId: taskID, RunId: runID, StepId: action.StepID,
		PlanRevisionId: action.PlanRevisionID, RunPlanItemId: action.RunPlanItemID,
		Kind: generated.HumanActionKind(action.Kind), Status: generated.HumanActionStatus(action.Status),
		Reviewer: generated.HumanActionReviewer(action.Reviewer), RiskLevel: action.RiskLevel,
		Title: action.Title, Reason: action.Reason, Request: action.Request,
		RequestHash: action.RequestHash, Response: action.Response,
		RequestedBy: action.RequestedBy, DecidedBy: action.DecidedBy,
		RequestedAt: action.RequestedAt, ExpiresAt: action.ExpiresAt,
		DecidedAt: action.DecidedAt, Version: action.Version,
	}, nil
}

func runPlanRevisionResponse(plan core.RunPlanRevision) (generated.RunPlanRevision, error) {
	id, err := uuid.Parse(plan.ID)
	if err != nil {
		return generated.RunPlanRevision{}, err
	}
	projectID, err := uuid.Parse(plan.ProjectID)
	if err != nil {
		return generated.RunPlanRevision{}, err
	}
	taskID, err := uuid.Parse(plan.TaskID)
	if err != nil {
		return generated.RunPlanRevision{}, err
	}
	runID, err := uuid.Parse(plan.RunID)
	if err != nil {
		return generated.RunPlanRevision{}, err
	}
	items := make([]generated.RunPlanItem, 0, len(plan.Items))
	for _, planItem := range plan.Items {
		itemID, parseErr := uuid.Parse(planItem.ID)
		if parseErr != nil {
			return generated.RunPlanRevision{}, parseErr
		}
		revisionID, parseErr := uuid.Parse(planItem.PlanRevisionID)
		if parseErr != nil {
			return generated.RunPlanRevision{}, parseErr
		}
		items = append(items, generated.RunPlanItem{
			Id: itemID, PlanRevisionId: revisionID, Position: planItem.Position,
			Title: planItem.Title, Description: planItem.Description,
			Status:        generated.RunPlanItemStatus(planItem.Status),
			ResultSummary: planItem.ResultSummary, StartedAt: planItem.StartedAt,
			CompletedAt: planItem.CompletedAt, CreatedAt: planItem.CreatedAt,
			UpdatedAt: planItem.UpdatedAt,
		})
	}
	return generated.RunPlanRevision{
		Id: id, ProjectId: projectID, TaskId: taskID, RunId: runID,
		Revision: plan.Revision, Trigger: generated.RunPlanRevisionTrigger(plan.Trigger),
		GoalSummary: plan.GoalSummary, Status: generated.RunPlanRevisionStatus(plan.Status),
		SupersedesRevisionId: plan.SupersedesRevisionID, CreatedBy: plan.CreatedBy,
		CreatedAt: plan.CreatedAt, SupersededAt: plan.SupersededAt, Items: items,
	}, nil
}

func (s *Server) correlationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if _, err := uuid.Parse(id); err != nil {
			generatedID, generateErr := uuid.NewV7()
			if generateErr != nil {
				id = fmt.Sprintf("corr-%d", time.Now().UnixNano())
			} else {
				id = generatedID.String()
			}
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey{}, id)))
	})
}

func requestBodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/projects/") && strings.HasSuffix(r.URL.Path, "/assets") {
			r.Body = http.MaxBytesReader(w, r.Body, application.MaxAssetFileBytes+1024*1024)
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapper := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(wrapper, r)
		s.logger.InfoContext(r.Context(), "http request", "method", r.Method, "path", r.URL.Path, "status", wrapper.Status(), "duration_ms", time.Since(started).Milliseconds(), "correlation_id", correlationID(r.Context()))
	})
}

func (s *Server) requestError(w http.ResponseWriter, r *http.Request, err error) {
	s.writeMappedProblem(w, r, err)
}

func (s *Server) responseError(w http.ResponseWriter, r *http.Request, err error) {
	s.writeMappedProblem(w, r, err)
}

func (s *Server) writeMappedProblem(w http.ResponseWriter, r *http.Request, err error) {
	var validation *core.ValidationError
	var forbidden *core.ForbiddenError
	var idempotencyConflict *core.IdempotencyConflictError
	var stateConflict *core.StateConflictError
	var resourceConflict *core.ResourceConflictError
	var modelInUse *core.ModelInUseError
	var capacityExceeded *core.CapacityExceededError
	var modelCatalogUnavailable *application.ModelCatalogUnavailableError
	var modelTurnUnavailable *application.ModelTurnUnavailableError
	var maxBytes *http.MaxBytesError
	switch {
	case errors.As(err, &forbidden):
		s.writeProblem(w, r, http.StatusForbidden, "FORBIDDEN", "Forbidden", forbidden.Error())
	case errors.As(err, &validation):
		s.writeProblem(w, r, http.StatusBadRequest, "VALIDATION_FAILED", "Invalid request", validation.Error())
	case errors.As(err, &idempotencyConflict):
		s.writeProblem(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency conflict", idempotencyConflict.Error())
	case errors.As(err, &stateConflict):
		s.writeProblem(w, r, http.StatusConflict, "INVALID_STATE_TRANSITION", "Invalid state transition", stateConflict.Error())
	case errors.As(err, &resourceConflict):
		s.writeProblem(w, r, http.StatusConflict, "RESOURCE_CONFLICT", "Resource conflict", resourceConflict.Error())
	case errors.As(err, &modelInUse):
		s.writeProblem(w, r, http.StatusConflict, "MODEL_IN_USE", "Model in use", modelInUse.Error())
	case errors.As(err, &capacityExceeded):
		s.writeProblem(w, r, http.StatusInsufficientStorage, "PROJECT_CAPACITY_EXCEEDED", "Project capacity exceeded", capacityExceeded.Error())
	case errors.As(err, &modelCatalogUnavailable):
		s.writeProblem(w, r, http.StatusServiceUnavailable, modelCatalogUnavailable.Code, "Model catalog unavailable", "the configured model provider could not return a usable model catalog")
	case errors.As(err, &modelTurnUnavailable):
		detail := modelTurnUnavailable.Error()
		s.writeProblem(w, r, http.StatusServiceUnavailable, "MODEL_TURN_UNAVAILABLE", "Model unavailable", detail)
	case errors.As(err, &maxBytes):
		s.writeProblem(w, r, http.StatusRequestEntityTooLarge, "REQUEST_TOO_LARGE", "Request too large", fmt.Sprintf("request body must not exceed %d bytes", maxBytes.Limit))
	case errors.Is(err, sql.ErrNoRows):
		s.writeProblem(w, r, http.StatusNotFound, "RESOURCE_NOT_FOUND", "Resource not found", "the resource does not exist or is not visible")
	default:
		s.logger.ErrorContext(r.Context(), "request failed", "error", err, "correlation_id", correlationID(r.Context()))
		s.writeProblem(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error", "the request could not be completed")
	}
}

func (s *Server) writeProblem(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(generated.Problem{Type: "about:blank", Title: title, Status: status, Code: code, CorrelationId: correlationID(r.Context()), Detail: &detail})
}

func correlationID(ctx context.Context) string {
	if value, ok := ctx.Value(correlationKey{}).(string); ok && value != "" {
		return value
	}
	return "unknown"
}

func optionalIdempotencyKey(value *generated.IdempotencyKey) string {
	if value == nil {
		return ""
	}
	return *value
}

func firstFormValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
