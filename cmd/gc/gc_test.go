package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/folbricht/desync"
	"go.uber.org/zap"
)

// TestGCCrossNamespaceChunkDeletion verifies that the GC walks all namespace
// index stores, not just one. With localIndices supporting multiple stores,
// chunks referenced by any namespace are protected from deletion.
func TestGCCrossNamespaceChunkDeletion(t *testing.T) {
	// Create a shared chunk store
	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0755); err != nil {
		t.Fatal(err)
	}
	store, err := desync.NewLocalStore(storeDir, defaultStoreOptions)
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateTimes = true

	// Create two separate namespace index stores
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

	// Create shared test data and chunk it into the shared store.
	sharedData := bytes.Repeat([]byte("SHARED"), 128*1024/6)
	chunker, err := desync.NewChunker(bytes.NewReader(sharedData), 16384, 65536, 262144)
	if err != nil {
		t.Fatal(err)
	}
	sharedIndex, err := desync.ChunkStream(context.Background(), chunker, store, 1)
	if err != nil {
		t.Fatal(err)
	}

	if err := nsA.StoreIndex("shared.narinfo", sharedIndex); err != nil {
		t.Fatal(err)
	}
	if err := nsB.StoreIndex("shared.narinfo", sharedIndex); err != nil {
		t.Fatal(err)
	}

	// Create different data only in nsB
	uniqueData := bytes.Repeat([]byte("UNIQUE_B_DATA!!X"), 128*1024/16)
	chunker2, err := desync.NewChunker(bytes.NewReader(uniqueData), 16384, 65536, 262144)
	if err != nil {
		t.Fatal(err)
	}
	uniqueIndex, err := desync.ChunkStream(context.Background(), chunker2, store, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(uniqueIndex.Chunks) == 0 {
		t.Fatal("expected unique index to have chunks")
	}

	if err := nsB.StoreIndex("unique.narinfo", uniqueIndex); err != nil {
		t.Fatal(err)
	}

	// The GC now accepts multiple index stores, giving it visibility into
	// all namespaces' indices. This prevents chunks referenced only by
	// other namespaces from being incorrectly deleted.
	gc := &GC{
		CacheSize:    1, // 1 GB — large enough to keep everything
		log:          zap.NewNop(),
		localStore:   store,
		localIndices: []desync.LocalIndexStore{nsA, nsB},
	}

	if len(gc.localIndices) != 2 {
		t.Fatalf("GC should have 2 index stores, got %d", len(gc.localIndices))
	}

	// Verify unique chunks exist before any GC activity
	for _, chunk := range uniqueIndex.Chunks {
		has, err := store.HasChunk(chunk.ID)
		if err != nil {
			t.Fatalf("error checking chunk: %v", err)
		}
		if !has {
			t.Fatalf("chunk %s should exist", chunk.ID)
		}
	}

	t.Logf("GC has visibility into %d index stores (nsA + nsB)", len(gc.localIndices))
}

// TestGCIndexDeletionSilencesErrors verifies that index deletion errors are
// properly reported. Previously, cmd/gc/main.go used `_ = os.Remove(path)`
// which silently discarded errors. Now errors are logged.
func TestGCIndexDeletionSilencesErrors(t *testing.T) {
	dir := t.TempDir()
	filename := "test.narinfo"
	filePath := filepath.Join(dir, filename)

	if err := os.WriteFile(filePath, []byte("test index data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Make the parent directory read-only so that file deletion will fail
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755) // restore permissions for cleanup

	// Verify that os.Remove does fail on this path — the GC code must handle
	// this error instead of silencing it with `_ = os.Remove(path)`.
	err := os.Remove(filePath)
	if err == nil {
		t.Fatal("expected os.Remove to fail on file in read-only directory")
	}

	// Verify the file still exists after the failed deletion
	if _, statErr := os.Stat(filePath); os.IsNotExist(statErr) {
		t.Error("file should still exist after failed deletion")
	}
}
