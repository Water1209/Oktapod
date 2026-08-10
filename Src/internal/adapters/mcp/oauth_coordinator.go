package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const oauthFlowLifetime = 10 * time.Minute

type OAuthSecretStore interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
}

// OAuthCoordinator owns only short-lived browser authorization state. Durable
// credentials stay in SecretStore and durable MCP health stays in the resource
// repository, preserving the application/adapter boundary.
type OAuthCoordinator struct {
	root        context.Context
	secrets     OAuthSecretStore
	redirectURL string

	mu           sync.Mutex
	byID         map[string]*oauthFlow
	byState      map[string]*oauthFlow
	byCapability map[string]*oauthFlow
}

type oauthFlow struct {
	mu         sync.Mutex
	session    core.MCPOAuthSession
	state      string
	credential string
	result     chan *sdkauth.AuthorizationResult
	ready      chan struct{}
	done       chan struct{}
	readyOnce  sync.Once
	cancel     context.CancelFunc
}

func NewOAuthCoordinator(root context.Context, secrets OAuthSecretStore, redirectURL string) (*OAuthCoordinator, error) {
	if root == nil || secrets == nil {
		return nil, errors.New("OAuth coordinator requires a lifecycle context and SecretStore")
	}
	parsed, err := url.Parse(strings.TrimSpace(redirectURL))
	if err != nil || parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) || parsed.Host == "" {
		return nil, errors.New("MCP OAuth redirect URL must use an absolute loopback HTTP address")
	}
	return &OAuthCoordinator{
		root: root, secrets: secrets, redirectURL: parsed.String(),
		byID: map[string]*oauthFlow{}, byState: map[string]*oauthFlow{}, byCapability: map[string]*oauthFlow{},
	}, nil
}

func (coordinator *OAuthCoordinator) Start(ctx context.Context, capability core.Capability) (core.MCPOAuthSession, error) {
	spec, err := decodeManifest(capability.ManifestJSON)
	if err != nil || spec.AuthType != "oauth" || (spec.Transport != "streamable_http" && spec.Transport != "http") {
		return core.MCPOAuthSession{}, errors.New("capability is not a remote OAuth MCP")
	}
	if strings.TrimSpace(capability.CredentialRef) == "" {
		return core.MCPOAuthSession{}, errors.New("OAuth MCP has no SecretStore reference")
	}
	credential, _ := coordinator.secrets.Get(ctx, capability.CredentialRef)
	now := time.Now().UTC()
	flowContext, cancel := context.WithTimeout(coordinator.root, oauthFlowLifetime)
	flow := &oauthFlow{
		session: core.MCPOAuthSession{
			ID: uuid.NewString(), CapabilityID: capability.ID, Status: core.MCPOAuthStatusPreparing,
			ExpiresAt: now.Add(oauthFlowLifetime), UpdatedAt: now,
		},
		credential: credential, result: make(chan *sdkauth.AuthorizationResult, 1),
		ready: make(chan struct{}), done: make(chan struct{}), cancel: cancel,
	}
	coordinator.mu.Lock()
	coordinator.pruneLocked(now)
	if previous := coordinator.byCapability[capability.ID]; previous != nil {
		previous.cancelFlow()
	}
	coordinator.byID[flow.session.ID] = flow
	coordinator.byCapability[capability.ID] = flow
	coordinator.mu.Unlock()

	go coordinator.runFlow(flowContext, flow, capability)

	select {
	case <-flow.ready:
		return flow.snapshot(), nil
	case <-ctx.Done():
		flow.cancelFlow()
		return core.MCPOAuthSession{}, ctx.Err()
	case <-time.After(30 * time.Second):
		flow.cancelFlow()
		return core.MCPOAuthSession{}, errors.New("OAuth server did not produce an authorization URL in time")
	}
}

