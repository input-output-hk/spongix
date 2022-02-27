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
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

var (
	metricChunkCount   = metrics.MustInteger("spongix_chunk_count_local", "Number of chunks")
	metricChunkGcCount = metrics.MustCounter("spongix_chunk_gc_count_local", "Number of chunks deleted by GC")
	metricChunkGcSize  = metrics.MustCounter("spongix_chunk_gc_bytes_local", "Size of chunks deleted by GC")
	metricChunkSize    = metrics.MustInteger("spongix_chunk_size_local", "Size of the chunks in bytes")
	metricChunkWalk    = metrics.MustCounter("spongix_chunk_walk_local", "Total time spent walking the cache in ms")

	metricIndexCount   = metrics.MustInteger("spongix_index_count_local", "Number of indices")
	metricIndexGcCount = metrics.MustCounter("spongix_index_gc_count_local", "Number of indices deleted by GC")
	metricIndexGcSize  = metrics.MustCounter("spongix_index_gc_bytes_local", "Size of indices deleted by GC")
	metricIndexSize    = metrics.MustInteger("spongix_index_size_local", "Size of index files in bytes")
	metricIndexWalk    = metrics.MustCounter("spongix_index_walk_local", "Total time spent walking the index in ms")

	metricMaxSize    = metrics.MustInteger("spongix_max_size_local", "Limit for the local cache in bytes")
	metricGcTime     = metrics.MustCounter("spongix_gc_time_local", "Total time spent in GC")
	metricVerifyTime = metrics.MustCounter("spongix_verify_time_local", "Total time spent in verification")
)

func measure(metric *metrics.Counter, f func()) {
	start := time.Now()
	f()
	metric.Add(uint64(time.Since(start).Milliseconds()))
}

func (proxy *Proxy) gc() {
	proxy.log.Debug("Initializing GC", zap.Duration("interval", proxy.GcInterval))
	cacheStat := map[string]*ChunkStat{}
	measure(metricGcTime, func() { proxy.gcOnce(cacheStat) })

	ticker := time.NewTicker(proxy.GcInterval)
	for {
		<-ticker.C
		measure(metricGcTime, func() { proxy.gcOnce(cacheStat) })
	}
}

func (proxy *Proxy) verify() {
	proxy.log.Debug("Initializing Verifier", zap.Duration("interval", proxy.VerifyInterval))

	ticker := time.NewTicker(proxy.VerifyInterval)
	for {
		<-ticker.C
		measure(metricVerifyTime, func() { proxy.verifyOnce() })
	}
}

func (proxy *Proxy) verifyOnce() {
	store := proxy.localStore.(desync.LocalStore)
	err := store.Verify(context.Background(), 1, false, os.Stderr)

	if err != nil {
		proxy.log.Error("store verify failed", zap.Error(err))
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
func (proxy *Proxy) gcOnce(cacheStat map[string]*ChunkStat) {
	// Keep cache size on disk below this size
	maxCacheSize := (uint64(math.Pow(2, 30)) * proxy.CacheSize) - maxCacheDirPortion
	metricMaxSize.Set(int64(maxCacheSize))

	store := proxy.localStore.(desync.LocalStore)
	indices := proxy.localIndex.(desync.LocalIndexStore)

	chunkStats := []*ChunkStat{}
	onDiskSize := uint64(0)
	startWalkStore := time.Now()

	err := filepath.WalkDir(store.Base, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if chunkStat, found := cacheStat[path]; found {
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
			cacheStat[path] = chunkStat
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
	indicesSize := int64(0)
	indicesCount := int64(0)
	indicesToDeleteSize := uint64(0)

	startIndexWalk := time.Now()

	err = filepath.WalkDir(indices.Path, func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return errors.WithMessage(err, "walking indices")
		}
		if info.IsDir() {
			return nil
		}

		narHash := info.Name()
		index, err := indices.GetIndex(narHash)
		if err != nil {
			return errors.WithMessage(err, "getting index from narHash")
		}

		indicesSize += int64(index.Length())
		indicesCount++

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

	metricChunkCount.Set(int64(len(oldChunks) + len(newChunks)))
	metricChunkGcCount.Add(uint64(len(oldChunks)))
	metricChunkGcSize.Add(chunksToDeleteSize)
	metricChunkSize.Set(int64(onDiskSize))
	metricChunkWalk.Add(uint64(time.Since(startWalkStore).Milliseconds()))

	metricIndexCount.Set(indicesCount)
	metricIndexGcCount.Add(uint64(len(indicesToDelete)))
	metricIndexGcSize.Add(indicesToDeleteSize)
	metricIndexSize.Set(indicesSize)
	metricIndexWalk.Add(uint64(time.Since(startIndexWalk).Milliseconds()))

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
	indexDir := proxy.localIndex.(desync.LocalIndexStore).Path

	for narHash := range narinfos {
		if narHash == "" {
			continue
		}
		proxy.log.Debug("Delete narinfo", zap.String("narHash", narHash), zap.String("indexDir", indexDir))

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

		indexFilePath := filepath.Join(indexDir, narHash)
		if err = os.Remove(indexFilePath); err != nil && !os.IsNotExist(err) {
			proxy.log.Error("failed to remove index", zap.String("path", indexFilePath), zap.String("name", name), zap.String("hash", narHash), zap.Error(err))
			return err
		}

		proxy.log.Debug("Deleted narinfo", zap.String("path", indexFilePath))
	}

	return nil
}
