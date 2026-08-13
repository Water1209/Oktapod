package application

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
)

const MaxArtifactCaptureBytes int64 = 100 * 1024 * 1024

type WorkspaceCaptureFile interface {
	io.ReadCloser
	RelativePath() string
	SizeBytes() int64
	VerifyUnchanged(expectedContentHash string) error
}

type WorkspaceCaptureReader interface {
	OpenCaptureFile(ctx context.Context, projectID, relativePath string, maxBytes int64) (WorkspaceCaptureFile, error)
}

type ArtifactCaptureResult struct {
	Path        string
	Kind        string
	MediaType   string
	ContentHash string
	StorageKey  string
	SizeBytes   int64
}

type ArtifactCapture interface {
	Capture(ctx context.Context, projectID, relativePath, kind string) (ArtifactCaptureResult, error)
}

type ArtifactCaptureService struct {
	workspace WorkspaceCaptureReader
	blobs     BlobStore
}

func NewArtifactCaptureService(workspace WorkspaceCaptureReader, blobs BlobStore) (*ArtifactCaptureService, error) {
	if workspace == nil || blobs == nil {
		return nil, fmt.Errorf("workspace reader and blob store are required")
	}
	return &ArtifactCaptureService{workspace: workspace, blobs: blobs}, nil
}

func (s *ArtifactCaptureService) Capture(ctx context.Context, projectID, relativePath, kind string) (ArtifactCaptureResult, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "deliverable" && kind != "evidence" {
		return ArtifactCaptureResult{}, fmt.Errorf("artifact kind must be deliverable or evidence")
	}
	clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relativePath)))
	if clean == "." || clean == "" || isInternalWorkspacePath(clean) {
		return ArtifactCaptureResult{}, fmt.Errorf("internal workspace files cannot be captured")
	}
	source, err := s.workspace.OpenCaptureFile(ctx, projectID, clean, MaxArtifactCaptureBytes)
	if err != nil {
		return ArtifactCaptureResult{}, err
	}
	defer source.Close()
	reader := bufio.NewReaderSize(source, 4096)
	prefix, peekErr := reader.Peek(512)
	if peekErr != nil && !errors.Is(peekErr, io.EOF) && !errors.Is(peekErr, bufio.ErrBufferFull) {
		return ArtifactCaptureResult{}, fmt.Errorf("inspect artifact signature: %w", peekErr)
	}
	mediaType, err := DetectArtifactMediaType(clean, prefix)
	if err != nil {
		return ArtifactCaptureResult{}, err
	}
	descriptor, err := s.blobs.Put(ctx, reader, mediaType, MaxArtifactCaptureBytes)
	if err != nil {
		return ArtifactCaptureResult{}, err
	}
	if descriptor.SizeBytes != source.SizeBytes() {
		return ArtifactCaptureResult{}, fmt.Errorf("artifact changed while it was captured: expected %d bytes, captured %d", source.SizeBytes(), descriptor.SizeBytes)
	}
	if err := source.VerifyUnchanged(descriptor.ContentHash); err != nil {
		return ArtifactCaptureResult{}, err
	}
	return ArtifactCaptureResult{
		Path: source.RelativePath(), Kind: kind, MediaType: descriptor.MediaType,
		ContentHash: descriptor.ContentHash, StorageKey: descriptor.StorageKey, SizeBytes: descriptor.SizeBytes,
	}, nil
}

func isInternalWorkspacePath(path string) bool {
	return path == ".oktapod" || strings.HasPrefix(path, ".oktapod/") || path == ".git" || strings.HasPrefix(path, ".git/")
}

// DetectArtifactMediaType is the single MIME decision point for workspace
// deliverables. PDF is signature-gated because an extension alone is not
// evidence that a conversion succeeded.
func DetectArtifactMediaType(name string, prefix []byte) (string, error) {
	extension := strings.ToLower(filepath.Ext(name))
	isPDF := len(prefix) >= len("%PDF-") && string(prefix[:len("%PDF-")]) == "%PDF-"
	if extension == ".pdf" && !isPDF {
		return "", fmt.Errorf("PDF_SIGNATURE_INVALID: %s does not start with %%PDF-", name)
	}
	if isPDF {
		return "application/pdf", nil
	}
	detected := http.DetectContentType(prefix)
	if strings.HasPrefix(detected, "image/png") || strings.HasPrefix(detected, "image/jpeg") || strings.HasPrefix(detected, "image/gif") || strings.HasPrefix(detected, "image/webp") {
		return strings.Split(detected, ";")[0], nil
	}
	switch extension {
	case ".md", ".markdown":
		return "text/markdown; charset=utf-8", nil
	case ".txt", ".log":
		return "text/plain; charset=utf-8", nil
	case ".json":
		return "application/json", nil
	case ".csv":
		return "text/csv; charset=utf-8", nil
	case ".html", ".htm":
		return "text/html; charset=utf-8", nil
	case ".png":
		return "image/png", nil
	case ".jpg", ".jpeg":
		return "image/jpeg", nil
	case ".gif":
		return "image/gif", nil
	case ".webp":
		return "image/webp", nil
	default:
		return detected, nil
	}
}
