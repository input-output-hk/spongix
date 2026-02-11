package store

import (
	"bytes"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble"
	"github.com/folbricht/desync"
)

func makeBatchTestIndex(chunks ...*desync.Chunk) desync.Index {
	idx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: desync.CaFormatExcludeNoDump | desync.CaFormatSHA512256,
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
	}
	var offset uint64
	for _, c := range chunks {
		data, _ := c.Data()
		idx.Chunks = append(idx.Chunks, desync.IndexChunk{
			ID:    c.ID(),
			Start: offset,
			Size:  uint64(len(data)),
		})
		offset += uint64(len(data))
	}
	return idx
}

func TestBatchingRoundTrip(t *testing.T) {
	db := tempDB(t)
	batcher := NewBatchingChunkStore(db, 256<<20)

	data := []byte("batching round trip test data")
	chunk := desync.NewChunk(data)
	if err := batcher.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	idx := makeBatchTestIndex(chunk)
	name := "indices/narinfo/test/testroundtrip"
	if err := batcher.CommitWithIndex(name, idx, nil); err != nil {
		t.Fatal(err)
	}

	// Verify chunk exists in Pebble (c/ key).
	key := chunkKey(chunk.ID())
	val, closer, err := db.db.Get(key)
	if err != nil {
		t.Fatalf("chunk not found in Pebble: %v", err)
	}
	if !bytes.Equal(val, data) {
		t.Fatal("chunk data mismatch")
	}
	_ = closer.Close()

	// Verify index exists (i/ key).
	iKey := indexKey(name)
	_, closer, err = db.db.Get(iKey)
	if err != nil {
		t.Fatalf("index not found in Pebble: %v", err)
	}
	_ = closer.Close()

	// Verify timestamp exists (t/ key).
	tKey := timestampKey(name)
	_, closer, err = db.db.Get(tKey)
	if err != nil {
		t.Fatalf("timestamp not found in Pebble: %v", err)
	}
	_ = closer.Close()

	// Verify chunk ref exists (cr/ key).
	crKey := chunkRefKey(chunk.ID(), name)
	_, closer, err = db.db.Get(crKey)
	if err != nil {
		t.Fatalf("chunk ref not found in Pebble: %v", err)
	}
	_ = closer.Close()
}

