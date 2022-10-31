package main

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/folbricht/desync"
	"github.com/nix-community/go-nix/pkg/nar"
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
	metricChunkDirs    = metrics.MustInteger("spongix_chunk_dir_count", "Number of directories the chunks are stored in")

	metricIndexCount   = metrics.MustInteger("spongix_index_count_local", "Number of indices")
	metricIndexGcCount = metrics.MustCounter("spongix_index_gc_count_local", "Number of indices deleted by GC")
	metricIndexWalk    = metrics.MustCounter("spongix_index_walk_local", "Total time spent walking the index in ms")

	metricInflated   = metrics.MustInteger("spongix_inflated_size_local", "Size of cache in bytes contents if they were inflated")
	metricMaxSize    = metrics.MustInteger("spongix_max_size_local", "Limit for the local cache in bytes")
	metricGcTime     = metrics.MustCounter("spongix_gc_time_local", "Total time spent in GC")
	metricVerifyTime = metrics.MustCounter("spongix_verify_time_local", "Total time spent in verification")
)

var yes = struct{}{}

func measure(metric *metrics.Counter, f func()) {
	start := time.Now()
	f()
	metric.Add(uint64(time.Since(start).Milliseconds()))
}

func (proxy *Proxy) gc() {
	proxy.log.Debug("Initializing GC", zap.Duration("interval", proxy.GcInterval))
	cacheStat := map[string]*chunkStat{}
	measure(metricGcTime, func() { proxy.gcOnce(cacheStat) })

	ticker := time.NewTicker(proxy.GcInterval)
	for range ticker.C {
		measure(metricGcTime, func() { proxy.gcOnce(cacheStat) })
	}
}

func (proxy *Proxy) verify() {
	proxy.log.Debug("Initializing Verifier", zap.Duration("interval", proxy.VerifyInterval))
	measure(metricVerifyTime, func() { proxy.verifyOnce() })

	ticker := time.NewTicker(proxy.VerifyInterval)
	for range ticker.C {
		measure(metricVerifyTime, func() { proxy.verifyOnce() })
	}
}

func (proxy *Proxy) verifyOnce() {
	log := proxy.log.Named("verify").Sugar()
	log.Info("store verify started")
	store := proxy.localStore.(desync.LocalStore)
	err := store.Verify(context.Background(), runtime.GOMAXPROCS(0), true, os.Stderr)

	if err != nil {
		log.Error("store verify failed", zap.Error(err))
	} else {
		log.Info("store verify completed")
	}
}

type chunkStat struct {
	id    desync.ChunkID
	size  int64
	mtime time.Time
}

type chunkLRU struct {
	live        []*chunkStat
	liveSize    uint64
	liveSizeMax uint64
	dead        map[desync.ChunkID]struct{}
	deadSize    uint64
}

func NewLRU(liveSizeMax uint64) *chunkLRU {
	return &chunkLRU{
		live:        []*chunkStat{},
		liveSizeMax: liveSizeMax,
		dead:        map[desync.ChunkID]struct{}{},
	}
}

func (l *chunkLRU) AddDead(stat *chunkStat) {
	l.dead[stat.id] = yes
	l.deadSize += uint64(stat.size)
}

func (l *chunkLRU) Add(stat *chunkStat) {
	isOlder := func(i int) bool { return l.live[i].mtime.Before(stat.mtime) }
	i := sort.Search(len(l.live), isOlder)
	l.insertAt(i, stat)
	l.liveSize += uint64(stat.size)
	for l.liveSize > l.liveSizeMax {
		die := l.live[len(l.live)-1]
		l.dead[die.id] = yes
		l.live = l.live[:len(l.live)-1]
		l.deadSize += uint64(die.size)
		l.liveSize -= uint64(die.size)
	}
}

func (l *chunkLRU) insertAt(i int, v *chunkStat) {
	if i == len(l.live) {
		l.live = append(l.live, v)
	} else {
		l.live = append(l.live[:i+1], l.live[i:]...)
		l.live[i] = v
	}
}

func (l *chunkLRU) IsDead(id desync.ChunkID) bool {
	_, found := l.dead[id]
	return found
}

func (l *chunkLRU) Dead() map[desync.ChunkID]struct{} {
	return l.dead
}

// we assume every directory requires 4KB of size (one block) desync stores
// files in directories with a 4 hex prefix, so we need to keep at least this
// amount of space reserved (~256MiB).
const maxCacheDirPortion = 0xffff * 4096
const GiB = 1024 * 1024 * 1024

type integrityCheck struct {
	path  string
	index desync.Index
}

func checkNarContents(store desync.Store, idx desync.Index) error {
	buf := newAssembler(store, idx)
	narRd, err := nar.NewReader(buf)
	if err != nil {
		return errors.WithMessage(err, "creating NAR reader")
	}
	none := true
	for {
		if _, err := narRd.Next(); err == nil {
			none = false
		} else if err == io.EOF {
			break
		} else {
			return err
		}
	}

	if none {
		return errors.New("no contents in NAR")
	}

	return nil
}

