package application

import (
	"context"
	"io"
	"strings"
	"time"

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

// FilePreviewSource names an already-authorized product file without exposing
// an absolute host path to the browser or the document runtime.
type FilePreviewSource struct {
	ProjectID string
	Kind      string
	Reference string
}

type FilePreviewSession struct {
	ID        string
	Name      string
	Format    string
	PageNames []string
	Warnings  []string
	ExpiresAt time.Time
}

// FilePreviewer is the read-only document-runtime boundary used by the Files
// inspector. Preview sessions are short-lived capabilities: page requests must
// still match the user and organization that created the session.
type FilePreviewer interface {
	CreateFilePreview(ctx context.Context, scope core.Scope, source FilePreviewSource) (FilePreviewSession, error)
	OpenFilePreview(ctx context.Context, scope core.Scope, previewID, previewName string) (DocumentPreview, error)
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

func (s *Service) CreateFilePreview(ctx context.Context, source FilePreviewSource) (FilePreviewSession, error) {
	source.ProjectID = strings.TrimSpace(source.ProjectID)
	source.Kind = strings.ToLower(strings.TrimSpace(source.Kind))
	source.Reference = strings.TrimSpace(source.Reference)
	if source.ProjectID == "" {
		return FilePreviewSession{}, &core.ValidationError{Field: "project_id", Message: "is required"}
	}
	if source.Kind != "workspace" && source.Kind != "asset" && source.Kind != "artifact" {
		return FilePreviewSession{}, &core.ValidationError{Field: "source_kind", Message: "must be workspace, asset, or artifact"}
	}
	if source.Reference == "" {
		return FilePreviewSession{}, &core.ValidationError{Field: "reference", Message: "is required"}
	}
	if _, err := s.repository.GetProject(ctx, s.ScopeFor(ctx), source.ProjectID); err != nil {
		return FilePreviewSession{}, err
	}
	previewer, ok := s.documentRuntime.(FilePreviewer)
	if !ok || previewer == nil {
		return FilePreviewSession{}, &core.ResourceConflictError{Resource: "file preview", Reason: "document runtime is unavailable"}
	}
	return previewer.CreateFilePreview(ctx, s.ScopeFor(ctx), source)
}

func (s *Service) OpenFilePreview(ctx context.Context, previewID, previewName string) (DocumentPreview, error) {
	previewer, ok := s.documentRuntime.(FilePreviewer)
	if !ok || previewer == nil {
		return DocumentPreview{}, &core.ResourceConflictError{Resource: "file preview", Reason: "document runtime is unavailable"}
	}
	return previewer.OpenFilePreview(ctx, s.ScopeFor(ctx), previewID, previewName)
}
