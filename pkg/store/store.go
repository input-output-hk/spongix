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
	"os"
	"path/filepath"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
	"github.com/folbricht/desync"
)

var _ desync.WriteStore = (*ChunkStore)(nil)

// DB wraps a Pebble database used for chunk and index storage.
type DB struct {
	db *pebble.DB
}

// PebbleOptions configures the Pebble database.
type PebbleOptions struct {
	WALDir             string // separate WAL directory (e.g. SSD)
	MemTableSize       uint64 // default 64MB if 0
	L0CompactionThresh int    // default 4 if 0
}

// Open opens or creates a Pebble database at the given directory.
func Open(dir string, opts *PebbleOptions) (*DB, error) {
	if opts == nil {
		opts = &PebbleOptions{}
	}
	if opts.MemTableSize == 0 {
		opts.MemTableSize = 64 << 20
	}
	if opts.L0CompactionThresh == 0 {
		opts.L0CompactionThresh = 4
	}

	pOpts := &pebble.Options{
		MemTableSize:          opts.MemTableSize,
		L0CompactionThreshold: opts.L0CompactionThresh,
		Levels: []pebble.LevelOptions{{
			FilterPolicy: bloom.FilterPolicy(10),
		}},
	}
	if opts.WALDir != "" {
		pOpts.WALDir = opts.WALDir
	}

	db, err := pebble.Open(dir, pOpts)
	if err != nil && opts.WALDir != "" && errors.Is(err, os.ErrNotExist) {
		// Separate WAL dir (e.g. tmpfs) was likely wiped on reboot.
		// Create empty placeholders for missing WAL files so pebble
		// can replay them as truncated, preserving all flushed SST data.
		db, err = recoverMissingWAL(dir, pOpts)
	}
	if err != nil {
		return nil, fmt.Errorf("opening pebble db: %w", err)
	}
	return &DB{db: db}, nil
}

// recoverMissingWAL retries pebble.Open, creating empty placeholder files
// for any WAL files that pebble expects but can't find (e.g. after a tmpfs
// WAL directory is wiped on reboot). Empty WAL files are treated as truncated
// during replay, so all flushed SST data is preserved.
func recoverMissingWAL(dir string, pOpts *pebble.Options) (*pebble.DB, error) {
	for range 10 {
		db, err := pebble.Open(dir, pOpts)
		if err == nil {
			return db, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		// Extract the missing path from the error chain.
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			return nil, err
		}
		missing := pathErr.Path
		if !strings.HasPrefix(missing, pOpts.WALDir+string(filepath.Separator)) {
			return nil, err
		}
		f, createErr := os.Create(missing)
		if createErr != nil {
			return nil, fmt.Errorf("creating WAL placeholder %s: %w (original: %v)", missing, createErr, err)
		}
		_ = f.Close()
	}
	return nil, fmt.Errorf("too many missing WAL files in %s", pOpts.WALDir)
}

// Flush flushes the current memtable to L0 SSTables and syncs.
// Call before Close when the WAL is on volatile storage (e.g. tmpfs).
func (d *DB) Flush() error {
	return d.db.Flush()
}

// Close closes the underlying Pebble database.
func (d *DB) Close() error {
	return d.db.Close()
}

// NewBatch creates a new Pebble write batch.
func (d *DB) NewBatch() *pebble.Batch { return d.db.NewBatch() }

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
