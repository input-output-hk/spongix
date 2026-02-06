package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/folbricht/desync"
)

func setupGCTestDB(t *testing.T) (*DB, *ChunkStore, *IndexStore) {
	t.Helper()
	db := tempDB(t)
	cs := NewChunkStore(db)
	is := NewIndexStore(db, "test")
	return db, cs, is
}

// defaultFeatureFlags for test indices — desync requires CaFormatSHA512256.
const testFeatureFlags = desync.CaFormatExcludeNoDump | desync.CaFormatSHA512256

func makeTestIndex(t *testing.T, cs *ChunkStore, is *IndexStore, hash string, data []byte, narRefs []string) desync.ChunkID {
	t.Helper()
	chunk := desync.NewChunk(data)
	if err := cs.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}
	idx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: testFeatureFlags,
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: chunk.ID(), Start: 0, Size: uint64(len(data))},
		},
	}
	prefix := hash
	if len(hash) > 4 {
		prefix = hash[0:4]
	}
	name := "indices/narinfo/" + prefix + "/" + hash

	if err := is.StoreIndexWithRefs(name, idx, narRefs); err != nil {
		t.Fatal(err)
	}
	return chunk.ID()
}

func TestGCPreservesReachable(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create two narinfos: A references B
	makeTestIndex(t, cs, is, "aaaa1111aaaa1111aaaa1111aaaa1111", []byte("data A"), []string{"bbbb2222bbbb2222bbbb2222bbbb2222"})
	chunkB := makeTestIndex(t, cs, is, "bbbb2222bbbb2222bbbb2222bbbb2222", []byte("data B"), nil)

	// A is pinned (root), B is referenced by A's r/ entry → both reachable
	stats, err := GC(db, GCConfig{
		PinnedHashes: map[string]struct{}{
			"aaaa1111aaaa1111aaaa1111aaaa1111": {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both should be reachable (A is pinned root, B is referenced by A)
	if stats.DeletedIndices != 0 {
		t.Errorf("expected 0 deleted indices, got %d", stats.DeletedIndices)
	}
	if stats.DeletedChunks != 0 {
		t.Errorf("expected 0 deleted chunks, got %d", stats.DeletedChunks)
	}

	// Verify chunk B still exists
	has, err := cs.HasChunk(chunkB)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("chunk B should still exist (reachable via A's references)")
	}
}

func TestGCDeletesOrphanChunks(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create a narinfo with a chunk
	makeTestIndex(t, cs, is, "aaaa1111aaaa1111aaaa1111aaaa1111", []byte("data A"), nil)

	// Create an orphan chunk (not referenced by any index)
	orphanData := []byte("orphan chunk data")
	orphanChunk := desync.NewChunk(orphanData)
	if err := cs.StoreChunk(orphanChunk); err != nil {
		t.Fatal(err)
	}

	// Pin the narinfo so it stays; orphan chunk still gets deleted
	stats, err := GC(db, GCConfig{
		PinnedHashes: map[string]struct{}{
			"aaaa1111aaaa1111aaaa1111aaaa1111": {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.OrphanChunks != 1 {
		t.Errorf("expected 1 orphan chunk, got %d", stats.OrphanChunks)
	}
	if stats.DeletedChunks != 1 {
		t.Errorf("expected 1 deleted chunk, got %d", stats.DeletedChunks)
	}

	// Orphan chunk should be gone
	has, err := cs.HasChunk(orphanChunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("orphan chunk should have been deleted")
	}
}

func TestGCDryRun(t *testing.T) {
	db, cs, _ := setupGCTestDB(t)

	// Create an orphan chunk
	orphanData := []byte("orphan for dry run")
	orphanChunk := desync.NewChunk(orphanData)
	if err := cs.StoreChunk(orphanChunk); err != nil {
		t.Fatal(err)
	}

	stats, err := GC(db, GCConfig{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}

	if stats.OrphanChunks != 1 {
		t.Errorf("expected 1 orphan found, got %d", stats.OrphanChunks)
	}
	if stats.DeletedChunks != 0 {
		t.Errorf("expected 0 deleted (dry run), got %d", stats.DeletedChunks)
	}

	// Chunk should still exist
	has, err := cs.HasChunk(orphanChunk.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("chunk should still exist in dry run mode")
	}
}

func TestGCTransitiveClosure(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create a chain: A -> B -> C
	makeTestIndex(t, cs, is, "aaaa1111aaaa1111aaaa1111aaaa1111", []byte("data A"),
		[]string{"bbbb2222bbbb2222bbbb2222bbbb2222"})
	makeTestIndex(t, cs, is, "bbbb2222bbbb2222bbbb2222bbbb2222", []byte("data B"),
		[]string{"cccc3333cccc3333cccc3333cccc3333"})
	chunkC := makeTestIndex(t, cs, is, "cccc3333cccc3333cccc3333cccc3333", []byte("data C"), nil)

	// A is pinned, B reachable via A→B, C reachable via B→C
	stats, err := GC(db, GCConfig{
		PinnedHashes: map[string]struct{}{
			"aaaa1111aaaa1111aaaa1111aaaa1111": {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// All three should be reachable through transitive closure
	if stats.ReachableNarinfos != 3 {
		t.Errorf("expected 3 reachable, got %d", stats.ReachableNarinfos)
	}
	if stats.DeletedIndices != 0 {
		t.Errorf("expected 0 deleted, got %d", stats.DeletedIndices)
	}

	// C's chunk should still exist
	has, err := cs.HasChunk(chunkC)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("chunk C should still exist (reachable via A -> B -> C)")
	}
}

func TestGCEmptyDB(t *testing.T) {
	db := tempDB(t)

	stats, err := GC(db, GCConfig{})
	if err != nil {
		t.Fatal(err)
	}

	if stats.ReachableNarinfos != 0 || stats.DeletedIndices != 0 || stats.DeletedChunks != 0 {
		t.Errorf("expected all zeros for empty DB, got %+v", stats)
	}
}

func TestGCNonNarinfoIndicesPreserved(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create a narinfo that references a NAR index.
	// The narinfo is pinned, so the NAR's chunks survive GC via cr/ refs.
	narData := []byte("narinfo data")
	narChunk := desync.NewChunk(narData)
	if err := cs.StoreChunk(narChunk); err != nil {
		t.Fatal(err)
	}
	narinfoIdx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: testFeatureFlags,
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: narChunk.ID(), Start: 0, Size: uint64(len(narData))},
		},
	}
	narinfoName := "indices/narinfo/1h6m/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp"
	if err := is.StoreIndexWithRefs(narinfoName, narinfoIdx, nil); err != nil {
		t.Fatal(err)
	}

	// Create a companion NAR index sharing the same store path hash
	data := []byte("nar data")
	chunk := desync.NewChunk(data)
	if err := cs.StoreChunk(chunk); err != nil {
		t.Fatal(err)
	}
	narIdx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: testFeatureFlags,
			ChunkSizeMin: 1024,
			ChunkSizeAvg: 4096,
			ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: chunk.ID(), Start: 0, Size: uint64(len(data))},
		},
	}
	narName := "indices/nar/1h6m/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp"
	if err := is.StoreIndexWithRefs(narName, narIdx, nil); err != nil {
		t.Fatal(err)
	}

	stats, err := GC(db, GCConfig{
		PinnedHashes: map[string]struct{}{
			"1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp": {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The narinfo is pinned, so it and its chunks survive.
	if stats.ReachableNarinfos != 1 {
		t.Errorf("expected 1 reachable narinfo, got %d", stats.ReachableNarinfos)
	}

	// The NAR index should still exist — same store path hash is reachable.
	_, err = is.GetIndex(narName)
	if err != nil {
		t.Fatalf("NAR index should still exist: %v", err)
	}
}

func TestGCCorruptIndex(t *testing.T) {
	db := tempDB(t)
	cs := NewChunkStore(db)
	is := NewIndexStore(db, "test")

	// Create a normal narinfo and pin it
	makeTestIndex(t, cs, is, "aaaa1111aaaa1111aaaa1111aaaa1111", []byte("good data"), nil)

	// Write a corrupt index directly to Pebble (bypassing IndexStore)
	corruptKey := indexKey("indices/narinfo/baad/baad2222baad2222baad2222baad2222")
	err := db.db.Set(corruptKey, []byte("this is not a valid desync index"), pebble.Sync)
	if err != nil {
		t.Fatal(err)
	}

	// Pin the good one; the corrupt one (baad...) is not pinned, so it WILL be deleted.
	stats, err := GC(db, GCConfig{
		PinnedHashes: map[string]struct{}{
			"aaaa1111aaaa1111aaaa1111aaaa1111": {},
		},
	})
	if err != nil {
		t.Fatal("GC should not fail on corrupt indices: ", err)
	}

	// The corrupt index should be deleted (unreachable + corrupt)
	if stats.CorruptIndices != 1 {
		t.Errorf("expected 1 corrupt index, got %d", stats.CorruptIndices)
	}
	if stats.DeletedIndices != 1 {
		t.Errorf("expected 1 deleted index (the corrupt one), got %d", stats.DeletedIndices)
	}

	// The good narinfo should still be intact
	_, err = is.GetIndex("indices/narinfo/aaaa/aaaa1111aaaa1111aaaa1111aaaa1111")
	if err != nil {
		t.Fatal("good index should still exist:", err)
	}

	// The corrupt narinfo should be gone
	_, err = is.GetIndex("indices/narinfo/baad/baad2222baad2222baad2222baad2222")
	if err == nil {
		t.Fatal("corrupt index should have been deleted")
	}
}

func TestGCDeletesUnpinnedNarinfos(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create two narinfos: one pinned, one not
	makeTestIndex(t, cs, is, "aaaa1111aaaa1111aaaa1111aaaa1111", []byte("pinned data"), nil)
	chunkB := makeTestIndex(t, cs, is, "bbbb2222bbbb2222bbbb2222bbbb2222", []byte("unpinned data"), nil)

	stats, err := GC(db, GCConfig{
		PinnedHashes: map[string]struct{}{
			"aaaa1111aaaa1111aaaa1111aaaa1111": {},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.ReachableNarinfos != 1 {
		t.Errorf("expected 1 reachable narinfo, got %d", stats.ReachableNarinfos)
	}
	if stats.DeletedIndices != 1 {
		t.Errorf("expected 1 deleted index (unpinned narinfo), got %d", stats.DeletedIndices)
	}

	// Pinned narinfo should survive
	_, err = is.GetIndex("indices/narinfo/aaaa/aaaa1111aaaa1111aaaa1111aaaa1111")
	if err != nil {
		t.Fatal("pinned narinfo should survive:", err)
	}

	// Unpinned narinfo should be deleted
	_, err = is.GetIndex("indices/narinfo/bbbb/bbbb2222bbbb2222bbbb2222bbbb2222")
	if err == nil {
		t.Fatal("unpinned narinfo should have been deleted")
	}

	// Unpinned narinfo's chunk should also be deleted (orphaned after index deletion)
	has, err := cs.HasChunk(chunkB)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("chunk from unpinned narinfo should have been deleted")
	}
}

func TestGCRetentionKeepsRecent(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create a narinfo (will have a "now" timestamp)
	chunkA := makeTestIndex(t, cs, is, "aaaa1111aaaa1111aaaa1111aaaa1111", []byte("recent data"), nil)

	// GC with 1-hour retention: the just-created narinfo is within retention
	stats, err := GC(db, GCConfig{
		RetentionPeriod: 1 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.ReachableNarinfos != 1 {
		t.Errorf("expected 1 reachable (within retention), got %d", stats.ReachableNarinfos)
	}
	if stats.DeletedIndices != 0 {
		t.Errorf("expected 0 deleted, got %d", stats.DeletedIndices)
	}

	// Narinfo and chunk should survive
	_, err = is.GetIndex("indices/narinfo/aaaa/aaaa1111aaaa1111aaaa1111aaaa1111")
	if err != nil {
		t.Fatal("recent narinfo should survive:", err)
	}
	has, err := cs.HasChunk(chunkA)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("chunk should survive (narinfo within retention)")
	}
}

func TestGCDeletesExpiredNarinfos(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create a narinfo
	chunkA := makeTestIndex(t, cs, is, "aaaa1111aaaa1111aaaa1111aaaa1111", []byte("old data"), nil)

	// Manually backdate its timestamp to 2 hours ago
	indexPath := "indices/narinfo/aaaa/aaaa1111aaaa1111aaaa1111aaaa1111"
	oldTime := time.Now().Add(-2 * time.Hour)
	err := db.db.Set(timestampKey(indexPath), encodeTimestamp(oldTime), pebble.Sync)
	if err != nil {
		t.Fatal(err)
	}

	// GC with 1-hour retention: the narinfo is expired
	stats, err := GC(db, GCConfig{
		RetentionPeriod: 1 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.ReachableNarinfos != 0 {
		t.Errorf("expected 0 reachable (all expired), got %d", stats.ReachableNarinfos)
	}
	if stats.DeletedIndices != 1 {
		t.Errorf("expected 1 deleted, got %d", stats.DeletedIndices)
	}

	// Narinfo should be deleted
	_, err = is.GetIndex(indexPath)
	if err == nil {
		t.Fatal("expired narinfo should have been deleted")
	}

	// Chunk should be orphaned and deleted
	has, err := cs.HasChunk(chunkA)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("chunk from expired narinfo should have been deleted")
	}
}

func TestGCDeletesOrphanNARIndices(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create a narinfo and a companion NAR index sharing the same store path hash
	narData := []byte("narinfo data for orphan test")
	narChunk := desync.NewChunk(narData)
	if err := cs.StoreChunk(narChunk); err != nil {
		t.Fatal(err)
	}
	narinfoIdx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: testFeatureFlags,
			ChunkSizeMin: 1024, ChunkSizeAvg: 4096, ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: narChunk.ID(), Start: 0, Size: uint64(len(narData))},
		},
	}
	hash := "aaaa1111aaaa1111aaaa1111aaaa1111"
	narinfoName := "indices/narinfo/aaaa/" + hash
	if err := is.StoreIndexWithRefs(narinfoName, narinfoIdx, nil); err != nil {
		t.Fatal(err)
	}

	// NAR index with same store path hash
	narIndexData := []byte("nar data for orphan test")
	narIndexChunk := desync.NewChunk(narIndexData)
	if err := cs.StoreChunk(narIndexChunk); err != nil {
		t.Fatal(err)
	}
	narIdx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: testFeatureFlags,
			ChunkSizeMin: 1024, ChunkSizeAvg: 4096, ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: narIndexChunk.ID(), Start: 0, Size: uint64(len(narIndexData))},
		},
	}
	narName := "indices/nar/aaaa/" + hash
	if err := is.StoreIndexWithRefs(narName, narIdx, nil); err != nil {
		t.Fatal(err)
	}

	// GC with no pins and no retention: narinfo is not a root, so BOTH indices are unreachable
	stats, err := GC(db, GCConfig{})
	if err != nil {
		t.Fatal(err)
	}

	// Both should be deleted (narinfo + nar)
	if stats.DeletedIndices != 2 {
		t.Errorf("expected 2 deleted indices (narinfo + nar), got %d", stats.DeletedIndices)
	}

	// Verify both are gone
	_, err = is.GetIndex(narinfoName)
	if err == nil {
		t.Fatal("narinfo should have been deleted")
	}
	_, err = is.GetIndex(narName)
	if err == nil {
		t.Fatal("NAR index should have been deleted (orphaned)")
	}
}

func TestGCPinnedNarinfoPreservesNARIndex(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	hash := "aaaa1111aaaa1111aaaa1111aaaa1111"

	// Create narinfo + NAR with same hash
	makeTestIndex(t, cs, is, hash, []byte("narinfo data"), nil)

	narData := []byte("nar data")
	narChunk := desync.NewChunk(narData)
	if err := cs.StoreChunk(narChunk); err != nil {
		t.Fatal(err)
	}
	narIdx := desync.Index{
		Index: desync.FormatIndex{
			FeatureFlags: testFeatureFlags,
			ChunkSizeMin: 1024, ChunkSizeAvg: 4096, ChunkSizeMax: 16384,
		},
		Chunks: []desync.IndexChunk{
			{ID: narChunk.ID(), Start: 0, Size: uint64(len(narData))},
		},
	}
	narName := "indices/nar/aaaa/" + hash
	if err := is.StoreIndexWithRefs(narName, narIdx, nil); err != nil {
		t.Fatal(err)
	}

	// Pin the narinfo: both narinfo and NAR should survive (same store path hash)
	stats, err := GC(db, GCConfig{
		PinnedHashes: map[string]struct{}{hash: {}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if stats.DeletedIndices != 0 {
		t.Errorf("expected 0 deleted (hash is pinned), got %d", stats.DeletedIndices)
	}

	// Both should still exist
	_, err = is.GetIndex("indices/narinfo/aaaa/" + hash)
	if err != nil {
		t.Fatal("pinned narinfo should exist:", err)
	}
	_, err = is.GetIndex(narName)
	if err != nil {
		t.Fatal("NAR index should exist (same hash as pinned narinfo):", err)
	}
}

func TestGCBatchOverflow(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	// Create >1000 unpinned narinfos to exercise batch commit logic
	for i := 0; i < 1050; i++ {
		hash := fmt.Sprintf("%032x", i) // 32-char hex hash
		data := []byte(fmt.Sprintf("data-%d", i))
		makeTestIndex(t, cs, is, hash, data, nil)
	}

	// GC with no pins: all 1050 should be deleted
	stats, err := GC(db, GCConfig{})
	if err != nil {
		t.Fatal(err)
	}

	if stats.DeletedIndices != 1050 {
		t.Errorf("expected 1050 deleted indices, got %d", stats.DeletedIndices)
	}
	if stats.DeletedChunks != 1050 {
		t.Errorf("expected 1050 deleted chunks, got %d", stats.DeletedChunks)
	}
}
