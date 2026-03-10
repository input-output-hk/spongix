package store

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/folbricht/desync"
)

// chunkRefKey returns the key for a chunk->index reference: cr/<chunkID>/<indexPath>
func chunkRefKey(id desync.ChunkID, indexPath string) []byte {
	hex := id.String()
	// cr/ + hex + / + indexPath
	key := make([]byte, 3+len(hex)+1+len(indexPath))
	copy(key, "cr/")
	copy(key[3:], hex)
	key[3+len(hex)] = '/'
	copy(key[3+len(hex)+1:], indexPath)
	return key
}

// chunkRefPrefix returns the key prefix for all references to a chunk: cr/<chunkID>/
func chunkRefPrefix(id desync.ChunkID) []byte {
	hex := id.String()
	prefix := make([]byte, 3+len(hex)+1)
	copy(prefix, "cr/")
	copy(prefix[3:], hex)
	prefix[3+len(hex)] = '/'
	return prefix
}

// narRefKey returns the key for a store path's references: r/<storePathHash>
func narRefKey(storePathHash StorePathHash32) []byte {
	key := make([]byte, 2+len(storePathHash))
	key[0] = 'r'
	key[1] = '/'
	copy(key[2:], storePathHash)
	return key
}

// narHashKey returns the Pebble key for a NarHash mapping: nh/<narHash52>
// Maps a 52-char nix-base32 NarHash to a 32-char store path hash.
func narHashKey(narHash52 NarHash52) []byte {
	key := make([]byte, 3+len(narHash52))
	key[0] = 'n'
	key[1] = 'h'
	key[2] = '/'
	copy(key[3:], narHash52)
	return key
}

// narHashReverseKey returns the Pebble key: nhr/<storePathHash32>
// Reverse of nh/, enabling GC to efficiently find and delete nh/ entries.
func narHashReverseKey(storePathHash StorePathHash32) []byte {
	key := make([]byte, 4+len(storePathHash))
	copy(key, "nhr/")
	copy(key[4:], storePathHash)
	return key
}

// encodeNARRefs encodes a list of store path hashes as newline-separated bytes.
func encodeNARRefs(refs []StorePathHash32) []byte {
	strs := make([]string, len(refs))
	for i, r := range refs {
		strs[i] = string(r)
	}
	return []byte(strings.Join(strs, "\n"))
}

// decodeNARRefs decodes a newline-separated list of store path hashes.
func decodeNARRefs(data []byte) []StorePathHash32 {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\n")
	refs := make([]StorePathHash32, len(parts))
	for i, p := range parts {
		refs[i] = StorePathHash32(p)
	}
	return refs
}

// extractPathHash extracts the hash from an index name (last path component).
// Index names look like "indices/narinfo/hyhr/hyhrnrnpsz9fw5p9dk85a58y31ink18c"
// or "indices/nar/0bmw/0bmwc9fqyilxy46nmvy5wkdfbkqwvi7y80wlw5nks7l7xydzkfnp".
// The returned string may be a StorePathHash32 or NarHash52 depending on
// the index kind; callers must cast based on the path prefix.
func extractPathHash(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-1]
}

// RefTracker provides reference tracking operations on the Pebble DB.
type RefTracker struct {
	db *DB
}

// NewRefTracker creates a new RefTracker for the given DB.
func NewRefTracker(db *DB) *RefTracker {
	return &RefTracker{db: db}
}

// GetChunkRefCount returns the number of indices that reference a chunk.
func (rt *RefTracker) GetChunkRefCount(id desync.ChunkID) (int, error) {
	prefix := chunkRefPrefix(id)
	iter, err := rt.db.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return 0, fmt.Errorf("creating iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.First(); iter.Valid(); iter.Next() {
		count++
	}
	return count, iter.Error()
}

// GetNARRefs returns the store path hashes referenced by a given store path.
func (rt *RefTracker) GetNARRefs(storePathHash StorePathHash32) ([]StorePathHash32, error) {
	key := narRefKey(storePathHash)
	val, closer, err := rt.db.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting NAR refs for %s: %w", storePathHash, err)
	}
	data := make([]byte, len(val))
	copy(data, val)
	_ = closer.Close()
	return decodeNARRefs(data), nil
}

// DeleteChunkRefs deletes all cr/ entries for a given index path.
// Used when deleting an index to clean up reverse references.
func (rt *RefTracker) DeleteChunkRefsForIndex(indexPath string, chunkIDs []desync.ChunkID) error {
	batch := rt.db.db.NewBatch()
	for _, id := range chunkIDs {
		_ = batch.Delete(chunkRefKey(id, indexPath), nil)
	}
	return batch.Commit(pebble.Sync)
}

// DeleteNARRef deletes the r/ entry for a store path hash.
func (rt *RefTracker) DeleteNARRef(storePathHash StorePathHash32) error {
	return rt.db.db.Delete(narRefKey(storePathHash), pebble.Sync)
}

// incrementKey returns a key that is one byte past the prefix,
// used as an upper bound for prefix iteration.
func incrementKey(prefix []byte) []byte {
	upper := make([]byte, len(prefix))
	copy(upper, prefix)
	for i := len(upper) - 1; i >= 0; i-- {
		upper[i]++
		if upper[i] != 0 {
			return upper
		}
	}
	return nil // overflow: all 0xFF → nil means no upper bound (scan to end of DB). Safe: our prefixes never overflow.
}

// timestampKey returns the Pebble key for an index timestamp: t/<indexPath>
func timestampKey(indexPath string) []byte {
	key := make([]byte, 2+len(indexPath))
	key[0] = 't'
	key[1] = '/'
	copy(key[2:], indexPath)
	return key
}

func encodeTimestamp(t time.Time) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(t.UnixNano()))
	return buf
}

func decodeTimestamp(data []byte) time.Time {
	if len(data) != 8 {
		return time.Time{}
	}
	nano := int64(binary.BigEndian.Uint64(data))
	return time.Unix(0, nano)
}
