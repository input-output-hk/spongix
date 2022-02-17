package main

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/folbricht/desync"
	"go.uber.org/zap"
)

func (proxy *Proxy) gc() {
	cache := map[string]*ChunkStat{}
	proxy.gcOnce(cache)

	gcInterval := 1 * time.Minute

	ticker := time.NewTicker(gcInterval)
	for {
		<-ticker.C
		proxy.gcOnce(cache)
	}
}

type ChunkStat struct {
	id    desync.ChunkID
	size  int64
	mtime time.Time
}

// we assume every directory requires 4KB of size (one block) desync stores
// files in directories with a 4 hex prefix, so we need to keep at least this
// amount of space reserved.
const maxCacheDirPortion = 0xffff * 4096

/*
Local GC strategies:
  Check every index file:
    If chunks are missing, delete it.
  	If it is not referenced by the database anymore, delete it.
  Check every narinfo in the database:
    If index is missing, delete it.
  	If last access is too old, delete it.
*/
func (proxy *Proxy) gcOnce(cache map[string]*ChunkStat) {
	// Keep cache size on disk below this size
	maxCacheSize := uint64(math.Pow(10, 10)*2) - maxCacheDirPortion

	store := proxy.narStore.(desync.LocalStore)
	indices := proxy.narIndex.(desync.LocalIndexStore)
	// err := store.Verify(context.Background(), 1, true, os.Stderr)
	// fatal(err)

	chunkStats := []*ChunkStat{}
	onDiskSize := uint64(0)

	startWalkStore := time.Now()

	err := filepath.WalkDir(store.Base, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if chunkStat, found := cache[path]; found {
			onDiskSize += uint64(chunkStat.size)
			chunkStats = append(chunkStats, chunkStat)
		} else {
			if info.IsDir() {
				return nil
			}

			name := info.Name()
			if strings.HasPrefix(name, ".tmp") {
				return nil
			}

			ext := filepath.Ext(name)
			if ext != desync.CompressedChunkExt {
				return nil
			}

			idstr := name[0 : len(name)-len(ext)]

			id, err := desync.ChunkIDFromString(idstr)
			if err != nil {
				return err
			}

			i, err := info.Info()
			if err != nil {
				fmt.Println(err)
				return err
			}

			onDiskSize += uint64(i.Size())

			chunkStat := &ChunkStat{id: id, size: i.Size(), mtime: i.ModTime()}
			chunkStats = append(chunkStats, chunkStat)
			cache[path] = chunkStat
		}

		return nil
	})
	if err != nil {
		proxy.log.Error("Failure while walking store", zap.Error(err))
		return
	}

	sort.Slice(chunkStats, func(i, j int) bool {
		return chunkStats[i].mtime.Before(chunkStats[j].mtime)
	})

	oldChunks := map[desync.ChunkID]struct{}{}
	newChunks := map[desync.ChunkID]struct{}{}
	chunksToDeleteSize := uint64(0)
	for _, stat := range chunkStats {
		if (onDiskSize - chunksToDeleteSize) > maxCacheSize {
			oldChunks[stat.id] = struct{}{}
			chunksToDeleteSize += uint64(stat.size)
		} else {
			newChunks[stat.id] = struct{}{}
		}
	}

	indicesToDelete := map[string]struct{}{}
	indicesSize := uint64(0)
	indicesToDeleteSize := uint64(0)

	startIndexWalk := time.Now()

	err = filepath.WalkDir(indices.Path, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		narHash := info.Name()
		index, err := indices.GetIndex(narHash)
		if err != nil {
			return err
		}

		indicesSize += uint64(index.Length())

		for _, chunk := range index.Chunks {
			_, found := oldChunks[chunk.ID]
			if found {
				indicesToDelete[narHash] = struct{}{}
				indicesToDeleteSize += uint64(index.Length())
			}
		}

		return nil
	})
	if err != nil {
		proxy.log.Error("While walking index", zap.Error(err))
		return
	}

	proxy.log.Debug("gc",
		zap.String("cache_max", ByteCountSI(int64(maxCacheSize))),
		zap.Uint64("cache_max_bytes", maxCacheSize),
		zap.Int("chunk_files", len(oldChunks)+len(newChunks)),
		zap.String("chunk_human", ByteCountSI(int64(onDiskSize))),
		zap.Uint64("chunk_bytes", onDiskSize),
		zap.String("index_human", ByteCountSI(int64(indicesSize))),
		zap.Uint64("index_bytes", indicesSize),
		zap.Duration("store_walk", time.Since(startWalkStore)),
		zap.Duration("index_walk", time.Since(startIndexWalk)),
		zap.Int("chunks", len(chunkStats)),
		zap.Int("index_gc_files", len(indicesToDelete)),
		zap.Uint64("index_gc_bytes", indicesToDeleteSize),
		zap.Uint64("chunk_gc_bytes", chunksToDeleteSize),
	)

	if len(indicesToDelete) == 0 {
		return
	}

	proxy.log.Debug("indices to remove",
		zap.Int("index_files", len(indicesToDelete)),
		zap.Uint64("index_files", indicesToDeleteSize),
		zap.Uint64("chunk_fies", chunksToDeleteSize),
	)

	if err := proxy.deleteNarinfos(indicesToDelete); err != nil {
		proxy.log.Error("Failed to delete narinfos", zap.Error(err))
	}

	startPrune := time.Now()

	if err = store.Prune(context.Background(), newChunks); err != nil {
		proxy.log.Error("Failed to prune store", zap.Error(err))
	}

	proxy.log.Debug("pruned store",
		zap.Duration("duration", time.Since(startPrune)),
	)
}

