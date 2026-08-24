package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type workspaceExecutionStub struct{}

func (workspaceExecutionStub) Execute(_ context.Context, _ string, call core.ToolCall) core.ToolExecution {
	path, _ := call.Arguments["path"].(string)
	return core.ToolExecution{ToolCallID: call.ID, Status: core.StatusSucceeded, ExitCode: 0, Output: map[string]any{"path": path}}
}

type artifactCaptureStub struct {
	calls int
	err   error
}

func (capture *artifactCaptureStub) Capture(_ context.Context, _ string, path, kind string) (application.ArtifactCaptureResult, error) {
	capture.calls++
	if capture.err != nil {
		return application.ArtifactCaptureResult{}, capture.err
	}
	return application.ArtifactCaptureResult{Path: path, Kind: kind, ContentHash: "sha256:test", StorageKey: "test", SizeBytes: 1}, nil
}

func TestInternalWorkspaceWriteSkipsArtifactCaptureWithoutFailing(t *testing.T) {
	capture := &artifactCaptureStub{err: errors.New("internal workspace files cannot be captured")}
	runtime := ToolRuntime{workspace: workspaceExecutionStub{}, artifactCapture: capture}
	call := core.ToolCall{ID: "call-1", Name: core.ToolWorkspaceWriteFile, Arguments: map[string]any{
		"path": ".oktapod/work/run-1/server.js", "content": "server",
	}}
	execution := runtime.dispatchWorkspace(context.Background(), core.RunContext{ProjectID: "project-1"}, call, core.PlanTool(call))
	if execution.Status != core.StatusSucceeded || execution.ErrorCode != "" {
		t.Fatalf("internal write was misclassified as failed: %#v", execution)
	}
	if capture.calls != 0 || execution.Output["artifact_capture_skipped"] != "internal_workspace_file" {
		t.Fatalf("capture_calls=%d output=%#v", capture.calls, execution.Output)
	}
}

func TestDeliverableWorkspaceWriteStillRequiresArtifactCapture(t *testing.T) {
	capture := &artifactCaptureStub{err: errors.New("capture failed")}
	runtime := ToolRuntime{workspace: workspaceExecutionStub{}, artifactCapture: capture}
	call := core.ToolCall{ID: "call-2", Name: core.ToolWorkspaceWriteFile, Arguments: map[string]any{
		"path": "outputs/page.html", "content": "page",
	}}
	execution := runtime.dispatchWorkspace(context.Background(), core.RunContext{ProjectID: "project-1"}, call, core.PlanTool(call))
	if execution.Status != core.StatusFailed || execution.ErrorCode != "ARTIFACT_CAPTURE_FAILED" || capture.calls != 1 {
		t.Fatalf("deliverable capture contract changed: %#v calls=%d", execution, capture.calls)
	}
}
