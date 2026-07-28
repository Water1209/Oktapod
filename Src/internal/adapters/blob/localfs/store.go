package localfs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type Store struct {
	root string
}

func New(root string) (*Store, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve blob root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create blob root: %w", err)
	}
	return &Store{root: absolute}, nil
}

func (s *Store) Put(_ context.Context, content io.Reader, mediaType string, maxBytes int64) (application.BlobDescriptor, error) {
	temporary, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return application.BlobDescriptor{}, err
	}
	temporaryPath := temporary.Name()
	keepTemporary := true
	defer func() {
		if keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	hasher := sha256.New()
	limit := maxBytes + 1
	if maxBytes < 0 {
		limit = 1<<63 - 1
	}
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(content, limit))
	if err != nil {
		_ = temporary.Close()
		return application.BlobDescriptor{}, err
	}
	if maxBytes >= 0 && written > maxBytes {
		_ = temporary.Close()
		return application.BlobDescriptor{}, &core.ValidationError{Field: "file", Message: fmt.Sprintf("must not exceed %d bytes", maxBytes)}
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return application.BlobDescriptor{}, err
	}
	if err := temporary.Close(); err != nil {
		return application.BlobDescriptor{}, err
	}

	hexHash := fmt.Sprintf("%x", hasher.Sum(nil))
	storageKey := filepath.Join("sha256", hexHash[:2], hexHash)
	target, err := s.resolve(storageKey)
	if err != nil {
		return application.BlobDescriptor{}, err
	}
	if info, statErr := os.Stat(target); statErr == nil {
		_ = os.Remove(temporaryPath)
		keepTemporary = false
		return application.BlobDescriptor{ContentHash: "sha256:" + hexHash, StorageKey: filepath.ToSlash(storageKey), SizeBytes: info.Size(), MediaType: mediaType}, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return application.BlobDescriptor{}, statErr
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return application.BlobDescriptor{}, err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		if _, statErr := os.Stat(target); statErr != nil {
			return application.BlobDescriptor{}, err
		}
		_ = os.Remove(temporaryPath)
	}
	keepTemporary = false
	return application.BlobDescriptor{ContentHash: "sha256:" + hexHash, StorageKey: filepath.ToSlash(storageKey), SizeBytes: written, MediaType: mediaType}, nil
}

func (s *Store) Open(_ context.Context, storageKey string, expectedSize int64) (io.ReadCloser, error) {
	path, err := s.resolve(filepath.FromSlash(storageKey))
	if err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if expectedSize >= 0 && info.Size() != expectedSize {
		_ = file.Close()
		return nil, fmt.Errorf("blob size mismatch: expected %d, got %d", expectedSize, info.Size())
	}
	return file, nil
}

func (s *Store) resolve(storageKey string) (string, error) {
	if filepath.IsAbs(storageKey) {
		return "", fmt.Errorf("absolute blob key is not allowed")
	}
	target := filepath.Clean(filepath.Join(s.root, storageKey))
	relative, err := filepath.Rel(s.root, target)
	if err != nil || relative == ".." || filepath.IsAbs(relative) || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("blob key escapes root")
	}
	return target, nil
}
