package store

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/folbricht/desync"
)

func tempDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestChunkStoreRoundTrip(t *testing.T) {
	db := tempDB(t)
	store := NewChunkStore(db)

	data := []byte("hello, pebble chunks!")
	chunk := desync.NewChunk(data)
	id := chunk.ID()

	// Store
	if err := store.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	// Has
	has, err := store.HasChunk(id)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("expected HasChunk to return true")
	}

	// Get
	got, err := store.GetChunk(id)
	if err != nil {
		t.Fatal(err)
	}
	gotData, err := got.Data()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotData, data) {
		t.Fatalf("got %q, want %q", gotData, data)
	}
}

func TestChunkStoreMissing(t *testing.T) {
	db := tempDB(t)
	store := NewChunkStore(db)

	id := desync.ChunkID{} // zero ID, not stored

	has, err := store.HasChunk(id)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("expected HasChunk to return false for missing chunk")
	}

	_, err = store.GetChunk(id)
	if _, ok := err.(desync.ChunkMissing); !ok {
		t.Fatalf("expected ChunkMissing, got %T: %v", err, err)
	}
}

func TestChunkStoreDedup(t *testing.T) {
	db := tempDB(t)
	store := NewChunkStore(db)

	data := []byte("dedup test data")
	chunk1 := desync.NewChunk(data)
	chunk2 := desync.NewChunk(data) // same content = same ID

	if chunk1.ID() != chunk2.ID() {
		t.Fatal("expected same IDs for same content")
	}

	if err := store.StoreChunk(chunk1); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreChunk(chunk2); err != nil {
		t.Fatal(err)
	}

	// Should still be retrievable
	got, err := store.GetChunk(chunk1.ID())
	if err != nil {
		t.Fatal(err)
	}
	gotData, _ := got.Data()
	if !bytes.Equal(gotData, data) {
		t.Fatal("data mismatch after dedup write")
	}
}

func TestIndexStoreRoundTrip(t *testing.T) {
	db := tempDB(t)
	chunkStore := NewChunkStore(db)
	indexStore := NewIndexStore(db, "test")

	// Create a chunk and an index referencing it.
	data := []byte("index test data")
	chunk := desync.NewChunk(data)
	if err := chunkStore.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	idx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: desync.CaFormatExcludeNoDump | desync.CaFormatSHA512256,
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: chunk.ID(), Start: 0, Size: uint64(len(data))},
		},
	}

	name := "indices/narinfo/inde/indextest"
	if err := indexStore.StoreIndex(name, idx); err != nil {
		t.Fatal(err)
	}

	// Get the index back.
	got, err := indexStore.GetIndex(name)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(got.Chunks))
	}
	if got.Chunks[0].ID != chunk.ID() {
		t.Fatal("chunk ID mismatch")
	}
}

func TestIndexStoreReader(t *testing.T) {
	db := tempDB(t)
	indexStore := NewIndexStore(db, "test")

	idx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: 0,
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
	}

	name := "indices/nar/test/testreaderhash"
	if err := indexStore.StoreIndex(name, idx); err != nil {
		t.Fatal(err)
	}

	r, err := indexStore.GetIndexReader(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty data from reader")
	}
}

func TestIndexStoreMissing(t *testing.T) {
	db := tempDB(t)
	indexStore := NewIndexStore(db, "test")

	_, err := indexStore.GetIndex("indices/narinfo/miss/missing")
	if err == nil {
		t.Fatal("expected error for missing index")
	}
}

func TestStoreIndexWithRefs(t *testing.T) {
	db := tempDB(t)
	chunkStore := NewChunkStore(db)
	indexStore := NewIndexStore(db, "test")
	refs := NewRefTracker(db)

	data := []byte("ref tracking test data")
	chunk := desync.NewChunk(data)
	if err := chunkStore.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	idx := desync.Index{
		Index: desync.FormatIndex{
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: chunk.ID(), Start: 0, Size: uint64(len(data))},
		},
	}

	name := "indices/narinfo/hyhr/hyhrnrnpsz9fw5p9dk85a58y31ink18c"
	narRefs := []string{"5b4cprjhjw35wyzvgmgvqay4hjf59h7x"}

	if err := indexStore.StoreIndexWithRefs(name, idx, narRefs); err != nil {
		t.Fatal(err)
	}

	// Check chunk reference count
	count, err := refs.GetChunkRefCount(chunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 chunk ref, got %d", count)
	}

	// Check NAR refs
	gotRefs, err := refs.GetNARRefs("hyhrnrnpsz9fw5p9dk85a58y31ink18c")
	if err != nil {
		t.Fatal(err)
	}
	if len(gotRefs) != 1 || gotRefs[0] != "5b4cprjhjw35wyzvgmgvqay4hjf59h7x" {
		t.Fatalf("expected [5b4cprjhjw35wyzvgmgvqay4hjf59h7x], got %v", gotRefs)
	}
}

func TestRefTrackerDeleteChunkRefsForIndex(t *testing.T) {
	db := tempDB(t)
	cs := NewChunkStore(db)
	is := NewIndexStore(db, "test")
	refs := NewRefTracker(db)

	data := []byte("delete refs test")
	chunk := desync.NewChunk(data)
	if err := cs.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	idx := desync.Index{
		Index: desync.FormatIndex{
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: chunk.ID(), Start: 0, Size: uint64(len(data))},
		},
	}
	indexPath := "indices/nar/test/testhash"
	if err := is.StoreIndexWithRefs(indexPath, idx, nil); err != nil {
		t.Fatal(err)
	}

	// Verify ref exists
	count, err := refs.GetChunkRefCount(chunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 ref, got %d", count)
	}

	if err := refs.DeleteChunkRefsForIndex(indexPath, []desync.ChunkID{chunk.ID()}); err != nil {
		t.Fatal(err)
	}

	count, err = refs.GetChunkRefCount(chunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected 0 refs after delete, got %d", count)
	}
}

func TestStoreIndexWritesCREntries(t *testing.T) {
	db := tempDB(t)
	cs := NewChunkStore(db)
	is := NewIndexStore(db, "test")
	refs := NewRefTracker(db)

	data := []byte("cr entry test")
	chunk := desync.NewChunk(data)
	if err := cs.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}

	idx := desync.Index{
		Index: desync.FormatIndex{
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: chunk.ID(), Start: 0, Size: uint64(len(data))},
		},
	}

	// Use StoreIndex (NOT StoreIndexWithRefs) - should still write cr/ entries
	name := "indices/nar/test/testhash123"
	if err := is.StoreIndex(name, idx); err != nil {
		t.Fatal(err)
	}

	// Verify cr/ entries exist
	count, err := refs.GetChunkRefCount(chunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 chunk ref from StoreIndex, got %d", count)
	}
}

func TestIndexStoreErrorContract(t *testing.T) {
	db := tempDB(t)
	is := NewIndexStore(db, "test")

	_, err := is.GetIndex("nonexistent")
	if !errors.Is(err, ErrIndexNotFound) {
		t.Fatalf("expected ErrIndexNotFound, got %v", err)
	}

	_, err = is.GetIndexReader("nonexistent")
	if !errors.Is(err, ErrIndexNotFound) {
		t.Fatalf("expected ErrIndexNotFound from GetIndexReader, got %v", err)
	}
}