func (proxy *Proxy) deleteNarinfos(narinfos map[string]struct{}) error {
	indexPath := proxy.narIndex.(desync.LocalIndexStore).Path

	for narHash := range narinfos {
		tx, err := proxy.db.BeginTx(context.Background(), nil)
		if err != nil {
			proxy.log.Error("failed to start transaction", zap.Error(err))
			return err
		}

		res := tx.QueryRow(`SELECT name FROM narinfos WHERE nar_hash = ?`, narHash)
		var name string
		if err = res.Scan(&name); err != nil && err != sql.ErrNoRows {
			proxy.log.Error("failed delete narinfo", zap.String("hash", narHash), zap.Error(err))
			if err = tx.Rollback(); err != nil {
				return err
			}
			return err
		}

		_, err = tx.Exec(`DELETE FROM signatures WHERE name = ?`, name)
		if err != nil && err != sql.ErrNoRows {
			proxy.log.Error("failed delete narinfo sigs", zap.String("name", name), zap.String("hash", narHash), zap.Error(err))
			if err = tx.Rollback(); err != nil {
				return err
			}
			return err
		}

		_, err = tx.Exec(`DELETE FROM refs WHERE parent = ?`, name)
		if err != nil && err != sql.ErrNoRows {
			proxy.log.Error("failed delete narinfo refs", zap.String("name", name), zap.String("hash", narHash), zap.Error(err))
			if err = tx.Rollback(); err != nil {
				return err
			}
			return err
		}

		_, err = tx.Exec(`DELETE FROM narinfos WHERE nar_hash = ?`, narHash)
		if err != nil && err != sql.ErrNoRows {
			proxy.log.Error("failed delete narinfo", zap.String("hash", narHash), zap.Error(err))
			if err = tx.Rollback(); err != nil {
				return err
			}
			return err
		}

		if err = tx.Commit(); err != nil {
			proxy.log.Error("failed to commit transaction", zap.String("name", name), zap.String("hash", narHash), zap.Error(err))
			return err
		}

		indexPath := filepath.Join(indexPath, narHash)
		if err = os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
			proxy.log.Error("failed to remove index", zap.String("path", indexPath), zap.String("name", name), zap.String("hash", narHash), zap.Error(err))
			return err
		}

		proxy.log.Debug("Deleted", zap.String("path", indexPath))
	}

	return nil
}

