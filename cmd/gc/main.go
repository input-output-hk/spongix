package main

import (
	"context"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	arg "github.com/alexflint/go-arg"
	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/assembler"
	"github.com/input-output-hk/spongix/pkg/logger"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func main() {
	gc := newGC()
	arg.MustParse(gc)
	gc.setupDesync()
	gc.setupLogger()
	if gc.Verify {
		gc.verify()
	}
	gc.gc()
}

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

	defaultStoreOptions = desync.StoreOptions{
		N:            1,
		Timeout:      1 * time.Second,
		ErrorRetry:   0,
		Uncompressed: false,
		SkipVerify:   false,
	}
)

var yes = struct{}{}

type GC struct {
	Dir        string `arg:"--dir,env:CACHE_DIR" help:"directory for the cache"`
	CacheSize  uint64 `arg:"--cache-size,env:CACHE_SIZE" help:"Number of gigabytes to keep in the disk cache"`
	Verify     bool   `arg:"--verify" help:"Run store integrity verification before GC"`
	LogLevel   string `arg:"--log-level,env:LOG_LEVEL" help:"One of debug, info, warn, error, dpanic, panic, fatal"`
	LogMode    string `arg:"--log-mode,env:LOG_MODE" help:"development or production"`
	log          *zap.Logger
	localStore   desync.LocalStore
	localIndices []desync.LocalIndexStore
}

func newGC() *GC {
	devLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	return &GC{
		Dir:      "./cache",
		LogLevel: "debug",
		LogMode:  "production",
		log:      devLog,
	}
}

func (gc *GC) setupLogger() {
	if log, err := logger.SetupLogger(gc.LogMode, gc.LogLevel); err != nil {
		panic(err)
	} else {
		gc.log = log
	}
}

func (gc *GC) setupDesync() {
	storeDir := filepath.Join(gc.Dir, "store")
	localStore, err := desync.NewLocalStore(storeDir, defaultStoreOptions)
	if err != nil {
		gc.log.Fatal("failed creating local store", zap.Error(err), zap.String("dir", storeDir))
	}
	localStore.UpdateTimes = true

	indexDir := filepath.Join(gc.Dir, "index")
	localIndex, err := desync.NewLocalIndexStore(indexDir)
	if err != nil {
		gc.log.Fatal("failed creating local index", zap.Error(err), zap.String("dir", indexDir))
	}

	gc.localStore = localStore
	gc.localIndices = []desync.LocalIndexStore{localIndex}
}

func measure(metric *metrics.Counter, f func()) {
	start := time.Now()
	f()
	metric.Add(uint64(time.Since(start).Milliseconds()))
}

func (gc *GC) gc() {
	measure(metricGcTime, func() { gc.gcOnce() })
}

func (gc *GC) verify() {
	measure(metricVerifyTime, func() { gc.verifyOnce() })
}

func (gc *GC) verifyOnce() {
	gc.log.Info("store verify started")
	store := gc.localStore
	err := store.Verify(context.Background(), runtime.GOMAXPROCS(0), true, os.Stderr)

	if err != nil {
		gc.log.Error("store verify failed", zap.Error(err))
	} else {
		gc.log.Info("store verify completed")
	}
}

type chunkStat struct {
	id        desync.ChunkID // [32]byte, no pointers
	size      int64
	mtimeNano int64 // UnixNano, avoids time.Time's *Location pointer
}

type chunkLRU struct {
	live        []chunkStat
	liveSize    uint64
	liveSizeMax uint64
	dead        map[desync.ChunkID]struct{}
	deadSize    uint64
}

func NewLRU(liveSizeMax uint64) *chunkLRU {
	// Estimate chunk count from cache size assuming avg 64KB chunks.
	estimatedChunks := int(liveSizeMax / 65536)
	return &chunkLRU{
		live:        make([]chunkStat, 0, estimatedChunks),
		liveSizeMax: liveSizeMax,
		dead:        map[desync.ChunkID]struct{}{},
	}
}

func (l *chunkLRU) AddDead(stat chunkStat) {
	l.dead[stat.id] = yes
	l.deadSize += uint64(stat.size)
}

func (l *chunkLRU) Add(stat chunkStat) {
	l.live = append(l.live, stat)
	l.liveSize += uint64(stat.size)
}

