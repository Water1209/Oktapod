package documentruntime

import (
	"context"
	"fmt"
	"sync"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// RuntimeGate keeps the main HTTP service available while a signed document
// runtime is being verified. It never delegates a document call until a
// verified executor has been installed.
type RuntimeGate struct {
	mu       sync.RWMutex
	executor application.DocumentExecutor
	health   application.DocumentRuntimeHealth
	repairMu sync.Mutex
	repair   func(context.Context) error
}

func NewRuntimeGate() *RuntimeGate {
	return &RuntimeGate{health: application.DocumentRuntimeHealth{Protocol: protocolVersion, DegradedReason: "document runtime is verifying"}}
}

func (g *RuntimeGate) SetRepair(repair func(context.Context) error) { g.repair = repair }

func (g *RuntimeGate) SetReady(executor application.DocumentExecutor, health application.DocumentRuntimeHealth) {
	g.mu.Lock()
	g.executor, g.health = executor, health
	g.mu.Unlock()
}

func (g *RuntimeGate) SetDegraded(reason string) {
	g.mu.Lock()
	g.executor = nil
	g.health = application.DocumentRuntimeHealth{Protocol: protocolVersion, DegradedReason: reason}
	g.mu.Unlock()
}

func (g *RuntimeGate) Health(context.Context) application.DocumentRuntimeHealth {
	g.mu.RLock()
	deferred := g.health
	g.mu.RUnlock()
	return deferred
}

func (g *RuntimeGate) Repair(ctx context.Context) error {
	if g.repair == nil {
		return fmt.Errorf("document runtime repair is not configured")
	}
	g.repairMu.Lock()
	defer g.repairMu.Unlock()
	return g.repair(ctx)
}

func (g *RuntimeGate) Execute(ctx context.Context, run core.RunContext, call core.ToolCall) core.ToolExecution {
	g.mu.RLock()
	executor := g.executor
	reason := g.health.DegradedReason
	g.mu.RUnlock()
	if executor == nil {
		planned := core.PlanTool(call)
		if reason == "" {
			reason = "document runtime is not ready"
		}
		return core.ToolExecution{ToolCallID: call.ID, Status: core.StatusBlocked, ErrorCode: "DOCUMENT_RUNTIME_UNAVAILABLE", ExitCode: -1, InputHash: planned.InputHash, Idempotent: true, Output: map[string]any{"reason": reason, "retryable": true}}
	}
	return executor.Execute(ctx, run, call)
}

func (g *RuntimeGate) currentPreviewExecutor() (application.DocumentExecutor, error) {
	g.mu.RLock()
	executor := g.executor
	reason := g.health.DegradedReason
	g.mu.RUnlock()
	if executor == nil {
		if reason == "" {
			reason = "document runtime is not ready"
		}
		return nil, &core.ResourceConflictError{Resource: "document runtime", Reason: reason}
	}
	return executor, nil
}

func (g *RuntimeGate) CreateFilePreview(ctx context.Context, scope core.Scope, source application.FilePreviewSource) (application.FilePreviewSession, error) {
	executor, err := g.currentPreviewExecutor()
	if err != nil {
		return application.FilePreviewSession{}, err
	}
	previewer, ok := executor.(application.FilePreviewer)
	if !ok {
		return application.FilePreviewSession{}, fmt.Errorf("document runtime preview is unavailable")
	}
	return previewer.CreateFilePreview(ctx, scope, source)
}

func (g *RuntimeGate) OpenFilePreview(ctx context.Context, scope core.Scope, previewID, previewName string) (application.DocumentPreview, error) {
	executor, err := g.currentPreviewExecutor()
	if err != nil {
		return application.DocumentPreview{}, err
	}
	previewer, ok := executor.(application.FilePreviewer)
	if !ok {
		return application.DocumentPreview{}, fmt.Errorf("document runtime preview is unavailable")
	}
	return previewer.OpenFilePreview(ctx, scope, previewID, previewName)
}

func (g *RuntimeGate) Close() {
	g.mu.Lock()
	if closer, ok := g.executor.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
	g.executor = nil
	g.mu.Unlock()
}
