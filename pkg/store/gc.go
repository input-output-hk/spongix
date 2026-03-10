package store

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/folbricht/desync"
)

// GCConfig holds configuration for the garbage collector.
type GCConfig struct {
	DryRun       bool
	PinnedHashes map[StorePathHash32]struct{}
	// RetentionPeriod controls how long narinfos are kept.
	// Narinfos newer than this are roots (kept alive).
	// Zero means only pinned hashes survive.
	RetentionPeriod time.Duration
	// MaxBytes triggers size-based eviction when > 0.
	// After the normal GC phases, if disk usage exceeds this budget,
	// the oldest unpinned narinfos are evicted until under budget.
	MaxBytes uint64
}

// GCStats holds statistics about a GC run.
type GCStats struct {
	ReachableNarinfos   int
	UnreachableNarinfos int
	DeletedIndices      int
	CorruptIndices      int
	NHLookupErrors      int
	OrphanChunks        int
	DeletedChunks       int
	DeletedBytes        uint64
	SizeEvictedIndices  int
	SizeEvictedBytes    uint64
}

// GC runs the closure-aware garbage collection algorithm.
//
// Phase 1: Find roots — scan narinfo indices, keep those within retention or pinned.
// Phase 2: Transitive closure — BFS over r/ keys from roots.
// Phase 3: Delete unreachable indices and their cr/ entries.
// Phase 4: Delete orphan chunks — chunks with zero cr/ references.
func GC(db *DB, cfg GCConfig) (GCStats, error) {
	var stats GCStats

	// Phase 1: Find roots (narinfo indices within retention period or pinned)
	roots, err := findRoots(db, cfg)
	if err != nil {
		return stats, fmt.Errorf("phase 1 (find roots): %w", err)
	}

	// Phase 2: Transitive closure over r/ references
	reachable, err := transitiveClose(db, roots)
	if err != nil {
		return stats, fmt.Errorf("phase 2 (transitive closure): %w", err)
	}
	stats.ReachableNarinfos = len(reachable)

	// Phase 3: Delete unreachable indices
	deleted, err := deleteUnreachableIndices(db, reachable, cfg.DryRun)
	if err != nil {
		return stats, fmt.Errorf("phase 3 (delete unreachable): %w", err)
	}
	stats.DeletedIndices = deleted.count
	stats.UnreachableNarinfos = deleted.unreachable
	stats.CorruptIndices = deleted.corruptIndices
	stats.NHLookupErrors = deleted.nhLookupErrors

	// Phase 4: Delete orphan chunks
	orphans, err := deleteOrphanChunks(db, cfg.DryRun)
	if err != nil {
		return stats, fmt.Errorf("phase 4 (delete orphans): %w", err)
	}
	stats.OrphanChunks = orphans.found
	stats.DeletedChunks = orphans.deleted
	stats.DeletedBytes = orphans.bytes

	// Phase 5: Size-based eviction — if MaxBytes > 0 and DB exceeds budget,
	// evict oldest unpinned narinfos until under budget.
	if cfg.MaxBytes > 0 {
		sizeStats, err := sizeEvict(db, cfg)
		if err != nil {
			return stats, fmt.Errorf("phase 5 (size eviction): %w", err)
		}
		stats.SizeEvictedIndices = sizeStats.evicted
		stats.SizeEvictedBytes = sizeStats.bytes
	}

	return stats, nil
}

type sizeEvictResult struct {
	evicted int
	bytes   uint64
}

// timestampedIndex holds a narinfo index path and its creation timestamp.
type timestampedIndex struct {
	path string
	ts   time.Time
}

