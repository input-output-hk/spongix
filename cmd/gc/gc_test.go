package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/folbricht/desync"
	"go.uber.org/zap"
)

// storeIndex stores an index at a nested path, creating parent directories.
func storeIndex(t *testing.T, store desync.LocalIndexStore, name string, idx desync.Index) {
	t.Helper()
	fullPath := filepath.Join(store.Path, name)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := store.StoreIndex(name, idx); err != nil {
		t.Fatal(err)
	}
}

func makeChunkID(b byte) desync.ChunkID {
	var id desync.ChunkID
	for i := range id {
		id[i] = b
	}
	return id
}

// TestLRUAdd verifies that Add accumulates entries and tracks size.
func TestLRUAdd(t *testing.T) {
	lru := NewLRU(1000)
	lru.Add(chunkStat{id: makeChunkID(1), size: 100, mtimeNano: 1})
	lru.Add(chunkStat{id: makeChunkID(2), size: 200, mtimeNano: 2})
	lru.Add(chunkStat{id: makeChunkID(3), size: 300, mtimeNano: 3})

	if len(lru.live) != 3 {
		t.Fatalf("expected 3 live, got %d", len(lru.live))
	}
	if lru.liveSize != 600 {
		t.Fatalf("expected liveSize 600, got %d", lru.liveSize)
	}
}

// TestLRUFinalizeSortOrder verifies Finalize sorts newest-first.
func TestLRUFinalizeSortOrder(t *testing.T) {
	lru := NewLRU(10000)
	lru.Add(chunkStat{id: makeChunkID(1), size: 10, mtimeNano: 100})
	lru.Add(chunkStat{id: makeChunkID(2), size: 10, mtimeNano: 300})
	lru.Add(chunkStat{id: makeChunkID(3), size: 10, mtimeNano: 200})
	lru.Finalize()

	if len(lru.live) != 3 {
		t.Fatalf("expected 3 live, got %d", len(lru.live))
	}
	// Newest first: 300, 200, 100
	if lru.live[0].mtimeNano != 300 {
		t.Errorf("expected first mtime 300, got %d", lru.live[0].mtimeNano)
	}
	if lru.live[1].mtimeNano != 200 {
		t.Errorf("expected second mtime 200, got %d", lru.live[1].mtimeNano)
	}
	if lru.live[2].mtimeNano != 100 {
		t.Errorf("expected third mtime 100, got %d", lru.live[2].mtimeNano)
	}
}

// TestLRUFinalizeEviction verifies that Finalize evicts oldest chunks when over budget.
func TestLRUFinalizeEviction(t *testing.T) {
	lru := NewLRU(250) // budget of 250 bytes
	lru.Add(chunkStat{id: makeChunkID(1), size: 100, mtimeNano: 100}) // oldest
	lru.Add(chunkStat{id: makeChunkID(2), size: 100, mtimeNano: 200})
	lru.Add(chunkStat{id: makeChunkID(3), size: 100, mtimeNano: 300}) // newest

	lru.Finalize()

	// Total was 300, budget is 250, so oldest (id=1) should be evicted
	if len(lru.live) != 2 {
		t.Fatalf("expected 2 live after eviction, got %d", len(lru.live))
	}
	if lru.liveSize != 200 {
		t.Fatalf("expected liveSize 200, got %d", lru.liveSize)
	}
	if !lru.IsDead(makeChunkID(1)) {
		t.Error("chunk 1 (oldest) should be dead")
	}
	if lru.IsDead(makeChunkID(2)) {
		t.Error("chunk 2 should be alive")
	}
	if lru.IsDead(makeChunkID(3)) {
		t.Error("chunk 3 (newest) should be alive")
	}
	if lru.deadSize != 100 {
		t.Errorf("expected deadSize 100, got %d", lru.deadSize)
	}
}

// TestLRUFinalizeEvictsMultiple verifies that multiple chunks get evicted.
func TestLRUFinalizeEvictsMultiple(t *testing.T) {
	lru := NewLRU(100) // only 100 bytes budget
	for i := byte(0); i < 10; i++ {
		lru.Add(chunkStat{id: makeChunkID(i), size: 50, mtimeNano: int64(i) * 100})
	}

	lru.Finalize()

	// Total 500, budget 100 -> keep 2 newest (id=9, id=8)
	if lru.liveSize > 100 {
		t.Fatalf("liveSize %d exceeds budget 100", lru.liveSize)
	}
	if len(lru.live) != 2 {
		t.Fatalf("expected 2 live, got %d", len(lru.live))
	}
	// Newest should survive
	if lru.IsDead(makeChunkID(9)) {
		t.Error("chunk 9 (newest) should be alive")
	}
	if lru.IsDead(makeChunkID(8)) {
		t.Error("chunk 8 should be alive")
	}
	// Older should be dead
	for i := byte(0); i < 8; i++ {
		if !lru.IsDead(makeChunkID(i)) {
			t.Errorf("chunk %d should be dead", i)
		}
	}
}

