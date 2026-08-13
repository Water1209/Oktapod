package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"
)

type captureTestFile struct {
	*bytes.Reader
	path      string
	verifyErr error
}

func (f *captureTestFile) Close() error         { return nil }
func (f *captureTestFile) RelativePath() string { return f.path }
func (f *captureTestFile) SizeBytes() int64     { return int64(f.Len()) + int64(f.Size()-int64(f.Len())) }
func (f *captureTestFile) VerifyUnchanged(_ string) error {
	return f.verifyErr
}

type captureTestWorkspace struct {
	body      []byte
	verifyErr error
}

func (w captureTestWorkspace) OpenCaptureFile(_ context.Context, _ string, path string, _ int64) (WorkspaceCaptureFile, error) {
	return &captureTestFile{Reader: bytes.NewReader(w.body), path: path, verifyErr: w.verifyErr}, nil
}

type captureTestBlobStore struct{}

func (captureTestBlobStore) Put(_ context.Context, content io.Reader, mediaType string, _ int64) (BlobDescriptor, error) {
	body, err := io.ReadAll(content)
	if err != nil {
		return BlobDescriptor{}, err
	}
	sum := sha256.Sum256(body)
	digest := fmt.Sprintf("%x", sum[:])
	return BlobDescriptor{ContentHash: "sha256:" + digest, StorageKey: "sha256/" + digest[:2] + "/" + digest, SizeBytes: int64(len(body)), MediaType: mediaType}, nil
}

func (captureTestBlobStore) Open(context.Context, string, int64) (io.ReadCloser, error) {
	return nil, fmt.Errorf("not implemented")
}

func TestArtifactCaptureValidatesPDFAndProducesDescriptor(t *testing.T) {
	service, err := NewArtifactCaptureService(captureTestWorkspace{body: []byte("%PDF-1.7\n%%EOF")}, captureTestBlobStore{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Capture(t.Context(), "project", "report.pdf", "deliverable")
	if err != nil {
		t.Fatal(err)
	}
	if result.MediaType != "application/pdf" || !strings.HasPrefix(result.ContentHash, "sha256:") || result.Path != "report.pdf" {
		t.Fatalf("capture=%#v", result)
	}
}

func TestArtifactCaptureRejectsFakePDFAndChangedFile(t *testing.T) {
	service, _ := NewArtifactCaptureService(captureTestWorkspace{body: []byte("not a pdf")}, captureTestBlobStore{})
	if _, err := service.Capture(t.Context(), "project", "report.pdf", "deliverable"); err == nil || !strings.Contains(err.Error(), "PDF_SIGNATURE_INVALID") {
		t.Fatalf("fake PDF error=%v", err)
	}
	service, _ = NewArtifactCaptureService(captureTestWorkspace{body: []byte("%PDF-1.7"), verifyErr: fmt.Errorf("changed")}, captureTestBlobStore{})
	if _, err := service.Capture(t.Context(), "project", "report.pdf", "deliverable"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed file error=%v", err)
	}
}

func TestArtifactCaptureRejectsInternalWorkspaceMetadata(t *testing.T) {
	service, _ := NewArtifactCaptureService(captureTestWorkspace{body: []byte("internal")}, captureTestBlobStore{})
	for _, path := range []string{".oktapod/inputs/run/secret.txt", ".git/config"} {
		if _, err := service.Capture(t.Context(), "project", path, "evidence"); err == nil || !strings.Contains(err.Error(), "internal workspace") {
			t.Fatalf("path=%s error=%v", path, err)
		}
	}
}