// sizeEvict deletes the oldest unpinned narinfos (and their transitive chunks)
// until disk usage drops below cfg.MaxBytes.
func sizeEvict(db *DB, cfg GCConfig) (sizeEvictResult, error) {
	var result sizeEvictResult

	diskUsage := db.db.Metrics().DiskSpaceUsage()
	if diskUsage <= cfg.MaxBytes {
		return result, nil
	}

	// Merge all pin sources for exclusion.
	allPins := make(map[StorePathHash32]struct{}, len(cfg.PinnedHashes))
	for h := range cfg.PinnedHashes {
		allPins[h] = struct{}{}
	}
	pebblePins, err := db.ListPins()
	if err != nil {
		return result, fmt.Errorf("listing pebble pins: %w", err)
	}
	for _, pe := range pebblePins {
		allPins[pe.Hash] = struct{}{}
	}

	// Collect remaining (surviving) narinfo indices with timestamps.
	prefix := []byte("i/indices/narinfo/")
	iter, err := db.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return result, err
	}
	defer func() { _ = iter.Close() }()

	var candidates []timestampedIndex
	for iter.First(); iter.Valid(); iter.Next() {
		indexPath := strings.TrimPrefix(string(iter.Key()), "i/")
		hash := StorePathHash32(extractPathHash(indexPath))
		if hash == "" {
			continue
		}
		// Skip pinned hashes.
		if _, pinned := allPins[hash]; pinned {
			continue
		}
		ts := time.Time{}
		tsVal, tsCloser, tsErr := db.db.Get(timestampKey(indexPath))
		if tsErr == nil {
			ts = decodeTimestamp(tsVal)
			_ = tsCloser.Close()
		}
		candidates = append(candidates, timestampedIndex{path: indexPath, ts: ts})
	}
	if err := iter.Error(); err != nil {
		return result, err
	}

	// Sort oldest-first.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].ts.Before(candidates[j].ts)
	})

	// Evict oldest narinfos until under budget.
	for _, cand := range candidates {
		if db.db.Metrics().DiskSpaceUsage() <= cfg.MaxBytes {
			break
		}

		if cfg.DryRun {
			result.evicted++
			continue
		}

		batch := db.db.NewBatch()
		idxBytes := addEvictionDeletes(db, batch, cand)

		if err := batch.Commit(pebble.Sync); err != nil {
			_ = batch.Close()
			return result, fmt.Errorf("committing size eviction batch: %w", err)
		}

		result.evicted++
		result.bytes += uint64(idxBytes)
	}

	// Run orphan chunk cleanup again after eviction to reclaim chunk storage.
	if !cfg.DryRun && result.evicted > 0 {
		orphans, err := deleteOrphanChunks(db, false)
		if err != nil {
			return result, fmt.Errorf("post-eviction orphan cleanup: %w", err)
		}
		result.bytes += orphans.bytes
	}

	return result, nil
}

// addEvictionDeletes adds all deletion keys for a single narinfo to the batch:
// the index, timestamp, refs, nh/nhr mappings, and chunk references.
// It returns the byte length of the index value (0 if the index was not found).
func addEvictionDeletes(db *DB, batch *pebble.Batch, cand timestampedIndex) int {
	_ = batch.Delete(indexKey(cand.path), nil)
	_ = batch.Delete(timestampKey(cand.path), nil)

	hash := StorePathHash32(extractPathHash(cand.path))
	_ = batch.Delete(narRefKey(hash), nil)

	// Delete nh/ and nhr/ entries if present.
	nhrKey := narHashReverseKey(hash)
	nhrVal, nhrCloser, nhrErr := db.db.Get(nhrKey)
	if nhrErr == nil {
		nh52 := make([]byte, len(nhrVal))
		copy(nh52, nhrVal)
		_ = nhrCloser.Close()
		_ = batch.Delete(narHashKey(NarHash52(nh52)), nil)
		_ = batch.Delete(nhrKey, nil)
	}

	// Delete cr/ entries for each chunk in the index.
	idxVal, idxCloser, idxErr := db.db.Get(indexKey(cand.path))
	if idxErr != nil {
		return 0
	}
	data := make([]byte, len(idxVal))
	copy(data, idxVal)
	_ = idxCloser.Close()

	idx, parseErr := desync.IndexFromReader(strings.NewReader(string(data)))
	if parseErr != nil {
		return len(data)
	}
	for _, chunk := range idx.Chunks {
		_ = batch.Delete(chunkRefKey(chunk.ID, cand.path), nil)
	}
	return len(data)
}