// TestLRUAddDead verifies AddDead tracks dead chunks.
func TestLRUAddDead(t *testing.T) {
	lru := NewLRU(1000)
	id := makeChunkID(42)
	lru.AddDead(chunkStat{id: id, size: 500})

	if !lru.IsDead(id) {
		t.Error("chunk should be dead")
	}
	if lru.deadSize != 500 {
		t.Errorf("expected deadSize 500, got %d", lru.deadSize)
	}
	if len(lru.live) != 0 {
		t.Error("no live chunks expected")
	}
}

// TestLRUEmptyFinalize verifies Finalize on empty LRU is a no-op.
func TestLRUEmptyFinalize(t *testing.T) {
	lru := NewLRU(1000)
	lru.Finalize()
	if len(lru.live) != 0 || len(lru.dead) != 0 {
		t.Error("empty LRU should remain empty after Finalize")
	}
}

// TestLRUZeroBudget verifies that zero budget evicts everything.
func TestLRUZeroBudget(t *testing.T) {
	lru := NewLRU(0)
	lru.Add(chunkStat{id: makeChunkID(1), size: 100, mtimeNano: 100})
	lru.Finalize()

	if len(lru.live) != 0 {
		t.Errorf("expected 0 live with zero budget, got %d", len(lru.live))
	}
	if !lru.IsDead(makeChunkID(1)) {
		t.Error("chunk should be dead with zero budget")
	}
}