// Finalize sorts by newest-first and evicts oldest chunks until under budget.
func (l *chunkLRU) Finalize() {
	sort.Slice(l.live, func(i, j int) bool {
		return l.live[i].mtimeNano > l.live[j].mtimeNano
	})
	for l.liveSize > l.liveSizeMax && len(l.live) > 0 {
		last := l.live[len(l.live)-1]
		l.dead[last.id] = yes
		l.deadSize += uint64(last.size)
		l.liveSize -= uint64(last.size)
		l.live = l.live[:len(l.live)-1]
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
// amount of space reserved.
const maxCacheDirPortion = 0xffff * 4096

type integrityCheck struct {
	path  string
	index desync.Index
}

// kindFromPath extracts the index kind (narinfo, nar, realisations, log)
// from the path. Index files are stored as indices/{kind}/{hash[0:4]}/{hash}
// with no file extension.
func kindFromPath(path string) string {
	parts := strings.Split(path, string(filepath.Separator))
	for i, part := range parts {
		if part == "indices" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func checkNarContents(store desync.Store, idx desync.Index) error {
	buf := assembler.NewAssembler(store, idx)
	narRd, err := nar.NewReader(buf)
	if err != nil {
		return err
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
func (gc *GC) gcOnce() {
	log := gc.log.Named("gc")
	maxCacheSize := (uint64(math.Pow(2, 30)) * gc.CacheSize) - maxCacheDirPortion
	store := gc.localStore
	lru := NewLRU(maxCacheSize)
	walkStoreStart := time.Now()
	chunkDirs := int64(0)

	metricMaxSize.Set(int64(maxCacheSize))
	log.Info("GC started", zap.Uint64("maxSize", maxCacheSize))

	walkStoreErr := filepath.WalkDir(store.Base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if err == os.ErrNotExist {
				return nil
			}
			return err
		}

		if d.IsDir() {
			chunkDirs++
			return nil
		}

		name := d.Name()
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

		// Only stat files we actually need (WalkDir avoids lstat for filtered-out files).
		info, err := d.Info()
		if err != nil {
			return err
		}

		stat := chunkStat{id: id, size: info.Size(), mtimeNano: info.ModTime().UnixNano()}
		lru.Add(stat)

		return nil
	})

	chunkWalkDuration := time.Since(walkStoreStart)
	metricChunkWalk.Add(uint64(chunkWalkDuration.Milliseconds()))
	metricChunkDirs.Set(chunkDirs)

	if walkStoreErr != nil {
		log.Error("While walking store", zap.Error(walkStoreErr))
		return
	}

	lru.Finalize()

	metricChunkCount.Set(int64(len(lru.live)))
	metricChunkGcCount.Add(uint64(len(lru.dead)))
	metricChunkGcSize.Add(lru.deadSize)
	metricChunkSize.Set(int64(lru.liveSize))
	log.Info("chunk walk done",
		zap.Duration("duration", chunkWalkDuration),
		zap.Int64("dirs", chunkDirs),
		zap.Int("live chunks", len(lru.live)),
		zap.Uint64("live size", lru.liveSize),
		zap.Uint64("dead size", lru.deadSize),
		zap.Int("dead chunks", len(lru.dead)),
	)

	var deadMu sync.Mutex
	deadIndices := map[string]struct{}{}
	walkIndicesStart := time.Now()
	indicesCount := int64(0)
	inflatedSize := int64(0)
	ignoreBeforeTime := time.Now().Add(-10 * time.Minute)

	integrity := make(chan integrityCheck, 64)
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for check := range integrity {
				kind := kindFromPath(check.path)
				switch kind {
				case "nar":
					if err := checkNarContents(store, check.index); err != nil {
						log.Error("checking NAR contents", zap.Error(err), zap.String("path", check.path))
						deadMu.Lock()
						deadIndices[check.path] = yes
						deadMu.Unlock()
					}
				case "narinfo":
					if _, err := assembler.AssembleNarinfo(store, check.index); err != nil {
						log.Error("checking narinfo", zap.Error(err), zap.String("path", check.path))
						deadMu.Lock()
						deadIndices[check.path] = yes
						deadMu.Unlock()
					}
				}
			}
		}()
	}

	var walkIndicesErr error
	for _, indices := range gc.localIndices {
		err := filepath.WalkDir(indices.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}

			info, err := d.Info()
			if err != nil {
				return err
			}
			isOld := info.ModTime().Before(ignoreBeforeTime)

			kind := kindFromPath(path)
			isNar := kind == "nar"
			isNarinfo := kind == "narinfo"

			if !(isNar || isNarinfo || isOld) {
				return nil
			}

			name := path[len(indices.Path):]

			index, err := indices.GetIndex(name)
			if err != nil {
				return errors.WithMessagef(err, "while getting index %s", name)
			}

			integrity <- integrityCheck{path: path, index: index}

			inflatedSize += index.Length()
			indicesCount++

			if len(index.Chunks) == 0 {
				log.Debug("index chunks are empty", zap.String("path", path))
				deadMu.Lock()
				deadIndices[path] = yes
				deadMu.Unlock()
			} else {
				for _, indexChunk := range index.Chunks {
					if lru.IsDead(indexChunk.ID) {
						log.Debug("some chunks are dead", zap.String("path", path))
						deadMu.Lock()
						deadIndices[path] = yes
						deadMu.Unlock()
						break
					}
				}
			}

			return nil
		})
		if err != nil {
			walkIndicesErr = err
			break
		}
	}

	close(integrity)
	wg.Wait()

	metricIndexCount.Set(indicesCount)
	metricIndexWalk.Add(uint64(time.Since(walkIndicesStart).Milliseconds()))
	metricInflated.Set(inflatedSize)

	if walkIndicesErr != nil {
		log.Error("While walking index", zap.Error(walkIndicesErr))
		return
	}
	deadIndexCount := uint64(0)
	for path := range deadIndices {
		log.Info("deleting index", zap.String("path", path))
		if err := os.Remove(path); err != nil {
			log.Error("failed deleting index", zap.Error(err), zap.String("path", path))
		}
		deadIndexCount++
	}

	metricIndexGcCount.Add(deadIndexCount)

	// we don't use store.Prune because it does another filepath.Walk and no
	// added benefit for us.

	for id := range lru.Dead() {
		if err := store.RemoveChunk(id); err != nil {
			log.Error("Removing chunk", zap.Error(err), zap.String("id", id.String()))
		}
	}

	log.Info(
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
