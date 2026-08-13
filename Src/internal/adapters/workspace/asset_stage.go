package workspace

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oktapod/oktapod/Src/internal/application"
)

var stagedFileNameCleaner = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func (l *Local) StageAsset(ctx context.Context, projectID, runID, assetID, name string, content io.Reader, expectedSize int64, expectedHash string) (application.AssetStageResult, error) {
	if _, err := uuid.Parse(runID); err != nil {
		return application.AssetStageResult{}, fmt.Errorf("run id is not a UUID")
	}
	if _, err := uuid.Parse(assetID); err != nil {
		return application.AssetStageResult{}, fmt.Errorf("asset id is not a UUID")
	}
	if expectedSize < 0 || expectedSize > application.MaxArtifactCaptureBytes {
		return application.AssetStageResult{}, fmt.Errorf("asset exceeds staging limit")
	}
	root, err := l.projectRoot(projectID)
	if err != nil {
		return application.AssetStageResult{}, err
	}
	_ = l.cleanupStaleInputs(root, time.Now().UTC().Add(-24*time.Hour))
	directory := filepath.Join(root, ".oktapod", "inputs", runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return application.AssetStageResult{}, err
	}
	base := stagedFileNameCleaner.ReplaceAllString(filepath.Base(strings.TrimSpace(name)), "-")
	base = strings.Trim(base, ".-")
	if base == "" {
		base = "attachment"
	}
	target := filepath.Join(directory, assetID+"-"+base)
	temporary, err := os.CreateTemp(directory, ".stage-*")
	if err != nil {
		return application.AssetStageResult{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hasher), io.LimitReader(content, expectedSize+1))
	if err != nil {
		_ = temporary.Close()
		return application.AssetStageResult{}, err
	}
	if written != expectedSize {
		_ = temporary.Close()
		return application.AssetStageResult{}, fmt.Errorf("staged asset size mismatch")
	}
	actualHash := fmt.Sprintf("sha256:%x", hasher.Sum(nil))
	if actualHash != expectedHash {
		_ = temporary.Close()
		return application.AssetStageResult{}, fmt.Errorf("staged asset hash mismatch")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return application.AssetStageResult{}, err
	}
	if err := temporary.Close(); err != nil {
		return application.AssetStageResult{}, err
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		if existingHash, verifyErr := hashExistingStagedFile(target, expectedSize); verifyErr != nil || existingHash != expectedHash {
			return application.AssetStageResult{}, err
		}
	}
	if err := os.Chmod(target, 0o444); err != nil {
		return application.AssetStageResult{}, err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return application.AssetStageResult{}, err
	}
	select {
	case <-ctx.Done():
		return application.AssetStageResult{}, ctx.Err()
	default:
	}
	return application.AssetStageResult{Path: filepath.ToSlash(relative), ContentHash: actualHash, SizeBytes: written}, nil
}

func hashExistingStagedFile(path string, expectedSize int64) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != expectedSize {
		return "", fmt.Errorf("existing staged asset is not the expected regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", hasher.Sum(nil)), nil
}

func (l *Local) CleanupRunInputs(_ context.Context, projectID, runID string) error {
	if _, err := uuid.Parse(runID); err != nil {
		return fmt.Errorf("run id is not a UUID")
	}
	root, err := l.projectRoot(projectID)
	if err != nil {
		return err
	}
	target := filepath.Join(root, ".oktapod", "inputs", runID)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("input cleanup escaped workspace")
	}
	return os.RemoveAll(target)
}

func (l *Local) cleanupStaleInputs(root string, olderThan time.Time) error {
	inputs := filepath.Join(root, ".oktapod", "inputs")
	entries, err := os.ReadDir(inputs)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err == nil && info.ModTime().Before(olderThan) {
			_ = os.RemoveAll(filepath.Join(inputs, entry.Name()))
		}
	}
	return nil
}