/*
	return

	for path := range dirsToRemove {
		os.Remove(path)
	}

	err = filepath.WalkDir(indices.Path, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			fmt.Println(err)
			return err
		}
		if info.IsDir() {
			return nil
		}

		narHash := info.Name()
		index, err := indices.GetIndex(narHash)
		if err != nil {
			return err
		}

		chunksTotal += len(index.Chunks)

		i, err := info.Info()
		if err != nil {
			fmt.Println(err)
			return err
		}

		res := proxy.db.QueryRow(`SELECT name FROM narinfos WHERE nar_hash = $1 and accessed_at < $2`, narHash, cutoffTime)
		var name string
		err = res.Scan(&name)
		if err == sql.ErrNoRows {
			narinfosNotInDb[name] = narHash
		} else if err != nil {
			fmt.Println(err)
		} else if i.ModTime().Before(cutoffTime) {
			narinfosToPrune[name] = narHash
		}

		storeSize += float64(index.Length())

		for _, chunk := range index.Chunks {
			// present, err := store.HasChunk(chunk.ID)
			chunksAlive[chunk.ID] = struct{}{}
		}

		return nil
	})
	fatal(err)

	if len(narinfosNotInDb) > 0 {
		fmt.Printf("Indices not in DB: %d\n", len(narinfosNotInDb))
	}
	if len(narinfosToPrune) > 0 {
		fmt.Printf("Indices to Remove: %d\n", len(narinfosToPrune))
	}
	fmt.Printf("Chunks total/alive: %d/%d\n", chunksTotal, len(chunksAlive))

	err = store.Prune(context.Background(), chunksAlive)
	fatal(err)

	for _, narHash := range narinfosNotInDb {
		os.Remove(filepath.Join(indices.Path, narHash))
	}

	totalRowsDeleted := proxy.deleteNarinfos(narinfosToPrune)
	if totalRowsDeleted > 0 {
		fmt.Printf("Deleted %d cache rows for %d cache entries\n", totalRowsDeleted, len(narinfosToPrune))
	}

	saveSpaceQuery := `
		SELECT name, nar_hash
		FROM (SELECT o.*, SUM(nar_size) OVER (ORDER BY accessed_at ASC) as total_size
					FROM narinfos o) o
		WHERE o.total_size - o.nar_size < $2
	`

	res, err := proxy.db.Query(saveSpaceQuery, cutoffTime, maxCacheSize)
	fatal(err)

	missingIndices := map[string]string{}
	resultSize := int64(0)
	combined := map[desync.ChunkID]uint64{}
	chunksPresent := int64(0)
	chunksMissing := int64(0)

	for res.Next() {
		var name, narHash string
		fatal(res.Scan(&name, &narHash))
		index, err := proxy.narIndex.GetIndex(narHash)
		if err != nil {
			fmt.Printf("missing index for %s %s", name, narHash)
			missingIndices[name] = narHash
			continue
		}

		for _, chunk := range index.Chunks {
			combined[chunk.ID] = chunk.Size
			present, err := store.HasChunk(chunk.ID)
			if err != nil {
				fmt.Println(err)
				chunksMissing++
				continue
			}
			if present {
				chunksPresent++
			} else {
				chunksMissing++
			}
		}
		resultSize += index.Length()
	}
	res.Close()

	combinedSize := uint64(0)
	for _, size := range combined {
		combinedSize += size
	}

	fmt.Printf("Chunks present / missing: %d / %d\n", chunksPresent, chunksMissing)
	fmt.Println(ByteCountSI(int64(combinedSize)))
	fmt.Println(ByteCountSI(resultSize))

	pretty.Println("missing Indices:", missingIndices)
	proxy.deleteNarinfos(missingIndices)
}
*/