func TestBatchingHasChunkBuffered(t *testing.T) {
	db := tempDB(t)
	batcher := NewBatchingChunkStore(db, 256<<20)

	data := []byte("has chunk test")
	chunk := desync.NewChunk(data)
	if err := batcher.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	// Should find chunk in buffer.
	has, err := batcher.HasChunk(chunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected HasChunk to return true for buffered chunk")
	}

	// Should also find chunks already in Pebble.
	preData := []byte("pre-existing chunk")
	preChunk := desync.NewChunk(preData)
	if err := db.db.Set(chunkKey(preChunk.ID()), preData, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	has, err = batcher.HasChunk(preChunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected HasChunk to return true for Pebble chunk")
	}
}

func TestBatchingGetChunkBuffered(t *testing.T) {
	db := tempDB(t)
	batcher := NewBatchingChunkStore(db, 256<<20)

	data := []byte("get chunk test data")
	chunk := desync.NewChunk(data)
	if err := batcher.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	got, err := batcher.GetChunk(chunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	gotData, _ := got.Data()
	if !bytes.Equal(gotData, data) {
		t.Fatalf("got %q, want %q", gotData, data)
	}
}

func TestBatchingNARRefs(t *testing.T) {
	db := tempDB(t)
	batcher := NewBatchingChunkStore(db, 256<<20)
	refs := NewRefTracker(db)

	data := []byte("nar ref test data")
	chunk := desync.NewChunk(data)
	if err := batcher.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	idx := makeBatchTestIndex(chunk)
	name := "indices/narinfo/hyhr/hyhrnrnpsz9fw5p9dk85a58y31ink18c"
	narRefs := []string{"5b4cprjhjw35wyzvgmgvqay4hjf59h7x"}
	if err := batcher.CommitWithIndex(name, idx, narRefs); err != nil {
		t.Fatal(err)
	}

	gotRefs, err := refs.GetNARRefs("hyhrnrnpsz9fw5p9dk85a58y31ink18c")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotRefs) != 1 || gotRefs[0] != "5b4cprjhjw35wyzvgmgvqay4hjf59h7x" {
		t.Fatalf("expected [5b4cprjhjw35wyzvgmgvqay4hjf59h7x], got %v", gotRefs)
	}
}

func TestBatchingThreadSafety(t *testing.T) {
	db := tempDB(t)
	batcher := NewBatchingChunkStore(db, 256<<20)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			data := []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
			chunk := desync.NewChunk(data)
			if err := batcher.StoreChunk(chunk); err != nil {
				t.Errorf("StoreChunk(%d): %v", n, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestBatchingStaysBuffered(t *testing.T) {
	db := tempDB(t)
	batcher := NewBatchingChunkStore(db, 256<<20) // large limit

	data := []byte("should stay buffered")
	chunk := desync.NewChunk(data)
	if err := batcher.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	// Chunk should NOT be in Pebble yet.
	_, _, err := db.db.Get(chunkKey(chunk.ID()))
	if err != pebble.ErrNotFound {
		t.Fatal("expected chunk to NOT be in Pebble before commit")
	}

	// After commit, chunk should be in Pebble.
	idx := makeBatchTestIndex(chunk)
	if err := batcher.CommitWithIndex("indices/nar/test/buffered", idx, nil); err != nil {
		t.Fatal(err)
	}

	_, closer, err := db.db.Get(chunkKey(chunk.ID()))
	if err != nil {
		t.Fatal("expected chunk to be in Pebble after commit")
	}
	_ = closer.Close()
}

func TestBatchingTriggersFlush(t *testing.T) {
	db := tempDB(t)
	batcher := NewBatchingChunkStore(db, 1) // tiny limit: flush immediately

	data := []byte("should trigger flush")
	chunk := desync.NewChunk(data)
	if err := batcher.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	// Chunk should already be in Pebble (flushed).
	_, closer, err := db.db.Get(chunkKey(chunk.ID()))
	if err != nil {
		t.Fatalf("expected chunk to be in Pebble after flush: %v", err)
	}
	_ = closer.Close()

	// CommitWithIndex should only write index entries.
	idx := makeBatchTestIndex(chunk)
	if err := batcher.CommitWithIndex("indices/nar/test/flushed", idx, nil); err != nil {
		t.Fatal(err)
	}

	// Verify index entries exist.
	iKey := indexKey("indices/nar/test/flushed")
	_, closer, err = db.db.Get(iKey)
	if err != nil {
		t.Fatal("index not found after flush commit")
	}
	_ = closer.Close()
}

func TestBatchingMidStreamTransition(t *testing.T) {
	db := tempDB(t)
	// Set maxBytes so first chunk fits but second doesn't.
	batcher := NewBatchingChunkStore(db, 30)

	data1 := []byte("first chunk fits in buffer")   // 26 bytes
	data2 := []byte("second chunk causes a flush!") // 28 bytes - total > 30

	chunk1 := desync.NewChunk(data1)
	chunk2 := desync.NewChunk(data2)

	if err := batcher.StoreChunk(chunk1); err != nil {
		t.Fatal(err)
	}
	// chunk1 should be buffered, not yet in Pebble.
	_, _, err := db.db.Get(chunkKey(chunk1.ID()))
	if err != pebble.ErrNotFound {
		t.Fatal("expected chunk1 to be buffered, not in Pebble")
	}

	if err := batcher.StoreChunk(chunk2); err != nil {
		t.Fatal(err)
	}
	// After flush, both chunks should be in Pebble.
	_, closer, err := db.db.Get(chunkKey(chunk1.ID()))
	if err != nil {
		t.Fatal("expected chunk1 in Pebble after flush")
	}
	_ = closer.Close()

	_, closer, err = db.db.Get(chunkKey(chunk2.ID()))
	if err != nil {
		t.Fatal("expected chunk2 in Pebble after flush")
	}
	_ = closer.Close()

	// CommitWithIndex should still work correctly.
	idx := makeBatchTestIndex(chunk1, chunk2)
	if err := batcher.CommitWithIndex("indices/nar/test/midstream", idx, nil); err != nil {
		t.Fatal(err)
	}
}
