package main

import (
	"bytes"
	"io"
	"os"
	"time"

	"github.com/folbricht/desync"
	"github.com/pkg/errors"
)

// fakeStore
type fakeStore struct {
	chunks map[desync.ChunkID][]byte
}

func (s fakeStore) Close() error   { return nil }
func (s fakeStore) String() string { return "store" }

func newFakeStore() *fakeStore {
	return &fakeStore{
		chunks: map[desync.ChunkID][]byte{},
	}
}

func (s fakeStore) GetChunk(id desync.ChunkID) (*desync.Chunk, error) {
	found, ok := s.chunks[id]
	if !ok {
		return nil, desync.ChunkMissing{ID: id}
	}
	return desync.NewChunk(found), nil
}

func (s fakeStore) HasChunk(id desync.ChunkID) (bool, error) {
	_, ok := s.chunks[id]
	return ok, nil
}

func (s *fakeStore) StoreChunk(chunk *desync.Chunk) error {
	data, err := chunk.Data()
	if err != nil {
		return err
	}
	s.chunks[chunk.ID()] = data
	return nil
}

// fakeIndex
type fakeIndex struct {
	indices map[string][]byte
}

func newFakeIndex() *fakeIndex {
	return &fakeIndex{indices: map[string][]byte{}}
}

func (s fakeIndex) Close() error   { return nil }
func (s fakeIndex) String() string { return "index" }

func (s fakeIndex) StoreIndex(id string, index desync.Index) error {
	buf := &bytes.Buffer{}
	if _, err := index.WriteTo(buf); err != nil {
		return err
	}
	s.indices[id] = buf.Bytes()
	return nil
}

func (s fakeIndex) GetIndex(id string) (i desync.Index, e error) {
	f, err := s.GetIndexReader(id)
	if err != nil {
		return i, err
	}
	defer f.Close()
	idx, err := desync.IndexFromReader(f)
	if os.IsNotExist(err) {
		err = errors.New("reading index: The specified key does not exist.")
	}
	return idx, err
}

func (s fakeIndex) GetIndexReader(id string) (io.ReadCloser, error) {
	idx, ok := s.indices[id]
	if ok {
		return io.NopCloser(bytes.NewBuffer(idx)), nil
	}
	return nil, errors.New("reading index: The specified key does not exist.")
}

// slowFakeStore wraps fakeStore with a configurable delay per StoreChunk call.
type slowFakeStore struct {
	*fakeStore
	chunkDelay time.Duration
}

func (s *slowFakeStore) StoreChunk(chunk *desync.Chunk) error {
	time.Sleep(s.chunkDelay)
	return s.fakeStore.StoreChunk(chunk)
}

func (s *slowFakeStore) Close() error   { return s.fakeStore.Close() }
func (s *slowFakeStore) String() string { return s.fakeStore.String() }

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