// findRoots scans all narinfo indices and returns store path hashes that
// are roots (within retention, file-pinned, or Pebble-pinned).
func findRoots(db *DB, cfg GCConfig) (map[StorePathHash32]struct{}, error) {
	roots := make(map[StorePathHash32]struct{})
	now := time.Now()

	// Merge Pebble-managed pins (p/ keys) into the pin set.
	allPins := make(map[StorePathHash32]struct{}, len(cfg.PinnedHashes))
	for h := range cfg.PinnedHashes {
		allPins[h] = struct{}{}
	}
	pebblePins, err := db.ListPins()
	if err != nil {
		return nil, fmt.Errorf("listing pebble pins: %w", err)
	}
	for _, pe := range pebblePins {
		allPins[pe.Hash] = struct{}{}
	}

	prefix := []byte("i/indices/narinfo/")
	iter, err := db.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		indexPath := strings.TrimPrefix(key, "i/")
		hash := StorePathHash32(extractPathHash(indexPath))
		if hash == "" {
			continue
		}

		// Pinned hashes (file or Pebble) are always roots.
		if _, pinned := allPins[hash]; pinned {
			roots[hash] = struct{}{}
			continue
		}

		// Check retention: narinfos within the retention period are roots.
		if cfg.RetentionPeriod > 0 {
			tsKey := timestampKey(indexPath)
			tsVal, tsCloser, tsErr := db.db.Get(tsKey)
			if tsErr == nil {
				ts := decodeTimestamp(tsVal)
				_ = tsCloser.Close()
				if now.Sub(ts) <= cfg.RetentionPeriod {
					roots[hash] = struct{}{}
					continue
				}
			} else if !errors.Is(tsErr, pebble.ErrNotFound) {
				return nil, fmt.Errorf("reading timestamp for %s: %w", indexPath, tsErr)
			}
			// No timestamp or expired: not a root (fall through)
		}
		// RetentionPeriod == 0: only pinned hashes are roots (already handled above)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return roots, nil
}

// transitiveClose performs BFS over r/ references starting from roots,
// returning the full set of reachable store path hashes.
func transitiveClose(db *DB, roots map[StorePathHash32]struct{}) (map[StorePathHash32]struct{}, error) {
	reachable := make(map[StorePathHash32]struct{}, len(roots))
	queue := make([]StorePathHash32, 0, len(roots))

	for hash := range roots {
		reachable[hash] = struct{}{}
		queue = append(queue, hash)
	}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		key := narRefKey(current)
		val, closer, err := db.db.Get(key)
		if errors.Is(err, pebble.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading refs for %s: %w", current, err)
		}
		data := make([]byte, len(val))
		copy(data, val)
		_ = closer.Close()

		refs := decodeNARRefs(data)
		for _, ref := range refs {
			if _, seen := reachable[ref]; !seen {
				reachable[ref] = struct{}{}
				queue = append(queue, ref)
			}
		}
	}

	return reachable, nil
}

type deleteResult struct {
	count          int
	unreachable    int
	corruptIndices int
	nhLookupErrors int
}

// deleteUnreachableIndices scans ALL index types (narinfo, nar, realisations, log)
// and deletes those whose store path hash is not in the reachable set.
// Also deletes associated cr/, r/, nh/, nhr/, and t/ entries.
//
// BUG FIX: NAR index paths use NarHash52, not StorePathHash32. The reachable set
// contains StorePathHash32 values. Before this fix, NAR indices were NEVER matched
// and always deleted — even when their narinfo was pinned.
func deleteUnreachableIndices(db *DB, reachable map[StorePathHash32]struct{}, dryRun bool) (deleteResult, error) {
	var result deleteResult

	// Scan ALL index types — narinfo, nar, realisations, log
	prefix := []byte("i/indices/")
	iter, err := db.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return result, err
	}
	defer func() { _ = iter.Close() }()

	batch := db.db.NewBatch()
	batchSize := 0

	commitBatch := func(opts *pebble.WriteOptions) error {
		if batchSize == 0 {
			return nil
		}
		if err := batch.Commit(opts); err != nil {
			return err
		}
		batch = db.db.NewBatch()
		batchSize = 0
		return nil
	}
	defer func() {
		if batchSize > 0 {
			_ = batch.Close()
		}
	}()

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		indexPath := strings.TrimPrefix(key, "i/")
		hash := extractPathHash(indexPath)
		if hash == "" {
			continue
		}

		// Resolve to StorePathHash32 based on index kind.
		var storeHash StorePathHash32
		if strings.HasPrefix(indexPath, "indices/nar/") {
			// NAR index paths use NarHash52 — resolve via nh/ mapping.
			resolved, err := db.GetStorePathByNarHash(NarHash52(hash))
			if err != nil {
				// DB error reading nh/ mapping; treat as unreachable so we don't
				// block the entire GC run for a transient read failure.
				result.nhLookupErrors++
			} else {
				storeHash = resolved
			}
		} else if strings.HasPrefix(indexPath, "indices/narinfo/") {
			storeHash = StorePathHash32(hash)
		} else {
			// Realisations and logs use non-StorePathHash formats (64-char SHA256
			// hex for realisations, "hash-name" for logs). Leave storeHash empty
			// so they always fail the reachability check and get deleted, matching
			// current behavior.
		}

		if storeHash != "" {
			if _, ok := reachable[storeHash]; ok {
				continue
			}
		}

		result.unreachable++

		if dryRun {
			continue
		}

		// Delete the index itself
		_ = batch.Delete([]byte(key), nil)
		// Delete the timestamp
		_ = batch.Delete(timestampKey(indexPath), nil)

		// Clean up r/, nh/, and nhr/ entries based on index kind.
		if strings.HasPrefix(indexPath, "indices/nar/") {
			// NAR indices don't have r/ entries (only narinfos do).
			// Delete nh/<NarHash52> directly.
			_ = batch.Delete(narHashKey(NarHash52(hash)), nil)
			// If we resolved the StorePathHash32 earlier, also delete nhr/.
			if storeHash != "" {
				_ = batch.Delete(narHashReverseKey(storeHash), nil)
			}
		} else if strings.HasPrefix(indexPath, "indices/narinfo/") {
			// Narinfo: delete r/ and look up nhr/ -> nh/ as before.
			_ = batch.Delete(narRefKey(storeHash), nil)
			nhrKey := narHashReverseKey(storeHash)
			if nhrVal, nhrCloser, nhrErr := db.db.Get(nhrKey); nhrErr == nil {
				nh52 := make([]byte, len(nhrVal))
				copy(nh52, nhrVal)
				_ = nhrCloser.Close()
				_ = batch.Delete(narHashKey(NarHash52(nh52)), nil)
				_ = batch.Delete(nhrKey, nil)
			}
		}
		// Realisations/logs: no r/, nh/, or nhr/ entries to clean up.

		// Read the index to get chunk IDs for cr/ cleanup
		idx, err := desync.IndexFromReader(strings.NewReader(string(iter.Value())))
		if err != nil {
			result.corruptIndices++
		} else {
			for _, chunk := range idx.Chunks {
				_ = batch.Delete(chunkRefKey(chunk.ID, indexPath), nil)
			}
		}

		result.count++
		batchSize++

		if batchSize >= 1000 {
			if err := commitBatch(pebble.NoSync); err != nil {
				return result, err
			}
		}
	}

	if err := iter.Error(); err != nil {
		return result, err
	}

	if err := commitBatch(pebble.Sync); err != nil {
		return result, err
	}

	return result, nil
}