func (coordinator *OAuthCoordinator) runFlow(ctx context.Context, flow *oauthFlow, capability core.Capability) {
	defer close(flow.done)
	fetcher := func(fetchContext context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
		target, err := url.Parse(strings.TrimSpace(args.URL))
		if err != nil || target.User != nil || !authorizationURLAllowed(target) {
			return nil, errors.New("OAuth authorization URL is not allowed")
		}
		state := strings.TrimSpace(target.Query().Get("state"))
		if len(state) < 16 {
			return nil, errors.New("OAuth authorization state is invalid")
		}
		flow.mu.Lock()
		flow.state = state
		flow.session.AuthorizationURL = target.String()
		flow.session.Status = core.MCPOAuthStatusWaiting
		flow.session.UpdatedAt = time.Now().UTC()
		flow.mu.Unlock()
		coordinator.mu.Lock()
		coordinator.byState[state] = flow
		coordinator.mu.Unlock()
		flow.signalReady()
		select {
		case result := <-flow.result:
			return result, nil
		case <-fetchContext.Done():
			return nil, fetchContext.Err()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	_, err := ConnectWithOptions(ctx, capability.ManifestJSON, flow.credential, SessionOptions{
		AuthorizationCodeFetcher: fetcher,
		CredentialUpdater: func(saveContext context.Context, payload string) error {
			return coordinator.secrets.Set(saveContext, capability.CredentialRef, payload)
		},
		RedirectURL: coordinator.redirectURL,
	})
	now := time.Now().UTC()
	flow.mu.Lock()
	if flow.session.Status != core.MCPOAuthStatusCanceled {
		if err == nil {
			flow.session.Status = core.MCPOAuthStatusConnected
			flow.session.LastError = ""
		} else if errors.Is(ctx.Err(), context.Canceled) {
			flow.session.Status = core.MCPOAuthStatusCanceled
			flow.session.LastError = ""
		} else {
			flow.session.Status = core.MCPOAuthStatusFailed
			flow.session.LastError = safeMCPError(err)
		}
	}
	flow.session.UpdatedAt = now
	flow.session.ExpiresAt = now.Add(5 * time.Minute)
	flow.mu.Unlock()
	flow.signalReady()
}

func (coordinator *OAuthCoordinator) Complete(ctx context.Context, state, code, issuer string) (core.MCPOAuthSession, error) {
	flow, err := coordinator.flowByState(state)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	if strings.TrimSpace(code) == "" {
		return core.MCPOAuthSession{}, errors.New("OAuth callback code is missing")
	}
	flow.mu.Lock()
	if flow.session.Status != core.MCPOAuthStatusWaiting {
		status := flow.session.Status
		flow.mu.Unlock()
		return core.MCPOAuthSession{}, fmt.Errorf("OAuth session is %s", status)
	}
	flow.mu.Unlock()
	select {
	case flow.result <- &sdkauth.AuthorizationResult{Code: strings.TrimSpace(code), State: strings.TrimSpace(state), Iss: strings.TrimSpace(issuer)}:
	case <-ctx.Done():
		return core.MCPOAuthSession{}, ctx.Err()
	case <-flow.done:
		return flow.snapshot(), nil
	}
	select {
	case <-flow.done:
		return flow.snapshot(), nil
	case <-ctx.Done():
		return core.MCPOAuthSession{}, ctx.Err()
	}
}

func (coordinator *OAuthCoordinator) LookupState(_ context.Context, state string) (core.MCPOAuthSession, error) {
	flow, err := coordinator.flowByState(state)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	return flow.snapshot(), nil
}

func (coordinator *OAuthCoordinator) Status(_ context.Context, sessionID string) (core.MCPOAuthSession, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.pruneLocked(time.Now().UTC())
	flow := coordinator.byID[strings.TrimSpace(sessionID)]
	if flow == nil {
		return core.MCPOAuthSession{}, errors.New("OAuth session was not found or has expired")
	}
	return flow.snapshot(), nil
}

func (coordinator *OAuthCoordinator) Cancel(ctx context.Context, sessionID string) (core.MCPOAuthSession, error) {
	session, err := coordinator.Status(ctx, sessionID)
	if err != nil {
		return core.MCPOAuthSession{}, err
	}
	coordinator.mu.Lock()
	flow := coordinator.byID[session.ID]
	coordinator.mu.Unlock()
	flow.cancelFlow()
	select {
	case <-flow.done:
	case <-ctx.Done():
		return core.MCPOAuthSession{}, ctx.Err()
	}
	return flow.snapshot(), nil
}

func (coordinator *OAuthCoordinator) flowByState(state string) (*oauthFlow, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.pruneLocked(time.Now().UTC())
	flow := coordinator.byState[strings.TrimSpace(state)]
	if flow == nil {
		return nil, errors.New("OAuth state was not found or has expired")
	}
	return flow, nil
}

func (coordinator *OAuthCoordinator) pruneLocked(now time.Time) {
	for id, flow := range coordinator.byID {
		session := flow.snapshot()
		if now.Before(session.ExpiresAt) {
			continue
		}
		flow.cancelFlow()
		delete(coordinator.byID, id)
		if coordinator.byCapability[session.CapabilityID] == flow {
			delete(coordinator.byCapability, session.CapabilityID)
		}
		state := flow.stateSnapshot()
		if state != "" && coordinator.byState[state] == flow {
			delete(coordinator.byState, state)
		}
	}
}

func (flow *oauthFlow) snapshot() core.MCPOAuthSession {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	return flow.session
}

func (flow *oauthFlow) stateSnapshot() string {
	flow.mu.Lock()
	defer flow.mu.Unlock()
	return flow.state
}

func (flow *oauthFlow) signalReady() {
	flow.readyOnce.Do(func() { close(flow.ready) })
}

func (flow *oauthFlow) cancelFlow() {
	flow.mu.Lock()
	if flow.session.Status == core.MCPOAuthStatusPreparing || flow.session.Status == core.MCPOAuthStatusWaiting {
		flow.session.Status = core.MCPOAuthStatusCanceled
		flow.session.LastError = ""
		flow.session.UpdatedAt = time.Now().UTC()
	}
	flow.mu.Unlock()
	flow.cancel()
	flow.signalReady()
}

func authorizationURLAllowed(target *url.URL) bool {
	if target == nil || target.Host == "" || target.Fragment != "" {
		return false
	}
	if target.Scheme == "https" {
		return true
	}
	return target.Scheme == "http" && isLoopbackHost(target.Hostname())
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
