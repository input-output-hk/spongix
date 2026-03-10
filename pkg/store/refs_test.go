package store

import (
	"bytes"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
)

func TestIncrementKey(t *testing.T) {
	prefix := []byte("c/")
	got := incrementKey(prefix)
	want := []byte("c0") // '/' is 0x2F, incremented to 0x30 which is '0'
	if !bytes.Equal(got, want) {
		t.Fatalf("incrementKey(%q) = %q, want %q", prefix, got, want)
	}
}

func TestIncrementKeyAllFF(t *testing.T) {
	prefix := []byte{0xFF, 0xFF, 0xFF}
	got := incrementKey(prefix)
	if got != nil {
		t.Fatalf("incrementKey(all 0xFF) = %q, want nil", got)
	}
}

func TestIncrementKeyTrailingFF(t *testing.T) {
	prefix := []byte{'c', 0xFF}
	got := incrementKey(prefix)
	want := []byte{'d', 0x00}
	if !bytes.Equal(got, want) {
		t.Fatalf("incrementKey(%q) = %q, want %q", prefix, got, want)
	}
}

func TestEncodeDecodeNARRefs(t *testing.T) {
	refs := []StorePathHash32{
		"5b4cprjhjw35wyzvgmgvqay4hjf59h7x",
		"hyhrnrnpsz9fw5p9dk85a58y31ink18c",
		"914ivbx6hfpgczwphndm0vc4z6q2c8a1",
	}
	encoded := encodeNARRefs(refs)
	decoded := decodeNARRefs(encoded)
	if len(decoded) != len(refs) {
		t.Fatalf("round-trip length: got %d, want %d", len(decoded), len(refs))
	}
	for i, ref := range refs {
		if decoded[i] != ref {
			t.Fatalf("round-trip[%d]: got %q, want %q", i, decoded[i], ref)
		}
	}
}

func TestEncodeDecodeNARRefsEmpty(t *testing.T) {
	encoded := encodeNARRefs(nil)
	decoded := decodeNARRefs(encoded)
	if decoded != nil {
		t.Fatalf("decodeNARRefs of empty encode: got %v, want nil", decoded)
	}
}

func TestDecodeNARRefsSingle(t *testing.T) {
	data := []byte("5b4cprjhjw35wyzvgmgvqay4hjf59h7x")
	decoded := decodeNARRefs(data)
	if len(decoded) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(decoded))
	}
	if decoded[0] != StorePathHash32("5b4cprjhjw35wyzvgmgvqay4hjf59h7x") {
		t.Fatalf("got %q, want %q", decoded[0], "5b4cprjhjw35wyzvgmgvqay4hjf59h7x")
	}
}

func TestExtractStorePathHash(t *testing.T) {
	name := "indices/narinfo/hyhr/hyhrnrnpsz9fw5p9dk85a58y31ink18c"
	got := extractPathHash(name)
	want := "hyhrnrnpsz9fw5p9dk85a58y31ink18c"
	if got != want {
		t.Fatalf("extractPathHash(%q) = %q, want %q", name, got, want)
	}
}

func TestExtractStorePathHashMalformed(t *testing.T) {
	name := "noslashes"
	got := extractPathHash(name)
	if got != "" {
		t.Fatalf("extractPathHash(%q) = %q, want empty string", name, got)
	}
}

func TestDeleteNARRef(t *testing.T) {
	db := tempDB(t)
	rt := NewRefTracker(db)

	hash := StorePathHash32("hyhrnrnpsz9fw5p9dk85a58y31ink18c")
	refs := []StorePathHash32{"5b4cprjhjw35wyzvgmgvqay4hjf59h7x"}

	// Store a NAR ref directly via the underlying Pebble DB.
	key := narRefKey(hash)
	if err := db.db.Set(key, encodeNARRefs(refs), pebble.Sync); err != nil {
		t.Fatal(err)
	}

	// Verify it exists.
	got, err := rt.GetNARRefs(hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != refs[0] {
		t.Fatalf("before delete: got %v, want %v", got, refs)
	}

	// Delete it.
	if err := rt.DeleteNARRef(hash); err != nil {
		t.Fatal(err)
	}

	// Verify it is gone.
	got, err = rt.GetNARRefs(hash)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("after delete: got %v, want nil", got)
	}
}

func TestGetNARRefsNotFound(t *testing.T) {
	db := tempDB(t)
	rt := NewRefTracker(db)

	got, err := rt.GetNARRefs(StorePathHash32("nonexistent_hash"))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil refs, got %v", got)
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	now := time.Now()
	encoded := encodeTimestamp(now)
	decoded := decodeTimestamp(encoded)
	// UnixNano precision
	if now.UnixNano() != decoded.UnixNano() {
		t.Fatalf("timestamp round-trip failed: %v != %v", now, decoded)
	}
}

func TestTimestampKey(t *testing.T) {
	key := timestampKey("indices/narinfo/aaaa/aaaa1111")
	if string(key) != "t/indices/narinfo/aaaa/aaaa1111" {
		t.Fatalf("expected t/indices/narinfo/aaaa/aaaa1111, got %s", key)
	}
}

func TestDecodeTimestampInvalid(t *testing.T) {
	ts := decodeTimestamp([]byte("short"))
	if !ts.IsZero() {
		t.Fatal("expected zero time for invalid data")
	}
}

func TestNarHashMapping(t *testing.T) {
	db := tempDB(t)

	narHash52 := NarHash52("1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp")
	storePathHash := StorePathHash32("hyhrnrnpsz9fw5p9dk85a58y31ink18c")

	// Store mapping directly via batch
	batch := db.NewBatch()
	_ = batch.Set(narHashKey(narHash52), []byte(storePathHash), nil)
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	_ = batch.Close()

	// Lookup
	got, err := db.GetStorePathByNarHash(narHash52)
	if err != nil {
		t.Fatal(err)
	}
	if got != storePathHash {
		t.Fatalf("GetStorePathByNarHash = %q, want %q", got, storePathHash)
	}

	// Not found
	got, err = db.GetStorePathByNarHash(NarHash52("nonexistent"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty for missing, got %q", got)
	}
}
