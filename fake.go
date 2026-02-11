package main

import (
	"bytes"
	"io"
	"sync"

	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/store"
)

// fakeStore
type fakeStore struct {
	mu     sync.RWMutex
	chunks map[desync.ChunkID][]byte
}

func (*fakeStore) Close() error   { return nil }
func (*fakeStore) String() string { return "store" }

func newFakeStore() *fakeStore {
	return &fakeStore{
		chunks: map[desync.ChunkID][]byte{},
	}
}

func (s *fakeStore) GetChunk(id desync.ChunkID) (*desync.Chunk, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	found, ok := s.chunks[id]
	if !ok {
		return nil, desync.ChunkMissing{ID: id}
	}
	return desync.NewChunk(found), nil
}

func (s *fakeStore) HasChunk(id desync.ChunkID) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.chunks[id]
	return ok, nil
}

func (s *fakeStore) StoreChunk(chunk *desync.Chunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := chunk.Data()
	if err != nil {
		return err
	}
	s.chunks[chunk.ID()] = data
	return nil
}

// fakeIndex
type fakeIndex struct {
	mu      sync.RWMutex
	indices map[string][]byte
}

func newFakeIndex() *fakeIndex {
	return &fakeIndex{indices: map[string][]byte{}}
}

func (*fakeIndex) Close() error   { return nil }
func (*fakeIndex) String() string { return "index" }

func (s *fakeIndex) StoreIndex(id string, index desync.Index) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := &bytes.Buffer{}
	if _, err := index.WriteTo(buf); err != nil {
		return err
	}
	s.indices[id] = buf.Bytes()
	return nil
}

func (s *fakeIndex) GetIndex(id string) (i desync.Index, e error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, err := s.getIndexReaderLocked(id)
	if err != nil {
		return i, err
	}
	defer func() { _ = f.Close() }()
	return desync.IndexFromReader(f)
}

func (s *fakeIndex) GetIndexReader(id string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getIndexReaderLocked(id)
}

func (s *fakeIndex) getIndexReaderLocked(id string) (io.ReadCloser, error) {
	idx, ok := s.indices[id]
	if ok {
		return io.NopCloser(bytes.NewBuffer(idx)), nil
	}
	return nil, store.ErrIndexNotFound
}

// discardStore accepts chunks but doesn't retain them. Zero memory growth,
// isolates chunking cost from storage overhead in benchmarks.
type discardStore struct{}

func (discardStore) GetChunk(id desync.ChunkID) (*desync.Chunk, error) {
	return nil, desync.ChunkMissing{ID: id}
}
func (discardStore) HasChunk(desync.ChunkID) (bool, error) { return false, nil }
func (discardStore) StoreChunk(*desync.Chunk) error        { return nil }
func (discardStore) Close() error                          { return nil }
func (discardStore) String() string                        { return "discard" }

// errorFakeIndex wraps fakeIndex, forcing GetIndex/GetIndexReader to return a custom error.
type errorFakeIndex struct {
	*fakeIndex
	getError error
}

func (e *errorFakeIndex) GetIndex(id string) (desync.Index, error) {
	if e.getError != nil {
		return desync.Index{}, e.getError
	}
	return e.fakeIndex.GetIndex(id)
}

func (e *errorFakeIndex) GetIndexReader(id string) (io.ReadCloser, error) {
	if e.getError != nil {
		return nil, e.getError
	}
	return e.fakeIndex.GetIndexReader(id)
}

func (e *errorFakeIndex) Close() error   { return e.fakeIndex.Close() }
func (e *errorFakeIndex) String() string { return e.fakeIndex.String() }
