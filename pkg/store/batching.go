package store

import (
	"fmt"
	"sync"

	"github.com/cockroachdb/pebble"
	"github.com/folbricht/desync"
)

// BatchingChunkStore implements desync.WriteStore by buffering chunks in memory
// during ChunkStream, then writing everything atomically via a Pebble Batch.
// When buffered data exceeds maxBytes, the batcher auto-flushes buffered chunks
// directly to Pebble and switches to unbatched mode for the remainder.
type BatchingChunkStore struct {
	db            *DB
	mu            sync.RWMutex
	chunks        map[desync.ChunkID][]byte
	bufferedBytes uint64
	maxBytes      uint64
	flushed       bool
}

var _ desync.WriteStore = (*BatchingChunkStore)(nil)

// NewBatchingChunkStore creates a new BatchingChunkStore that buffers up to maxBytes
// of chunk data before flushing to Pebble.
func NewBatchingChunkStore(db *DB, maxBytes uint64) *BatchingChunkStore {
	return &BatchingChunkStore{
		db:       db,
		chunks:   make(map[desync.ChunkID][]byte),
		maxBytes: maxBytes,
	}
}

func (b *BatchingChunkStore) StoreChunk(chunk *desync.Chunk) error {
	data, err := chunk.Data()
	if err != nil {
		return fmt.Errorf("reading chunk data: %w", err)
	}
	id := chunk.ID()

	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.flushed && b.bufferedBytes+uint64(len(data)) <= b.maxBytes {
		// Buffer the chunk in memory.
		if _, exists := b.chunks[id]; !exists {
			cp := make([]byte, len(data))
			copy(cp, data)
			b.chunks[id] = cp
			b.bufferedBytes += uint64(len(data))
		}
		return nil
	}

	// Flush buffered chunks to Pebble if not already flushed.
	if !b.flushed {
		for cid, cdata := range b.chunks {
			if err := b.db.db.Set(chunkKey(cid), cdata, pebble.NoSync); err != nil {
				return fmt.Errorf("flushing chunk %s: %w", cid, err)
			}
		}
		b.chunks = nil
		b.flushed = true
	}

	// Write this chunk directly to Pebble.
	return b.db.db.Set(chunkKey(id), data, pebble.NoSync)
}

func (b *BatchingChunkStore) HasChunk(id desync.ChunkID) (bool, error) {
	b.mu.RLock()
	if !b.flushed {
		if _, ok := b.chunks[id]; ok {
			b.mu.RUnlock()
			return true, nil
		}
	}
	b.mu.RUnlock()

	// Fall through to Pebble.
	key := chunkKey(id)
	_, closer, err := b.db.db.Get(key)
	if err == pebble.ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking chunk %s: %w", id, err)
	}
	_ = closer.Close()
	return true, nil
}

func (b *BatchingChunkStore) GetChunk(id desync.ChunkID) (*desync.Chunk, error) {
	b.mu.RLock()
	if !b.flushed {
		if data, ok := b.chunks[id]; ok {
			b.mu.RUnlock()
			cp := make([]byte, len(data))
			copy(cp, data)
			return desync.NewChunk(cp), nil
		}
	}
	b.mu.RUnlock()

	// Fall through to Pebble.
	key := chunkKey(id)
	val, closer, err := b.db.db.Get(key)
	if err == pebble.ErrNotFound {
		return nil, desync.ChunkMissing{ID: id}
	}
	if err != nil {
		return nil, fmt.Errorf("getting chunk %s: %w", id, err)
	}
	data := make([]byte, len(val))
	copy(data, val)
	_ = closer.Close()
	return desync.NewChunk(data), nil
}

func (b *BatchingChunkStore) Close() error {
	return nil // batcher does not own the shared DB
}

func (b *BatchingChunkStore) String() string {
	return "BatchingChunkStore"
}

// CommitWithIndex atomically writes buffered chunks and index entries to Pebble.
// If chunks were already flushed (exceeded maxBytes), only index entries are written.
func (b *BatchingChunkStore) CommitWithIndex(name string, idx desync.Index, narRefs []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	batch := b.db.db.NewBatch()
	defer func() { _ = batch.Close() }()

	if !b.flushed {
		// Atomic path: write all buffered chunks + index entries in one batch.
		for id, data := range b.chunks {
			_ = batch.Set(chunkKey(id), data, nil)
		}
	}

	if err := addIndexEntries(batch, name, idx, narRefs); err != nil {
		return err
	}

	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("committing batch: %w", err)
	}

	b.chunks = nil
	return nil
}
