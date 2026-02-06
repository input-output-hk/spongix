package store

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/folbricht/desync"
	minio "github.com/minio/minio-go/v6"
)

// S3IndexStoreWrapper wraps a desync.S3IndexStore to normalize errors.
// S3 index stores return S3-specific errors for missing keys; this wrapper
// maps those to ErrIndexNotFound for consistent error handling.
type S3IndexStoreWrapper struct {
	inner desync.IndexWriteStore
}

// WrapS3IndexStore wraps an S3 index store to normalize not-found errors.
func WrapS3IndexStore(s desync.IndexWriteStore) *S3IndexStoreWrapper {
	return &S3IndexStoreWrapper{inner: s}
}

func (w *S3IndexStoreWrapper) GetIndex(name string) (desync.Index, error) {
	idx, err := w.inner.GetIndex(name)
	if err != nil && isS3NotFound(err) {
		return idx, fmt.Errorf("%w: %s", ErrIndexNotFound, name)
	}
	return idx, err
}

func (w *S3IndexStoreWrapper) GetIndexReader(name string) (io.ReadCloser, error) {
	r, err := w.inner.GetIndexReader(name)
	if err != nil && isS3NotFound(err) {
		return nil, fmt.Errorf("%w: %s", ErrIndexNotFound, name)
	}
	return r, err
}

func (w *S3IndexStoreWrapper) StoreIndex(name string, idx desync.Index) error {
	return w.inner.StoreIndex(name, idx)
}

func (w *S3IndexStoreWrapper) Close() error {
	return w.inner.Close()
}

func (w *S3IndexStoreWrapper) String() string {
	return w.inner.String()
}

// isS3NotFound checks if an error is an S3 "key not found" error.
// It uses type-based detection via minio.ErrorResponse, with a string
// fallback for wrapped errors that lose the type.
func isS3NotFound(err error) bool {
	var resp minio.ErrorResponse
	if errors.As(err, &resp) {
		return resp.Code == "NoSuchKey" || resp.StatusCode == 404
	}
	// Fallback for wrapped errors that lose the type
	return strings.Contains(err.Error(), "does not exist")
}
