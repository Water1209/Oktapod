package application

import (
	"context"
	"io"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type DocumentExecutor interface {
	Execute(ctx context.Context, run core.RunContext, call core.ToolCall) core.ToolExecution
	Health(ctx context.Context) DocumentRuntimeHealth
}

type DocumentPreview struct {
	Name        string
	MediaType   string
	SizeBytes   int64
	ContentHash string
	Content     io.ReadCloser
}

type DocumentPreviewOpener interface {
	OpenPreview(ctx context.Context, scope core.Scope, runID, documentID, previewName string) (DocumentPreview, error)
}

type DocumentRuntimeHealth struct {
	Available      bool     `json:"available"`
	Version        string   `json:"version"`
	Protocol       string   `json:"protocol"`
	Renderer       string   `json:"renderer"`
	OCRLanguages   []string `json:"ocr_languages"`
	DegradedReason string   `json:"degraded_reason,omitempty"`
}

type DocumentStateRepository interface {
	GetDocumentSession(ctx context.Context, scope core.Scope, runID, documentID string) (core.DocumentSession, error)
	GetDocumentSessionByOpenKey(ctx context.Context, scope core.Scope, runID, openKey string) (core.DocumentSession, error)
	CreateDocumentSession(ctx context.Context, scope core.Scope, session core.DocumentSession, initial core.DocumentRevision) (core.DocumentSession, error)
	AdvanceDocumentRevision(ctx context.Context, scope core.Scope, documentID string, expectedRevision int64, next core.DocumentRevision) (core.DocumentSession, bool, error)
	MarkDocumentRendered(ctx context.Context, scope core.Scope, documentID string, expectedRevision int64, renderHash, manifestJSON, warningsJSON string) (core.DocumentSession, error)
}

type DocumentSourceRepository interface {
	OpenAssetContent(ctx context.Context, scope core.Scope, assetID string) (core.Asset, io.ReadCloser, error)
	OpenArtifactContent(ctx context.Context, scope core.Scope, artifactID string) (core.Artifact, io.ReadCloser, error)
}

type DocumentWorkspace interface {
	WorkspaceCaptureReader
	WriteDocumentOutput(ctx context.Context, projectID, runID, name string, content io.Reader, maxBytes int64) (string, error)
}

func (s *Service) OpenDocumentPreview(ctx context.Context, runID, documentID, previewName string) (DocumentPreview, error) {
	opener, ok := s.documentRuntime.(DocumentPreviewOpener)
	if !ok || opener == nil {
		return DocumentPreview{}, &core.ResourceConflictError{Resource: "document preview", Reason: "document runtime is unavailable"}
	}
	return opener.OpenPreview(ctx, s.ScopeFor(ctx), runID, documentID, previewName)
}
