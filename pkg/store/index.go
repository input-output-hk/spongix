package store

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/folbricht/desync"
)

// ErrIndexNotFound is returned when a requested index does not exist.
var ErrIndexNotFound = errors.New("index not found")

var _ desync.IndexWriteStore = (*IndexStore)(nil)

// indexKey returns the Pebble key for an index: i/<name>
// The name already contains the full path like "indices/narinfo/hyhr/hash".
func indexKey(name string) []byte {
	key := make([]byte, 2+len(name))
	key[0] = 'i'
	key[1] = '/'
	copy(key[2:], name)
	return key
}

// IndexStore implements desync.IndexWriteStore on top of Pebble.
//
// Index keys are NOT prefixed with the namespace because Nix store paths are
// content-addressed: the same store path hash always maps to the same content
// regardless of which namespace cached it. This means multiple namespaces
// sharing a Pebble DB naturally deduplicate identical indices.
//
// The label field is used only for identification in String() and logging.
type IndexStore struct {
	db *DB
	// label identifies which namespace this store serves (for logging only;
	// does not affect key construction — see type-level doc comment).
	label string
}

// NewIndexStore creates a new IndexStore for the given label (typically a namespace name).
func NewIndexStore(db *DB, label string) *IndexStore {
	return &IndexStore{db: db, label: label}
}

func (s *IndexStore) GetIndex(name string) (desync.Index, error) {
	r, err := s.GetIndexReader(name)
	if err != nil {
		return desync.Index{}, err
	}
	defer func() { _ = r.Close() }()
	return desync.IndexFromReader(r)
}

func (s *IndexStore) GetIndexReader(name string) (io.ReadCloser, error) {
	key := indexKey(name)
	val, closer, err := s.db.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, ErrIndexNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting index %s: %w", name, err)
	}
	data := make([]byte, len(val))
	copy(data, val)
	_ = closer.Close()
	return io.NopCloser(bytes.NewReader(data)), nil
}

// addIndexEntries adds index, chunk-ref, NAR-ref, and timestamp keys to an existing batch.
// When narHash52 is non-empty, it also writes nh/ and nhr/ entries for NarHash->storePath mapping.
func addIndexEntries(batch *pebble.Batch, name string, idx desync.Index, narRefs []string, narHash52 string) error {
	buf := &bytes.Buffer{}
	if _, err := idx.WriteTo(buf); err != nil {
		return fmt.Errorf("serializing index: %w", err)
	}

	_ = batch.Set(indexKey(name), buf.Bytes(), nil)

	for _, chunk := range idx.Chunks {
		_ = batch.Set(chunkRefKey(chunk.ID, name), nil, nil)
	}

	if len(narRefs) > 0 {
		storePathHash := extractStorePathHash(name)
		if storePathHash != "" {
			_ = batch.Set(narRefKey(storePathHash), encodeNARRefs(narRefs), nil)
		}
	}

	_ = batch.Set(timestampKey(name), encodeTimestamp(time.Now()), nil)

	// Store NarHash -> storePathHash reverse mapping for proxyNar fallback.
	if narHash52 != "" {
		storePathHash := extractStorePathHash(name)
		if storePathHash != "" {
			_ = batch.Set(narHashKey(narHash52), []byte(storePathHash), nil)
			_ = batch.Set(narHashReverseKey(storePathHash), []byte(narHash52), nil)
		}
	}

	return nil
}

func (s *IndexStore) StoreIndex(name string, idx desync.Index) error {
	batch := s.db.db.NewBatch()
	defer func() { _ = batch.Close() }()
	if err := addIndexEntries(batch, name, idx, nil, ""); err != nil {
		return err
	}
	return batch.Commit(pebble.NoSync)
}

// StoreIndexWithRefs stores an index and records chunk references and NAR references.
// narRefs are store path hashes referenced by this narinfo (for r/ entries).
func (s *IndexStore) StoreIndexWithRefs(name string, idx desync.Index, narRefs []string) error {
	batch := s.db.db.NewBatch()
	defer func() { _ = batch.Close() }()
	if err := addIndexEntries(batch, name, idx, narRefs, ""); err != nil {
		return err
	}
	return batch.Commit(pebble.Sync)
}

func (s *IndexStore) Close() error {
	return nil // DB lifecycle managed by DB.Close()
}

func (s *IndexStore) String() string {
	return "pebble-index-store(" + s.label + ")"
}