type orphanResult struct {
	found   int
	deleted int
	bytes   uint64
}

// deleteOrphanChunks scans all chunks and deletes those with zero cr/ references.
func deleteOrphanChunks(db *DB, dryRun bool) (orphanResult, error) {
	var result orphanResult

	// Scan all chunk keys
	prefix := []byte("c/")
	iter, err := db.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return result, err
	}
	defer func() { _ = iter.Close() }()

	// Reusable iterator for cr/ prefix checks
	crPrefix := []byte("cr/")
	crIter, err := db.db.NewIter(&pebble.IterOptions{
		LowerBound: crPrefix,
		UpperBound: incrementKey(crPrefix),
	})
	if err != nil {
		return result, err
	}
	defer func() { _ = crIter.Close() }()

	batch := db.db.NewBatch()
	batchSize := 0

	commitBatch := func(opts *pebble.WriteOptions) error {
		if batchSize == 0 {
			return nil
		}
		if err := batch.Commit(opts); err != nil {
			return err
		}
		result.deleted += batchSize
		batch = db.db.NewBatch()
		batchSize = 0
		return nil
	}
	defer func() {
		if batchSize > 0 {
			_ = batch.Close() // discard uncommitted batch on error
		}
	}()

	for iter.First(); iter.Valid(); iter.Next() {
		key := iter.Key()
		hex := strings.TrimPrefix(string(key), "c/")

		id, err := desync.ChunkIDFromString(hex)
		if err != nil {
			continue
		}

		// Seek to cr/<chunkID>/ on reused iterator
		seekPrefix := chunkRefPrefix(id)
		hasRefs := crIter.SeekGE(seekPrefix) && bytes.HasPrefix(crIter.Key(), seekPrefix)

		if hasRefs {
			continue
		}

		result.found++
		result.bytes += uint64(len(iter.Value()))

		if dryRun {
			continue
		}

		_ = batch.Delete(append([]byte{}, key...), nil)
		batchSize++

		if batchSize >= 1000 {
			if err := commitBatch(pebble.NoSync); err != nil {
				return result, err
			}
		}
	}

	if err := iter.Error(); err != nil {
		return result, err
	}

	if err := commitBatch(pebble.Sync); err != nil {
		return result, err
	}

	return result, nil
}
