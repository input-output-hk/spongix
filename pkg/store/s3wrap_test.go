package store

import (
	"errors"
	"io"
	"testing"

	"github.com/folbricht/desync"
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
