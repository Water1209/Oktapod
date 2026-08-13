package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/application"
)

type captureFile struct {
	file         *os.File
	absolutePath string
	relativePath string
	sizeBytes    int64
	modifiedNS   int64
}

func (l *Local) OpenCaptureFile(_ context.Context, projectID, relativePath string, maxBytes int64) (application.WorkspaceCaptureFile, error) {
	root, err := l.projectRoot(projectID)
	if err != nil {
		return nil, err
	}
	path, err := secureExistingPath(root, relativePath, false)
	if err != nil {
		return nil, err
	}
	relative := relativeSlash(root, path)
	if relative == ".oktapod" || strings.HasPrefix(relative, ".oktapod/") {
		return nil, fmt.Errorf("internal workspace files cannot be captured")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, fmt.Errorf("artifact must be one regular file no larger than %d bytes", maxBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &captureFile{file: file, absolutePath: path, relativePath: filepath.ToSlash(relative), sizeBytes: info.Size(), modifiedNS: info.ModTime().UnixNano()}, nil
}

func (f *captureFile) Read(body []byte) (int, error) { return f.file.Read(body) }
func (f *captureFile) Close() error                  { return f.file.Close() }
func (f *captureFile) RelativePath() string          { return f.relativePath }
func (f *captureFile) SizeBytes() int64              { return f.sizeBytes }

func (f *captureFile) VerifyUnchanged(expectedContentHash string) error {
	info, err := os.Lstat(f.absolutePath)
	if err != nil {
		return fmt.Errorf("verify captured artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != f.sizeBytes || info.ModTime().UnixNano() != f.modifiedNS {
		return fmt.Errorf("artifact changed while it was captured")
	}
	reopened, err := os.Open(f.absolutePath)
	if err != nil {
		return fmt.Errorf("verify captured artifact: %w", err)
	}
	defer reopened.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, reopened); err != nil {
		return fmt.Errorf("verify captured artifact: %w", err)
	}
	actual := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	if actual != expectedContentHash {
		return fmt.Errorf("artifact changed while it was captured: expected %s, found %s", expectedContentHash, actual)
	}
	return nil
}
