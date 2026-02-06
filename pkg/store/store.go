// Package store implements a Pebble-backed content-addressed store
// that satisfies the desync.Store, desync.WriteStore, desync.IndexStore,
// and desync.IndexWriteStore interfaces.
//
// Key schema:
//
//	c/<chunkID>                  -> chunk bytes
//	i/<indexPath>                -> serialized desync.Index
//	r/<storePathHash>            -> newline-separated storePathHashes (narinfo References)
//	cr/<chunkID>/<indexPath>     -> {} (reverse: chunk -> which indices use it)
package store

import (
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/folbricht/desync"
)

var _ desync.WriteStore = (*ChunkStore)(nil)

// DB wraps a Pebble database used for chunk and index storage.
type DB struct {
	db *pebble.DB
}

// Open opens or creates a Pebble database at the given directory.
func Open(dir string) (*DB, error) {
	db, err := pebble.Open(dir, &pebble.Options{
		Levels: []pebble.LevelOptions{{
			FilterPolicy: bloom.FilterPolicy(10),
		}},
	})
	if err != nil {
		return nil, fmt.Errorf("opening pebble db: %w", err)
	}
	return &DB{db: db}, nil
}

// Close closes the underlying Pebble database.
func (d *DB) Close() error {
	return d.db.Close()
}

// chunkKey returns the Pebble key for a chunk: c/<chunkID hex>
func chunkKey(id desync.ChunkID) []byte {
	hex := id.String()
	key := make([]byte, 2+len(hex))
	key[0] = 'c'
	key[1] = '/'
	copy(key[2:], hex)
	return key
}

// ChunkStore implements desync.WriteStore on top of Pebble.
type ChunkStore struct {
	db *DB
}

// NewChunkStore creates a new ChunkStore backed by the given DB.
func NewChunkStore(db *DB) *ChunkStore {
	return &ChunkStore{db: db}
}

func (s *ChunkStore) GetChunk(id desync.ChunkID) (*desync.Chunk, error) {
	key := chunkKey(id)
	val, closer, err := s.db.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, desync.ChunkMissing{ID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("getting chunk %s: %w", id, err)
	}
	// Copy the value since closer will invalidate it.
	data := make([]byte, len(val))
	copy(data, val)
	_ = closer.Close()
	return desync.NewChunk(data), nil
}

func (s *ChunkStore) HasChunk(id desync.ChunkID) (bool, error) {
	key := chunkKey(id)
	_, closer, err := s.db.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking chunk %s: %w", id, err)
	}
	_ = closer.Close()
	return true, nil
}

func (s *ChunkStore) StoreChunk(chunk *desync.Chunk) error {
	data, err := chunk.Data()
	if err != nil {
		return fmt.Errorf("reading chunk data: %w", err)
	}
	key := chunkKey(chunk.ID())
	return s.db.db.Set(key, data, pebble.NoSync)
}

func (s *ChunkStore) Close() error {
	return nil // DB lifecycle managed by DB.Close()
}

func (s *ChunkStore) String() string {
	return "pebble-chunk-store"
}