/*
Local GC strategies:
  Check every index file:
    If chunks are missing, delete it.
  	If it is not referenced by the database anymore, delete it.
  Check every narinfo in the database:
    If index is missing, delete it.
  	If last access is too old, delete it.
*/
func (proxy *Proxy) gcOnce(cacheStat map[string]*chunkStat) {
	log := proxy.log.Named("gc")
	log.Info("store gc started")
	maxCacheSize := proxy.CacheSize*GiB - maxCacheDirPortion

	var narSizeTotal uint64
	if err := proxy.db.Get(&narSizeTotal, `SELECT SUM(nar_size) FROM narinfos;`); err != nil {
		log.Error("Calculating sum of nar_size", zap.Error(err))
		return
	}
	log.Info("Sum of nar_size", zap.Float64("GiB", float64(narSizeTotal)/GiB), zap.Uint64("bytes", narSizeTotal))

	// This only calculates the lower bound, the actual size occupied by the NARs
	// is usually much smaller than what we have stored in the narinfos.
	// Thus it's impossible to efficiently obtain the list of narinfos and NARs
	// that should be garbage collected.
	// Also, if we actually delete a chunk, this means that all other indices
	// that reference the chunk become invalid.
	// Another issue is that the same narinfo may exist in multiple namespaces,
	// thus deleting it in one namespace must never delete chunks.
	//
	// The naïve approach to garbage collection is:
	// * Obtain index of the NAR (A.NAR) pointed to by the narinfo (A) with the oldest atime
	// * If there exists another narinfo (B) with the same name, simply delete A and try the next one.
	// * Iterate all NAR indices, get the NAR chunk IDs that intersect with the A.NAR chunk IDs.
	// * Delete A (race condition?)
	// * Delete the chunks that are not in the intersection
	// * Increment the GC counter by the sizes of the deleted chunks
	// * Iterate these steps until we are below the desired maximum cache size.
	//
	// How can we improve on this?
	// * Use the naïve approach, but do it over the oldest N narinfos at a time
	//	 This would improve the time requirements and memory usage somewhat.
	//
	// * Actually store the desync.Index in the database, so we could run queries
	//   over the chunk IDs. This is not supported by desync itself, and may
	//   require a lot of work.
	//
	// * Store atime and size for chunks in the database, thus we could query the
	//   minimum amount of chunks to GC, then iterate once over the indices and
	//   eliminate the ones referencing GC'd chunks.
	//   In theory this will only affect narinfos that have an atime before the
	//   deleted chunks.
	//   The drawback is that we have the overhead of updating the atime of every
	//   chunk that is requested, which could be done more efficiently on the
	//   filesystem level. But we cannot guarantee that atime is actually being
	//   recorded in the FS.
	//
	// * Store chunks and indices in the DB. This would reduce the directory size
	//   overhead, maybe it's even possible to concat chunks within the database
	//   query. Not sure how this would behave with multi-gigabyte NARs though.
	rows, err := proxy.db.Queryx(`
		SELECT id, url, namespace, nar_size, acc FROM (
			SELECT id, url, namespace, nar_size, SUM(nar_size)
			OVER (ORDER BY atime DESC) AS acc
			FROM narinfos
		) n
		WHERE acc > ?;
  `, proxy.CacheSize*GiB)
	if err != nil {
		log.Error("Querying narinfos", zap.Error(err))
		return
	}
	defer rows.Close()

	store := proxy.localStore.(desync.LocalStore)
	indices := proxy.localIndices

	var total int64
	chunks := map[desync.ChunkID]uint64{}
	for rows.Next() {
		var id, url, namespace string
		var narSize, narSizeSum int64
		if err := rows.Scan(&id, &url, &namespace, &narSize, &narSizeSum); err != nil {
			log.Error("Scanning narinfo row", zap.Error(err))
			return
		}

		total += narSize

		if index, ok := indices[namespace]; ok {
			if idx, err := index.GetIndex(url); err != nil {
				log.Error("Looking up index", zap.Error(err))
				continue
			} else {
				for _, chunk := range idx.Chunks {
					chunks[chunk.ID] = chunk.Size
				}
			}
		}
	}

	var chunkTotal uint64
	for _, chunkSize := range chunks {
		chunkTotal += chunkSize
	}

	pp(float64(chunkTotal) / GiB)
	pp(float64(maxCacheSize)/GiB, float64(narSizeTotal)/GiB, float64(total)/GiB)
	pp(float64(narSizeTotal)/GiB - float64(total)/GiB)

	return

	// store := proxy.localStore.(desync.LocalStore)
	// indices := proxy.localIndices
	lru := NewLRU(maxCacheSize)
	walkStoreStart := time.Now()
	chunkDirs := int64(0)

	metricMaxSize.Set(int64(maxCacheSize))

	// filepath.Walk is faster for our usecase because we need the stat result anyway.
	walkStoreErr := filepath.Walk(store.Base, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			if err == os.ErrNotExist {
				return nil
			} else {
				return err
			}
		}

		if info.IsDir() {
			chunkDirs++
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

		stat := &chunkStat{id: id, size: info.Size(), mtime: info.ModTime()}

		if _, err := store.GetChunk(id); err != nil {
			proxy.log.Error("getting chunk", zap.Error(err), zap.String("chunk", id.String()))
			lru.AddDead(stat)
		} else {
			lru.Add(stat)
		}

		return nil
	})

	metricChunkWalk.Add(uint64(time.Since(walkStoreStart).Milliseconds()))
	metricChunkDirs.Set(chunkDirs)

	if walkStoreErr != nil {
		proxy.log.Error("While walking store", zap.Error(walkStoreErr))
		return
	}

	metricChunkCount.Set(int64(len(lru.live)))
	metricChunkGcCount.Add(uint64(len(lru.dead)))
	metricChunkGcSize.Add(lru.deadSize)
	metricChunkSize.Set(int64(lru.liveSize))

	deadIndices := &sync.Map{}
	walkIndicesStart := time.Now()
	indicesCount := int64(0)
	inflatedSize := int64(0)
	ignoreBeforeTime := time.Now().Add(10 * time.Minute)

	integrity := make(chan integrityCheck)
	wg := &sync.WaitGroup{}

	for i := 0; i < 3; i++ {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			for {
				select {
				case <-time.After(1 * time.Hour):
					return
				case check := <-integrity:
					switch filepath.Ext(check.path) {
					case ".nar":
						if err := checkNarContents(store, check.index); err != nil {
							proxy.log.Error("checking NAR contents", zap.Error(err), zap.String("path", check.path))
							deadIndices.Store(check.path, yes)
							continue
						}
					case ".narinfo":
						if _, err := assembleNarinfo(store, check.index); err != nil {
							proxy.log.Error("checking narinfo", zap.Error(err), zap.String("path", check.path))
							deadIndices.Store(check.path, yes)
						}
					}
				}
			}
		}(i)
	}

	for _, index := range indices {
		index := index.(desync.LocalIndexStore)

		walkIndicesErr := filepath.Walk(index.Path, func(path string, info fs.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}

			isOld := info.ModTime().Before(ignoreBeforeTime)

			ext := filepath.Ext(path)
			isNar := ext == ".nar"
			isNarinfo := ext == ".narinfo"

			if !(isNar || isNarinfo || isOld) {
				return nil
			}

			name := path[len(index.Path):]

			index, err := index.GetIndex(name)
			if err != nil {
				return errors.WithMessagef(err, "while getting index %s", name)
			}

			integrity <- integrityCheck{path: path, index: index}

			inflatedSize += index.Length()
			indicesCount++

			if len(index.Chunks) == 0 {
				proxy.log.Debug("index chunks are empty", zap.String("path", path))
				deadIndices.Store(path, yes)
			} else {
				for _, indexChunk := range index.Chunks {
					if lru.IsDead(indexChunk.ID) {
						proxy.log.Debug("some chunks are dead", zap.String("path", path))
						deadIndices.Store(path, yes)
						break
					}
				}
			}

			return nil
		})

		wg.Wait()
		close(integrity)

		metricIndexCount.Set(indicesCount)
		metricIndexWalk.Add(uint64(time.Since(walkIndicesStart).Milliseconds()))
		metricInflated.Set(inflatedSize)

		if walkIndicesErr != nil {
			proxy.log.Error("While walking index", zap.Error(walkIndicesErr))
			return
		}
	}

	deadIndexCount := uint64(0)
	deadIndices.Range(func(key, value interface{}) bool {
		path := key.(string)
		proxy.log.Debug("moving index to trash", zap.String("path", path))
		_ = os.Remove(path)
		deadIndexCount++
		return true
	})

	metricIndexGcCount.Add(deadIndexCount)

	// we don't use store.Prune because it does another filepath.Walk and no
	// added benefit for us.

	for id := range lru.Dead() {
		if err := store.RemoveChunk(id); err != nil {
			proxy.log.Error("Removing chunk", zap.Error(err), zap.String("id", id.String()))
		}
	}

	proxy.log.Debug(
		"GC stats",
		zap.Uint64("live_bytes", lru.liveSize),
		zap.Uint64("live_max_bytes", lru.liveSizeMax),
		zap.Int("live_chunk_count", len(lru.live)),
		zap.Uint64("dead_bytes", lru.deadSize),
		zap.Int("dead_chunk_count", len(lru.dead)),
		zap.Uint64("dead_index_count", deadIndexCount),
		zap.Duration("walk_indices_time", time.Since(walkIndicesStart)),
	)
}
