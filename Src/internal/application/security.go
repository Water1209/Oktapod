package application

import (
	"context"
	"errors"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

var ErrPrincipalRequired = errors.New("authenticated principal required")

type AuthenticationMethod string

const (
	AuthenticationSession    AuthenticationMethod = "session"
	AuthenticationBearer     AuthenticationMethod = "bearer"
	AuthenticationAdminToken AuthenticationMethod = "admin_token"
	AuthenticationLocal      AuthenticationMethod = "local"
	AuthenticationTrusted    AuthenticationMethod = "trusted_test"
)

// Principal is the authenticated caller identity. Authorization and execution
// policy receive this object rather than inferring identity from transport
// headers, a default actor, or a workspace path.
type Principal struct {
	OrganizationID       string
	UserID               string
	AuthenticationMethod AuthenticationMethod
	Administrator        bool
}

func (p Principal) Scope() core.Scope {
	return core.Scope{OrganizationID: p.OrganizationID, UserID: p.UserID}
}

func (p Principal) Valid() bool {
	return p.OrganizationID != "" && p.UserID != ""
}

type PrincipalCredential struct {
	Method AuthenticationMethod
	Secret string
}

type PrincipalResolver interface {
	ResolvePrincipal(ctx context.Context, credential PrincipalCredential) (Principal, error)
}

// LocalPrincipalResolver is deliberately limited to explicit offline entry
// points such as CLI/bootstrap. HTTP must use a session-backed resolver.
type LocalPrincipalResolver struct {
	Principal Principal
}

func (r LocalPrincipalResolver) ResolvePrincipal(_ context.Context, credential PrincipalCredential) (Principal, error) {
	if credential.Method != AuthenticationLocal || !r.Principal.Valid() {
		return Principal{}, ErrPrincipalRequired
	}
	principal := r.Principal
	principal.AuthenticationMethod = AuthenticationLocal
	return principal, nil
}

type AccessRequest struct {
	Action       string
	ResourceType string
	ResourceID   string
	ProjectID    string
}

type Authorizer interface {
	Authorize(ctx context.Context, principal Principal, request AccessRequest) error
}

type AuthorizationRepository interface {
	GetProject(ctx context.Context, scope core.Scope, projectID string) (core.Project, error)
	GetConversation(ctx context.Context, scope core.Scope, conversationID string) (core.Conversation, error)
	GetTask(ctx context.Context, scope core.Scope, taskID string) (core.Task, error)
}

// RepositoryAuthorizer makes resource authorization an application port while
// preserving the repository's scoped query as defense in depth.
type RepositoryAuthorizer struct {
	Repository AuthorizationRepository
}

func (a RepositoryAuthorizer) Authorize(ctx context.Context, principal Principal, request AccessRequest) error {
	if !principal.Valid() || a.Repository == nil {
		return ErrPrincipalRequired
	}
	scope := principal.Scope()
	switch request.ResourceType {
	case "project":
		_, err := a.Repository.GetProject(ctx, scope, request.ResourceID)
		return err
	case "thread":
		_, err := a.Repository.GetConversation(ctx, scope, request.ResourceID)
		return err
	case "task":
		_, err := a.Repository.GetTask(ctx, scope, request.ResourceID)
		return err
	default:
		return errors.New("unsupported authorization resource")
	}
}

type ExecutionRequest struct {
	ProjectID      string
	Call           core.ToolCall
	ConfigSnapshot string
}

type ExecutionPolicy interface {
	EvaluateExecution(ctx context.Context, principal Principal, request ExecutionRequest) (core.PlannedTool, error)
}

// StaticExecutionPolicy is the single deterministic policy path used both
// when persisting a ToolCall and immediately before the adapter executes it.
// Approval grants may narrow an Ask to Allow, but cannot bypass a hard Deny.
type StaticExecutionPolicy struct{}

func (StaticExecutionPolicy) EvaluateExecution(_ context.Context, _ Principal, request ExecutionRequest) (core.PlannedTool, error) {
	return core.PlanTool(request.Call), nil
}

type principalContextKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	ctx = context.WithValue(ctx, principalContextKey{}, principal)
	ctx = WithScope(ctx, principal.Scope())
	return WithAdmin(ctx, principal.Administrator)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok && principal.Valid()
}