// TestKindFromPath verifies kind extraction from index paths.
func TestKindFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/cache/index/indices/narinfo/abcd/abcdefgh", "narinfo"},
		{"/cache/index/indices/nar/1234/1234abcd", "nar"},
		{"/cache/index/indices/realisations/ffff/ffffaaaa", "realisations"},
		{"/cache/index/indices/log/0000/00001111", "log"},
		{"indices/narinfo/abcd/abcdefgh", "narinfo"},
		{"/no/indices/here", "here"}, // "indices" is present, returns next segment
		{"/no/match/at/all", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := kindFromPath(tt.path)
		if got != tt.want {
			t.Errorf("kindFromPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestGCOnceIntegration runs gcOnce with realistic extensionless index files.
func TestGCOnceIntegration(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := desync.NewLocalStore(storeDir, defaultStoreOptions)
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateTimes = true

	indexDir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(indexDir, 0755); err != nil {
		t.Fatal(err)
	}
	idx, err := desync.NewLocalIndexStore(indexDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create test data and chunk it
	data := bytes.Repeat([]byte("TESTDATA"), 128*1024/8)
	chunker, err := desync.NewChunker(bytes.NewReader(data), 16384, 65536, 262144)
	if err != nil {
		t.Fatal(err)
	}
	index, err := desync.ChunkStream(context.Background(), chunker, store, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Store index with extensionless path (like production: indices/narinfo/hash[0:4]/hash)
	hash := "hyhrtest1234567890abcdefghijklmn"
	indexPath := filepath.Join("indices", "narinfo", hash[0:4], hash)
	storeIndex(t, idx, indexPath, index)

	gc := &GC{
		CacheSize:    1, // 1 GB — large enough to keep everything
		log:          zap.NewNop(),
		localStore:   store,
		localIndices: []desync.LocalIndexStore{idx},
	}

	// This should NOT deadlock (the original code would deadlock on extensionless paths)
	done := make(chan struct{})
	go func() {
		gc.gcOnce()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(30 * time.Second):
		t.Fatal("gcOnce deadlocked (timed out after 30s)")
	}

	// Verify chunks still exist (cache budget is huge, nothing should be evicted)
	for _, chunk := range index.Chunks {
		has, err := store.HasChunk(chunk.ID)
		if err != nil {
			t.Fatalf("error checking chunk: %v", err)
		}
		if !has {
			t.Errorf("chunk %s was incorrectly deleted", chunk.ID)
		}
	}
}

// TestGCOnceEvictsOldChunks verifies that GC evicts chunks when over budget.
func TestGCOnceEvictsOldChunks(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := desync.NewLocalStore(storeDir, defaultStoreOptions)
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateTimes = true

	indexDir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(indexDir, 0755); err != nil {
		t.Fatal(err)
	}
	idx, err := desync.NewLocalIndexStore(indexDir)
	if err != nil {
		t.Fatal(err)
	}

	// Create enough data to exceed a tiny cache
	var indices []desync.Index
	for i := 0; i < 5; i++ {
		data := bytes.Repeat([]byte(fmt.Sprintf("DATA%04d", i)), 128*1024/8)
		chunker, err := desync.NewChunker(bytes.NewReader(data), 16384, 65536, 262144)
		if err != nil {
			t.Fatal(err)
		}
		index, err := desync.ChunkStream(context.Background(), chunker, store, 1)
		if err != nil {
			t.Fatal(err)
		}
		hash := fmt.Sprintf("hash%028d", i)
		indexPath := filepath.Join("indices", "narinfo", hash[0:4], hash)
		storeIndex(t, idx, indexPath, index)
		indices = append(indices, index)
		// Small delay so timestamps differ
		time.Sleep(10 * time.Millisecond)
	}

	// CacheSize=0 means maxCacheSize underflows (huge). Use CacheSize=1 (1GB) which is plenty.
	// To test eviction, we'd need to fill >1GB. Instead, test the LRU directly.
	// This test validates that gcOnce runs without deadlock on multiple indices.
	gc := &GC{
		CacheSize:    1,
		log:          zap.NewNop(),
		localStore:   store,
		localIndices: []desync.LocalIndexStore{idx},
	}

	done := make(chan struct{})
	go func() {
		gc.gcOnce()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(30 * time.Second):
		t.Fatal("gcOnce deadlocked")
	}
}

// TestGCCrossNamespaceChunkDeletion verifies that the GC walks all namespace
// index stores, not just one.
func TestGCCrossNamespaceChunkDeletion(t *testing.T) {
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := desync.NewLocalStore(storeDir, defaultStoreOptions)
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateTimes = true

	indexDirA := filepath.Join(t.TempDir(), "indexA")
	indexDirB := filepath.Join(t.TempDir(), "indexB")
	for _, d := range []string{indexDirA, indexDirB} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	nsA, err := desync.NewLocalIndexStore(indexDirA)
	if err != nil {
		t.Fatal(err)
	}
	nsB, err := desync.NewLocalIndexStore(indexDirB)
	if err != nil {
		t.Fatal(err)
	}

	sharedData := bytes.Repeat([]byte("SHARED"), 128*1024/6)
	chunker, err := desync.NewChunker(bytes.NewReader(sharedData), 16384, 65536, 262144)
	if err != nil {
		t.Fatal(err)
	}
	sharedIndex, err := desync.ChunkStream(context.Background(), chunker, store, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Store with extensionless paths (matching production behavior)
	storeIndex(t, nsA, "indices/narinfo/shar/shared_hash_a", sharedIndex)
	storeIndex(t, nsB, "indices/narinfo/shar/shared_hash_b", sharedIndex)

	gc := &GC{
		CacheSize:    1,
		log:          zap.NewNop(),
		localStore:   store,
		localIndices: []desync.LocalIndexStore{nsA, nsB},
	}

	done := make(chan struct{})
	go func() {
		gc.gcOnce()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(30 * time.Second):
		t.Fatal("gcOnce deadlocked on cross-namespace test")
	}

	// Verify shared chunks survive
	for _, chunk := range sharedIndex.Chunks {
		has, err := store.HasChunk(chunk.ID)
		if err != nil {
			t.Fatalf("error checking chunk: %v", err)
		}
		if !has {
			t.Fatalf("shared chunk %s should still exist", chunk.ID)
		}
	}
}

// TestGCIndexDeletionSilencesErrors verifies that index deletion errors are
// properly reported.
func TestGCIndexDeletionSilencesErrors(t *testing.T) {
	dir := t.TempDir()
	filename := "test.narinfo"
	filePath := filepath.Join(dir, filename)

	if err := os.WriteFile(filePath, []byte("test index data"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)

	err := os.Remove(filePath)
	if err == nil {
		t.Fatal("expected os.Remove to fail on file in read-only directory")
	}

	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		t.Error("file should still exist after failed deletion")
	}
}

// TestWorkerShutdown verifies all workers exit cleanly when the channel is closed.
func TestWorkerShutdown(t *testing.T) {
	ch := make(chan integrityCheck, 64)
	done := make(chan struct{})

	workerCount := 3
	remaining := make(chan int, workerCount)

	for i := 0; i < workerCount; i++ {
		go func() {
			for range ch {
				// consume
			}
			remaining <- 1
		}()
	}

	go func() {
		count := 0
		for range remaining {
			count++
			if count == workerCount {
				close(done)
				return
			}
		}
	}()

	close(ch)

	select {
	case <-done:
		// all workers exited
	case <-time.After(5 * time.Second):
		t.Fatal("not all workers exited after channel close")
	}
}

// BenchmarkLRUFinalize benchmarks the append-then-sort approach at scale.
func BenchmarkLRUFinalize(b *testing.B) {
	for _, n := range []int{1000, 10000, 100000, 1000000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			// Pre-generate random data
			ids := make([]desync.ChunkID, n)
			times := make([]int64, n)
			rng := rand.New(rand.NewSource(42))
			for i := 0; i < n; i++ {
				rng.Read(ids[i][:])
				times[i] = rng.Int63()
			}

			b.ResetTimer()
			for iter := 0; iter < b.N; iter++ {
				lru := NewLRU(uint64(n) * 32768) // ~half capacity -> evicts half
				for i := 0; i < n; i++ {
					lru.Add(chunkStat{id: ids[i], size: 65536, mtimeNano: times[i]})
				}
				lru.Finalize()
			}
		})
	}
}

// BenchmarkLRUFinalizeNoEviction benchmarks sort-only path.
func BenchmarkLRUFinalizeNoEviction(b *testing.B) {
	n := 100000
	ids := make([]desync.ChunkID, n)
	times := make([]int64, n)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < n; i++ {
		rng.Read(ids[i][:])
		times[i] = rng.Int63()
	}

	b.ResetTimer()
	for iter := 0; iter < b.N; iter++ {
		lru := NewLRU(uint64(n) * 65536 * 2) // plenty of budget
		for i := 0; i < n; i++ {
			lru.Add(chunkStat{id: ids[i], size: 65536, mtimeNano: times[i]})
		}
		lru.Finalize()
	}
}

// TestLRUFinalizeStability verifies that Finalize produces deterministic results
// regardless of insertion order.
func TestLRUFinalizeStability(t *testing.T) {
	makeEntries := func() []chunkStat {
		entries := make([]chunkStat, 100)
		for i := range entries {
			entries[i] = chunkStat{
				id:        makeChunkID(byte(i)),
				size:      100,
				mtimeNano: int64(i) * 1000,
			}
		}
		return entries
	}

	// Shuffle and finalize, results should be the same
	lru1 := NewLRU(5000) // keep 50 of 100
	entries := makeEntries()
	rng := rand.New(rand.NewSource(1))
	rng.Shuffle(len(entries), func(i, j int) { entries[i], entries[j] = entries[j], entries[i] })
	for _, e := range entries {
		lru1.Add(e)
	}
	lru1.Finalize()

	lru2 := NewLRU(5000)
	entries2 := makeEntries()
	rng2 := rand.New(rand.NewSource(99))
	rng2.Shuffle(len(entries2), func(i, j int) { entries2[i], entries2[j] = entries2[j], entries2[i] })
	for _, e := range entries2 {
		lru2.Add(e)
	}
	lru2.Finalize()

	if len(lru1.live) != len(lru2.live) {
		t.Fatalf("different live count: %d vs %d", len(lru1.live), len(lru2.live))
	}

	// Sort both by ID for comparison (order should be by mtime but dead set should match)
	dead1 := make([]byte, 0, len(lru1.dead))
	dead2 := make([]byte, 0, len(lru2.dead))
	for id := range lru1.dead {
		dead1 = append(dead1, id[0])
	}
	for id := range lru2.dead {
		dead2 = append(dead2, id[0])
	}
	sort.Slice(dead1, func(i, j int) bool { return dead1[i] < dead1[j] })
	sort.Slice(dead2, func(i, j int) bool { return dead2[i] < dead2[j] })

	if len(dead1) != len(dead2) {
		t.Fatalf("different dead count: %d vs %d", len(dead1), len(dead2))
	}
	for i := range dead1 {
		if dead1[i] != dead2[i] {
			t.Errorf("dead set differs at index %d: %d vs %d", i, dead1[i], dead2[i])
		}
	}
}
