package application

import (
	"context"
	"io"
)

type AssetStageResult struct {
	Path        string
	ContentHash string
	SizeBytes   int64
}

type AssetStager interface {
	StageAsset(ctx context.Context, projectID, runID, assetID, name string, content io.Reader, expectedSize int64, expectedHash string) (AssetStageResult, error)
	CleanupRunInputs(ctx context.Context, projectID, runID string) error
}
