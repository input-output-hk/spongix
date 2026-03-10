package store

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestPinHashRoundTrip(t *testing.T) {
	db := tempDB(t)

	hash := StorePathHash32("aaaa1111aaaa1111aaaa1111aaaa1111")

	// Initially not pinned.
	pinned, err := db.IsPinned(hash)
	if err != nil {
		t.Fatal(err)
	}
	if pinned {
		t.Fatal("expected not pinned initially")
	}

	// Pin it.
	if err := db.PinHash(hash, "test-label"); err != nil {
		t.Fatal(err)
	}

	pinned, err = db.IsPinned(hash)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Fatal("expected pinned after PinHash")
	}

	// Unpin it.
	if err := db.UnpinHash(hash); err != nil {
		t.Fatal(err)
	}

	pinned, err = db.IsPinned(hash)
	if err != nil {
		t.Fatal(err)
	}
	if pinned {
		t.Fatal("expected not pinned after UnpinHash")
	}
}

func TestListPins(t *testing.T) {
	db := tempDB(t)

	// Empty list initially.
	pins, err := db.ListPins()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("expected 0 pins, got %d", len(pins))
	}

	// Add two pins.
	if err := db.PinHash(StorePathHash32("aaaa1111aaaa1111aaaa1111aaaa1111"), "label-a"); err != nil {
		t.Fatal(err)
	}
	if err := db.PinHash(StorePathHash32("bbbb2222bbbb2222bbbb2222bbbb2222"), "label-b"); err != nil {
		t.Fatal(err)
	}

	pins, err = db.ListPins()
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Hash < pins[j].Hash })
	if len(pins) != 2 {
		t.Fatalf("expected 2 pins, got %d", len(pins))
	}
	if pins[0].Hash != StorePathHash32("aaaa1111aaaa1111aaaa1111aaaa1111") || pins[0].Label != "label-a" {
		t.Fatalf("unexpected pin[0]: %+v", pins[0])
	}
	if pins[1].Hash != StorePathHash32("bbbb2222bbbb2222bbbb2222bbbb2222") || pins[1].Label != "label-b" {
		t.Fatalf("unexpected pin[1]: %+v", pins[1])
	}
}

func TestLoadPinnedHashesEmptyPath(t *testing.T) {
	pins, err := LoadPinnedHashes("")
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("expected empty map, got %d entries", len(pins))
	}
}

func TestLoadPinnedHashesMissingFile(t *testing.T) {
	pins, err := LoadPinnedHashes("/nonexistent/path/to/file")
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("expected empty map for missing file, got %d entries", len(pins))
	}
}

func TestLoadPinnedHashesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pinned")
	content := "aaaa1111aaaa1111aaaa1111aaaa1111\n# comment line\n\nbbbb2222bbbb2222bbbb2222bbbb2222\n  cccc3333cccc3333cccc3333cccc3333  \n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	pins, err := LoadPinnedHashes(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 3 {
		t.Fatalf("expected 3 pins, got %d", len(pins))
	}
	for _, h := range []StorePathHash32{
		"aaaa1111aaaa1111aaaa1111aaaa1111",
		"bbbb2222bbbb2222bbbb2222bbbb2222",
		"cccc3333cccc3333cccc3333cccc3333",
	} {
		if _, ok := pins[h]; !ok {
			t.Errorf("expected pin %q to be present", h)
		}
	}
}

func TestGCPebblePinsProtectNarinfos(t *testing.T) {
	db, cs, is := setupGCTestDB(t)

	hash := StorePathHash32("aaaa1111aaaa1111aaaa1111aaaa1111")
	chunkA := makeTestIndex(t, cs, is, string(hash), []byte("pinned via pebble"), nil)

	// Pin via Pebble (not via file pins).
	if err := db.PinHash(hash, "gc-test"); err != nil {
		t.Fatal(err)
	}

	stats, err := GC(db, GCConfig{})
	if err != nil {
		t.Fatal(err)
	}

	if stats.ReachableNarinfos != 1 {
		t.Errorf("expected 1 reachable (pebble-pinned), got %d", stats.ReachableNarinfos)
	}
	if stats.DeletedIndices != 0 {
		t.Errorf("expected 0 deleted, got %d", stats.DeletedIndices)
	}

	has, err := cs.HasChunk(chunkA)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("chunk should survive (narinfo pinned via pebble)")
	}
}
