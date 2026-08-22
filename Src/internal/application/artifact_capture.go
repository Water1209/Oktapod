package application

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
	var descriptor BlobDescriptor
	if isOOXMLMediaType(mediaType) {
		temporary, tempErr := os.CreateTemp("", "oktapod-ooxml-capture-*")
		if tempErr != nil {
			return ArtifactCaptureResult{}, tempErr
		}
		temporaryPath := temporary.Name()
		defer os.Remove(temporaryPath)
		written, copyErr := io.Copy(temporary, io.LimitReader(reader, MaxArtifactCaptureBytes+1))
		if copyErr != nil {
			_ = temporary.Close()
			return ArtifactCaptureResult{}, copyErr
		}
		if written > MaxArtifactCaptureBytes {
			_ = temporary.Close()
			return ArtifactCaptureResult{}, fmt.Errorf("OOXML artifact exceeds %d bytes", MaxArtifactCaptureBytes)
		}
		if _, seekErr := temporary.Seek(0, io.SeekStart); seekErr != nil {
			_ = temporary.Close()
			return ArtifactCaptureResult{}, seekErr
		}
		if validateErr := validateOOXMLArchive(temporary, written, mediaType); validateErr != nil {
			_ = temporary.Close()
			return ArtifactCaptureResult{}, validateErr
		}
		if _, seekErr := temporary.Seek(0, io.SeekStart); seekErr != nil {
			_ = temporary.Close()
			return ArtifactCaptureResult{}, seekErr
		}
		descriptor, err = s.blobs.Put(ctx, temporary, mediaType, MaxArtifactCaptureBytes)
		_ = temporary.Close()
	} else {
		descriptor, err = s.blobs.Put(ctx, reader, mediaType, MaxArtifactCaptureBytes)
	}
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
	isZIP := len(prefix) >= 4 && prefix[0] == 'P' && prefix[1] == 'K' && (prefix[2] == 3 || prefix[2] == 5 || prefix[2] == 7) && (prefix[3] == 4 || prefix[3] == 6 || prefix[3] == 8)
	if extension == ".pptx" || extension == ".docx" || extension == ".xlsx" {
		if !isZIP {
			return "", fmt.Errorf("OOXML_SIGNATURE_INVALID: %s is not a ZIP package", name)
		}
		switch extension {
		case ".pptx":
			return "application/vnd.openxmlformats-officedocument.presentationml.presentation", nil
		case ".docx":
			return "application/vnd.openxmlformats-officedocument.wordprocessingml.document", nil
		default:
			return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
		}
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

func isOOXMLMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "application/vnd.openxmlformats-officedocument.")
}

func validateOOXMLArchive(reader io.ReaderAt, size int64, mediaType string) error {
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return fmt.Errorf("OOXML_ZIP_INVALID: %w", err)
	}
	if len(archive.File) > 20_000 {
		return fmt.Errorf("OOXML_ZIP_BOMB: package contains too many entries")
	}
	required := map[string]bool{"[Content_Types].xml": false}
	switch mediaType {
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		required["ppt/presentation.xml"] = false
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		required["word/document.xml"] = false
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		required["xl/workbook.xml"] = false
	}
	var expanded uint64
	for _, file := range archive.File {
		clean := filepath.ToSlash(filepath.Clean(file.Name))
		if clean == "." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || strings.Contains(file.Name, "\\") || file.Flags&0x1 != 0 {
			return fmt.Errorf("OOXML_PATH_INVALID: package contains an unsafe or encrypted entry")
		}
		expanded += file.UncompressedSize64
		if expanded > 512<<20 || (file.CompressedSize64 > 0 && file.UncompressedSize64/file.CompressedSize64 > 2000) {
			return fmt.Errorf("OOXML_ZIP_BOMB: expanded size or compression ratio exceeds the limit")
		}
		if _, ok := required[file.Name]; ok {
			required[file.Name] = true
		}
		lower := strings.ToLower(file.Name)
		if strings.HasSuffix(lower, "vbaproject.bin") || strings.HasSuffix(lower, ".xla") {
			return fmt.Errorf("DOCUMENT_MACRO_UNSUPPORTED: macro-enabled output cannot be captured")
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("OOXML_PART_MISSING: %s", name)
		}
	}
	return nil
}
