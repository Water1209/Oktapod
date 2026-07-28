package application

import (
	"context"
	"io"
)

type BlobDescriptor struct {
	ContentHash string
	StorageKey  string
	SizeBytes   int64
	MediaType   string
}

// BlobStore owns immutable file bytes. Repository implementations retain
// metadata, authorization, references, audit, and transaction boundaries.
type BlobStore interface {
	Put(ctx context.Context, content io.Reader, mediaType string, maxBytes int64) (BlobDescriptor, error)
	Open(ctx context.Context, storageKey string, expectedSize int64) (io.ReadCloser, error)
}
