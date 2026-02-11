package store

import (
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/folbricht/desync"
	minio "github.com/minio/minio-go/v6"
)

// mockS3IndexStore simulates a desync.S3IndexStore that returns S3-style errors.
type mockS3IndexStore struct{}

func (m *mockS3IndexStore) GetIndex(name string) (desync.Index, error) {
	return desync.Index{}, errors.New("reading index: The specified key does not exist.")
}

func (m *mockS3IndexStore) GetIndexReader(name string) (io.ReadCloser, error) {
	return nil, errors.New("reading index: The specified key does not exist.")
}

func (m *mockS3IndexStore) StoreIndex(name string, idx desync.Index) error {
	return nil
}

func (m *mockS3IndexStore) Close() error   { return nil }
func (m *mockS3IndexStore) String() string { return "mock-s3" }

func TestS3WrapperMapsNotFound(t *testing.T) {
	wrapped := WrapS3IndexStore(&mockS3IndexStore{})

	_, err := wrapped.GetIndex("missing")
	if !errors.Is(err, ErrIndexNotFound) {
		t.Fatalf("expected ErrIndexNotFound, got %v", err)
	}

	_, err = wrapped.GetIndexReader("missing")
	if !errors.Is(err, ErrIndexNotFound) {
		t.Fatalf("expected ErrIndexNotFound from GetIndexReader, got %v", err)
	}
}

func TestS3WrapperPassesThroughOtherErrors(t *testing.T) {
	// A store that returns a non-S3 error
	s := &mockErrorStore{err: errors.New("network timeout")}
	wrapped := WrapS3IndexStore(s)

	_, err := wrapped.GetIndex("anything")
	if errors.Is(err, ErrIndexNotFound) {
		t.Fatal("should not map non-S3 errors to ErrIndexNotFound")
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}

type mockErrorStore struct {
	err error
}

func (m *mockErrorStore) GetIndex(name string) (desync.Index, error) {
	return desync.Index{}, m.err
}
func (m *mockErrorStore) GetIndexReader(name string) (io.ReadCloser, error) {
	return nil, m.err
}
func (m *mockErrorStore) StoreIndex(name string, idx desync.Index) error { return nil }
func (m *mockErrorStore) Close() error                                   { return nil }
func (m *mockErrorStore) String() string                                 { return "mock-error" }

func TestS3WrapperMinioErrorResponse(t *testing.T) {
	// Real minio.ErrorResponse wrapped through pkg/errors (as desync does)
	minioErr := minio.ErrorResponse{Code: "NoSuchKey", StatusCode: 404}
	wrappedErr := fmt.Errorf("reading index: %w", minioErr)

	s := &mockErrorStore{err: wrappedErr}
	wrapped := WrapS3IndexStore(s)

	_, err := wrapped.GetIndex("missing")
	if !errors.Is(err, ErrIndexNotFound) {
		t.Fatalf("expected ErrIndexNotFound for NoSuchKey, got %v", err)
	}
}

func TestS3WrapperMinioAccessDeniedNotMapped(t *testing.T) {
	// AccessDenied should NOT be treated as not-found
	minioErr := minio.ErrorResponse{Code: "AccessDenied", StatusCode: 403}
	wrappedErr := fmt.Errorf("reading index: %w", minioErr)

	s := &mockErrorStore{err: wrappedErr}
	wrapped := WrapS3IndexStore(s)

	_, err := wrapped.GetIndex("forbidden")
	if errors.Is(err, ErrIndexNotFound) {
		t.Fatal("AccessDenied should not be mapped to ErrIndexNotFound")
	}
	if err == nil {
		t.Fatal("expected an error")
	}
}
