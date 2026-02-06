package store

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/folbricht/desync"
)

// GCConfig holds configuration for the garbage collector.
type GCConfig struct {
	DryRun       bool
	PinnedHashes map[string]struct{}
	// RetentionPeriod controls how long narinfos are kept.
	// Narinfos newer than this are roots (kept alive).
	// Zero means only pinned hashes survive.
	RetentionPeriod time.Duration
}

// GCStats holds statistics about a GC run.
type GCStats struct {
	ReachableNarinfos   int
	UnreachableNarinfos int
	DeletedIndices      int
	CorruptIndices      int
	OrphanChunks        int
	DeletedChunks       int
	DeletedBytes        uint64
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

	// Phase 4: Delete orphan chunks
	orphans, err := deleteOrphanChunks(db, cfg.DryRun)
	if err != nil {
		return stats, fmt.Errorf("phase 4 (delete orphans): %w", err)
	}
	stats.OrphanChunks = orphans.found
	stats.DeletedChunks = orphans.deleted
	stats.DeletedBytes = orphans.bytes

	return stats, nil
}

// findRoots scans all narinfo indices and returns store path hashes that
// are roots (within retention or pinned).
func findRoots(db *DB, cfg GCConfig) (map[string]struct{}, error) {
	roots := make(map[string]struct{})
	now := time.Now()

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
		hash := extractStorePathHash(indexPath)
		if hash == "" {
			continue
		}

		// Pinned hashes are always roots.
		if _, pinned := cfg.PinnedHashes[hash]; pinned {
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
func transitiveClose(db *DB, roots map[string]struct{}) (map[string]struct{}, error) {
	reachable := make(map[string]struct{}, len(roots))
	queue := make([]string, 0, len(roots))

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
}

// deleteUnreachableIndices scans ALL index types (narinfo, nar, realisations, log)
// and deletes those whose store path hash is not in the reachable set.
// Also deletes associated cr/, r/, and t/ entries.
func deleteUnreachableIndices(db *DB, reachable map[string]struct{}, dryRun bool) (deleteResult, error) {
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
		hash := extractStorePathHash(indexPath)
		if hash == "" {
			continue
		}

		if _, ok := reachable[hash]; ok {
			continue
		}

		result.unreachable++

		if dryRun {
			continue
		}

		// Delete the index itself
		_ = batch.Delete([]byte(key), nil)
		// Delete the timestamp
		_ = batch.Delete(timestampKey(indexPath), nil)
		// Delete NAR refs (if any)
		_ = batch.Delete(narRefKey(hash), nil)

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
