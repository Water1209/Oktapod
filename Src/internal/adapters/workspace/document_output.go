package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// WriteDocumentOutput is the only binary write port exposed to the document
// runtime adapter. It is create-only, project scoped, and content-idempotent.
func (l *Local) WriteDocumentOutput(_ context.Context, projectID, runID, name string, content io.Reader, maxBytes int64) (string, error) {
	if _, err := uuid.Parse(runID); err != nil {
		return "", fmt.Errorf("run id is not a UUID")
	}
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\x00/\\") {
		return "", fmt.Errorf("document output name must be one safe file name")
	}
	root, err := l.projectRoot(projectID)
	if err != nil {
		return "", err
	}
	relative := filepath.Join("outputs", runID, name)
	parent, err := secureParent(root, filepath.Dir(relative))
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, name)
	temporary, err := os.CreateTemp(parent, ".oktapod-document-export-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(content, maxBytes+1))
	if copyErr != nil {
		_ = temporary.Close()
		return "", copyErr
	}
	if written > maxBytes {
		_ = temporary.Close()
		return "", fmt.Errorf("document output exceeds %d bytes", maxBytes)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	contentHash := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("document output target is not a regular file")
		}
		existing, readErr := os.Open(target)
		if readErr != nil {
			return "", readErr
		}
		existingHasher := sha256.New()
		_, readErr = io.Copy(existingHasher, io.LimitReader(existing, maxBytes+1))
		_ = existing.Close()
		if readErr != nil {
			return "", readErr
		}
		if fmt.Sprintf("sha256:%x", existingHasher.Sum(nil)) == contentHash {
			return filepath.ToSlash(relative), nil
		}
		return "", fmt.Errorf("document output already exists with different content")
	} else if !os.IsNotExist(statErr) {
		return "", statErr
	}
	if err := os.Link(temporaryPath, target); err != nil {
		return "", fmt.Errorf("document output appeared before atomic create completed: %w", err)
	}
	return filepath.ToSlash(relative), nil
}
