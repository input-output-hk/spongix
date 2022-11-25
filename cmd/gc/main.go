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

	"github.com/alexflint/go-arg"
	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/assembler"
	"github.com/numtide/go-nix/nar"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func main() {
	gc := newGC()
	arg.MustParse(gc)
	gc.setupDesync()
	gc.setupLogger()
	gc.verify()
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
	LogLevel   string `arg:"--log-level,env:LOG_LEVEL" help:"One of debug, info, warn, error, dpanic, panic, fatal"`
	LogMode    string `arg:"--log-mode,env:LOG_MODE" help:"development or production"`
	log        *zap.Logger
	localStore desync.LocalStore
	localIndex desync.LocalIndexStore
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
	lvl := zap.NewAtomicLevel()
	if err := lvl.UnmarshalText([]byte(gc.LogLevel)); err != nil {
		panic(err)
	}
	development := gc.LogMode == "development"
	encoding := "json"
	encoderConfig := zap.NewProductionEncoderConfig()
	if development {
		encoding = "console"
		encoderConfig = zap.NewDevelopmentEncoderConfig()
	}

	l := zap.Config{
		Level:             lvl,
		Development:       development,
		DisableCaller:     false,
		DisableStacktrace: false,
		Sampling:          &zap.SamplingConfig{Initial: 1, Thereafter: 2},
		Encoding:          encoding,
		EncoderConfig:     encoderConfig,
		OutputPaths:       []string{"stderr"},
		ErrorOutputPaths:  []string{"stderr"},
	}

	var err error
	gc.log, err = l.Build()
	if err != nil {
		panic(err)
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
	gc.localIndex = localIndex
}

func measure(metric *metrics.Counter, f func()) {
	start := time.Now()
	f()
	metric.Add(uint64(time.Since(start).Milliseconds()))
}

func (gc *GC) gc() {
	cacheStat := map[string]*chunkStat{}
	measure(metricGcTime, func() { gc.gcOnce(cacheStat) })
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
// amount of space reserved.
const maxCacheDirPortion = 0xffff * 4096

type integrityCheck struct {
	path  string
	index desync.Index
}

func checkNarContents(store desync.Store, idx desync.Index) error {
	buf := assembler.NewAssembler(store, idx)
	narRd := nar.NewReader(buf)
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
func (gc *GC) gcOnce(cacheStat map[string]*chunkStat) {
	log := gc.log.Named("gc")
	maxCacheSize := (uint64(math.Pow(2, 30)) * gc.CacheSize) - maxCacheDirPortion
	store := gc.localStore
	indices := gc.localIndex
	lru := NewLRU(maxCacheSize)
	walkStoreStart := time.Now()
	chunkDirs := int64(0)

	metricMaxSize.Set(int64(maxCacheSize))
	log.Info("GC started", zap.Uint64("maxSize", maxCacheSize))

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
			log.Error("getting chunk", zap.Error(err), zap.String("chunk", id.String()))
			lru.AddDead(stat)
		} else {
			lru.Add(stat)
		}

		return nil
	})

	chunkWalkDuration := time.Since(walkStoreStart)
	metricChunkWalk.Add(uint64(chunkWalkDuration.Milliseconds()))
	metricChunkDirs.Set(chunkDirs)

	if walkStoreErr != nil {
		log.Error("While walking store", zap.Error(walkStoreErr))
		return
	}

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
				case <-time.After(5 * time.Minute):
					return
				case check := <-integrity:
					switch filepath.Ext(check.path) {
					case "":
						return
					case ".nar":
						if err := checkNarContents(store, check.index); err != nil {
							log.Error("checking NAR contents", zap.Error(err), zap.String("path", check.path))
							deadIndices.Store(check.path, yes)
						}
					case ".narinfo":
						if _, err := assembler.AssembleNarinfo(store, check.index); err != nil {
							log.Error("checking narinfo", zap.Error(err), zap.String("path", check.path))
							deadIndices.Store(check.path, yes)
						}
					}
				}
			}
		}(i)
	}

	walkIndicesErr := filepath.Walk(indices.Path, func(path string, info fs.FileInfo, err error) error {
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
			deadIndices.Store(path, yes)
		} else {
			for _, indexChunk := range index.Chunks {
				if lru.IsDead(indexChunk.ID) {
					log.Debug("some chunks are dead", zap.String("path", path))
					deadIndices.Store(path, yes)
					break
				}
			}
		}

		return nil
	})

	integrity <- integrityCheck{path: "", index: desync.Index{}}
	wg.Wait()
	close(integrity)

	metricIndexCount.Set(indicesCount)
	metricIndexWalk.Add(uint64(time.Since(walkIndicesStart).Milliseconds()))
	metricInflated.Set(inflatedSize)

	if walkIndicesErr != nil {
		log.Error("While walking index", zap.Error(walkIndicesErr))
		return
	}
	deadIndexCount := uint64(0)
	// time.Sleep(10 * time.Minute)
	deadIndices.Range(func(key, value interface{}) bool {
		path := key.(string)
		log.Info("deleting index", zap.String("path", path))
		_ = os.Remove(path)
		deadIndexCount++
		return true
	})

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
