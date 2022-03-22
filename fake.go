package main

import (
	"bytes"
	"io"
	"os"

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
		err = errors.Errorf("Index file does not exist: %v", err)
	}
	return idx, err
}

func (s fakeIndex) GetIndexReader(id string) (io.ReadCloser, error) {
	idx, ok := s.indices[id]
	if ok {
		return io.NopCloser(bytes.NewBuffer(idx)), nil
	}
	return nil, os.ErrNotExist
}
