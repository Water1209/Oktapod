package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultBrokerMaxSessions = 32

// SessionBroker preserves MCP protocol state only inside one frozen Run and
// capability boundary. Authorization and durable receipts remain per tool
// invocation; the broker owns transport continuity, not execution policy.
// Idle sessions and all subprocesses are closed on expiry or process shutdown.
type SessionBroker struct {
	ctx         context.Context
	idleTTL     time.Duration
	maxSessions int

	mu       sync.Mutex
	sessions map[string]*brokerSession
	closed   bool
}

type brokerSession struct {
	session  sdkClientSession
	close    func()
	gate     chan struct{}
	lastUsed time.Time
	dead     bool
}

// sdkClientSession keeps the broker testable while remaining a narrow wrapper
// around the SDK session used by callSDKTool.
type sdkClientSession interface {
	callTool(context.Context, string, map[string]any) (CallResult, error)
}

type liveSDKClientSession struct {
	session *sdkmcp.ClientSession
}

func (session liveSDKClientSession) callTool(ctx context.Context, toolName string, arguments map[string]any) (CallResult, error) {
	return callSDKTool(ctx, session.session, toolName, arguments)
}

func NewSessionBroker(ctx context.Context, idleTTL time.Duration) *SessionBroker {
	if ctx == nil {
		ctx = context.Background()
	}
	if idleTTL <= 0 {
		idleTTL = 15 * time.Minute
	}
	broker := &SessionBroker{ctx: ctx, idleTTL: idleTTL, maxSessions: defaultBrokerMaxSessions, sessions: map[string]*brokerSession{}}
	go broker.reapLoop()
	return broker
}

// Call serializes calls for one Run/capability session. A transport failure
// evicts the session but is never retried here because Receipt retry semantics
// are owned by ToolRuntime.
func (broker *SessionBroker) Call(ctx context.Context, key, manifestJSON, credential, toolName string, arguments map[string]any, updater CredentialUpdater) (CallResult, error) {
	if broker == nil || key == "" {
		return CallWithCredentialUpdater(ctx, manifestJSON, credential, toolName, arguments, updater)
	}
	for {
		entry, err := broker.getOrOpen(manifestJSON, credential, key, updater)
		if err != nil {
			return CallResult{}, err
		}
		select {
		case <-ctx.Done():
			return CallResult{}, ctx.Err()
		case <-entry.gate:
		}

		broker.mu.Lock()
		dead := entry.dead || broker.closed
		broker.mu.Unlock()
		if dead {
			entry.gate <- struct{}{}
			if broker.closed {
				return CallResult{}, errors.New("MCP session broker is closed")
			}
			continue
		}

		result, callErr := entry.session.callTool(ctx, toolName, arguments)
		var closeSession func()
		broker.mu.Lock()
		entry.lastUsed = time.Now().UTC()
		if callErr != nil {
			entry.dead = true
			if current := broker.sessions[key]; current == entry {
				delete(broker.sessions, key)
			}
			closeSession = entry.close
		}
		broker.mu.Unlock()
		entry.gate <- struct{}{}
		if closeSession != nil {
			closeSession()
		}
		return result, callErr
	}
}

func (broker *SessionBroker) getOrOpen(manifestJSON, credential, key string, updater CredentialUpdater) (*brokerSession, error) {
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if broker.closed {
		return nil, errors.New("MCP session broker is closed")
	}
	if existing := broker.sessions[key]; existing != nil && !existing.dead {
		return existing, nil
	}
	broker.evictOldestLocked()
	spec, err := decodeManifest(manifestJSON)
	if err != nil {
		return nil, err
	}
	spec.Transport = canonicalSessionTransport(spec)
	credential, err = manifestCredential(spec, credential)
	if err != nil {
		return nil, err
	}
	session, closeSession, err := openSDKSession(broker.ctx, spec, credential, SessionOptions{CredentialUpdater: updater})
	if err != nil {
		return nil, err
	}
	entry := &brokerSession{
		session:  liveSDKClientSession{session: session},
		close:    closeSession,
		gate:     make(chan struct{}, 1),
		lastUsed: time.Now().UTC(),
	}
	entry.gate <- struct{}{}
	broker.sessions[key] = entry
	return entry, nil
}

func canonicalSessionTransport(spec manifest) string {
	transport := strings.ToLower(strings.TrimSpace(spec.Transport))
	if transport != "" {
		return transport
	}
	if spec.URL != "" {
		return "streamable_http"
	}
	return "stdio"
}

func (broker *SessionBroker) evictOldestLocked() {
	if len(broker.sessions) < broker.maxSessions {
		return
	}
	var oldestKey string
	var oldest *brokerSession
	for key, entry := range broker.sessions {
		if oldest == nil || entry.lastUsed.Before(oldest.lastUsed) {
			oldestKey, oldest = key, entry
		}
	}
	if oldest == nil {
		return
	}
	select {
	case <-oldest.gate:
		oldest.dead = true
		delete(broker.sessions, oldestKey)
		oldest.gate <- struct{}{}
		oldest.close()
	default:
	}
}

func (broker *SessionBroker) reapLoop() {
	interval := broker.idleTTL / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-broker.ctx.Done():
			_ = broker.Close()
			return
		case <-ticker.C:
			broker.reapIdle(time.Now().UTC())
		}
	}
}

func (broker *SessionBroker) reapIdle(now time.Time) {
	var closers []func()
	broker.mu.Lock()
	for key, entry := range broker.sessions {
		if now.Sub(entry.lastUsed) < broker.idleTTL {
			continue
		}
		select {
		case <-entry.gate:
			entry.dead = true
			delete(broker.sessions, key)
			entry.gate <- struct{}{}
			closers = append(closers, entry.close)
		default:
		}
	}
	broker.mu.Unlock()
	for _, closeSession := range closers {
		closeSession()
	}
}

func (broker *SessionBroker) Close() error {
	if broker == nil {
		return nil
	}
	broker.mu.Lock()
	if broker.closed {
		broker.mu.Unlock()
		return nil
	}
	broker.closed = true
	closers := make([]func(), 0, len(broker.sessions))
	for key, entry := range broker.sessions {
		entry.dead = true
		delete(broker.sessions, key)
		closers = append(closers, entry.close)
	}
	broker.mu.Unlock()
	for _, closeSession := range closers {
		closeSession()
	}
	return nil
}
