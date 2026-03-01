package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/alitto/pond"
	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/config"
	"github.com/input-output-hk/spongix/pkg/logger"
	"github.com/input-output-hk/spongix/pkg/store"
	"github.com/minio/minio-go/v6"
	"github.com/minio/minio-go/v6/pkg/credentials"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"go.uber.org/zap"
)

func main() {
	// cpuprofile := "spongix.pprof"
	// f, err := os.Create(cpuprofile)
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// pprof.StartCPUProfile(f)
	// defer pprof.StopCPUProfile()

	cli := &config.CLI{}
	arg.MustParse(cli)
	cli.File = os.ExpandEnv(cli.File)

	c, err := config.LoadFile(cli.File)
	if err != nil {
		panic(err)
	}

	proxy := NewProxy(c)

	if err := proxy.config.Prepare(); err != nil {
		log.Fatal(err)
	}

	proxy.setupLogger()
	proxy.setupChunks()
	proxy.setupIndices()

	// Start in-process GC goroutine if configured and using Pebble.
	var gcTicker *time.Ticker
	if c.GC != nil && c.GC.Interval != "" && proxy.pebbleDB != nil {
		gcInterval, err := time.ParseDuration(c.GC.Interval)
		if err != nil {
			log.Fatalf("invalid gc.interval %q: %v", c.GC.Interval, err)
		}
		var gcRetention time.Duration
		if c.GC.RetentionPeriod != "" {
			gcRetention, err = time.ParseDuration(c.GC.RetentionPeriod)
			if err != nil {
				log.Fatalf("invalid gc.retention_period %q: %v", c.GC.RetentionPeriod, err)
			}
		}
		gcTicker = time.NewTicker(gcInterval)
		go func() {
			for range gcTicker.C {
				filePins, err := store.LoadPinnedHashes(c.GC.PinnedHashesFile)
				if err != nil {
					proxy.log.Error("loading pinned hashes file", zap.Error(err))
					continue
				}
				gcCfg := store.GCConfig{
					DryRun:          c.GC.DryRun,
					PinnedHashes:    filePins,
					RetentionPeriod: gcRetention,
					MaxBytes:        c.GC.MaxBytes,
				}
				stats, err := store.GC(proxy.pebbleDB, gcCfg)
				if err != nil {
					proxy.log.Error("GC failed", zap.Error(err))
					continue
				}
				proxy.log.Info("GC complete",
					zap.Int("reachable_narinfos", stats.ReachableNarinfos),
					zap.Int("unreachable_narinfos", stats.UnreachableNarinfos),
					zap.Int("deleted_indices", stats.DeletedIndices),
					zap.Int("orphan_chunks", stats.OrphanChunks),
					zap.Int("deleted_chunks", stats.DeletedChunks),
					zap.Uint64("deleted_bytes", stats.DeletedBytes),
					zap.Int("size_evicted_indices", stats.SizeEvictedIndices),
					zap.Uint64("size_evicted_bytes", stats.SizeEvictedBytes),
					zap.Bool("dry_run", c.GC.DryRun),
				)
			}
		}()
	}

	if c.SigningKeyFile != "" {
		raw, err := os.ReadFile(c.SigningKeyFile)
		if err != nil {
			log.Fatal("reading signing key file", zap.Error(err))
		}
		key, err := signature.LoadSecretKey(strings.TrimSpace(string(raw)))
		if err != nil {
			log.Fatal("loading signing key", zap.Error(err))
		}
		proxy.signingKey = &key
		proxy.log.Info("loaded signing key", zap.String("name", key.ToPublicKey().Name))
	}

	go func() {
		t := time.Tick(5 * time.Second)
		for range t {
			if err := proxy.log.Sync(); err != nil {
				if err.Error() != "sync /dev/stderr: invalid argument" {
					log.Printf("failed to sync zap: %s", err)
				}
			}
		}
	}()

	// nolint
	defer proxy.log.Sync()

	const timeout = 15 * time.Minute

	srv := &http.Server{
		Handler:      proxy.router(),
		Addr:         proxy.config.Listen,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(
		sc,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
	)

	if proxy.config.PProfListen != "" {
		go func() {
			proxy.log.Info("pprof listening", zap.String("listen", proxy.config.PProfListen))
			if err := http.ListenAndServe(proxy.config.PProfListen, nil); err != nil {
				proxy.log.Error("pprof listener failed", zap.Error(err))
			}
		}()
	}

	go func() {
		proxy.log.Info("Server starting", zap.String("listen", proxy.config.Listen))
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			// Only log an error if it's not due to shutdown or close
			proxy.log.Fatal("error bringing up listener", zap.Error(err))
		}
	}()

	// Load symlink binary for registry tarballs (if configured).
	proxy.loadSymlinkBin()

	<-sc
	signal.Stop(sc)

	// Shutdown timeout should be max request timeout (with 1s buffer).
	ctxShutDown, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := srv.Shutdown(ctxShutDown); err != nil {
		proxy.log.Error("server shutdown error", zap.Error(err))
	}

	// Stop GC ticker before draining pools.
	if gcTicker != nil {
		gcTicker.Stop()
	}

	// Drain worker pools before closing stores.
	// cachePool first (it writes to stores), then headPool (read-only).
	proxy.log.Info("draining worker pools")
	proxy.cachePool.StopAndWait()
	proxy.headPool.StopAndWait()

	// Flush Pebble memtable to SSTables, then close.
	// Critical when WAL is on tmpfs: unflushed memtable data is lost
	// once the tmpfs is wiped on restart.
	if proxy.pebbleDB != nil {
		if err := proxy.pebbleDB.Flush(); err != nil {
			proxy.log.Error("flushing pebble db", zap.Error(err))
		}
		if err := proxy.pebbleDB.Close(); err != nil {
			proxy.log.Error("closing pebble db", zap.Error(err))
		}
	}

	proxy.log.Info("server shutdown gracefully")
}

type Proxy struct {
	config *config.Config

	chunkStore   desync.WriteStore
	indexStores map[string]desync.IndexWriteStore

	log       *zap.Logger
	headPool  *pond.WorkerPool
	cachePool *pond.WorkerPool

	httpHeadClient *http.Client
	httpGetClient  *http.Client

	// pebbleDB is the shared Pebble database for chunks and indices.
	pebbleDB *store.DB

	signingKey     *signature.SecretKey // nil if no key configured
	symlinkBinData []byte              // raw symlink binary for registry tarballs
}

func NewProxy(config *config.Config) *Proxy {
	devLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	httpHeadClient := &http.Client{}
	httpHeadClient.Timeout = 1 * time.Second

	httpGetClient := &http.Client{}
	httpGetClient.Timeout = 0 // disabled — let context govern timeouts

	return &Proxy{
		config:         config,
		log:            devLog,
		headPool:       pond.New(10, 1000),
		cachePool:      pond.New(10, 1000),
		indexStores:      map[string]desync.IndexWriteStore{},
		httpHeadClient: httpHeadClient,
		httpGetClient:  httpGetClient,
	}
}

var (
	buildVersion        = "dev"
	buildCommit         = "dirty"
	symlinkBinStorePath = "" // set by ldflags: store path of the symlink binary
)

func (proxy *Proxy) Version() string {
	return buildVersion + " (" + buildCommit + ")"
}

func (proxy *Proxy) setupChunks() {
	switch proxy.config.Chunks.StoreType {
	case "s3":
		s3 := proxy.config.Chunks.S3
		if s3.Url == "" {
			proxy.log.Fatal("No S3 URL given, will not upload files")
		}

		if s3.Region == "" {
			proxy.log.Fatal("No S3 region given, will not upload files")
		}

		s3Url, err := url.Parse(s3.Url)
		if err != nil {
			proxy.log.Fatal("couldn't parse S3 URL", zap.Error(err), zap.String("url", s3.Url))
		}

		chunkStore, err := desync.NewS3Store(
			s3Url,
			mkCredentials(s3),
			s3.Region,
			proxy.defaultStoreOptions(),
			minio.BucketLookupAuto)
		if err != nil {
			proxy.log.Fatal("failed creating s3 store",
				zap.Error(err),
				zap.String("url", s3Url.String()),
				zap.String("region", s3.Region),
			)
		}

		proxy.chunkStore = chunkStore

	case "pebble":
		opts := &store.PebbleOptions{
			WALDir:             proxy.config.Chunks.PebbleWALDir,
			MemTableSize:       proxy.config.Chunks.PebbleMemTableSize,
			L0CompactionThresh: proxy.config.Chunks.PebbleL0CompactionThresh,
		}
		db, err := store.Open(proxy.config.Chunks.PebbleDir, opts)
		if err != nil {
			proxy.log.Fatal("failed opening pebble db",
				zap.Error(err),
				zap.String("dir", proxy.config.Chunks.PebbleDir),
			)
		}
		proxy.pebbleDB = db
		proxy.chunkStore = store.NewChunkStore(db)

		if err := db.MigrateRegistryDropOwner(); err != nil {
			proxy.log.Fatal("registry migration failed", zap.Error(err))
		}
	}
}

func (proxy *Proxy) setupIndices() {
	for namespace, ns := range proxy.config.Namespaces {
		switch ns.StoreType {
		case "s3":
			s3 := ns.S3
			s3Url, err := url.Parse(s3.Url)
			if err != nil {
				proxy.log.Fatal("couldn't parse S3 URL", zap.Error(err), zap.String("namespace", namespace), zap.String("url", s3.Url))
			}

			index, err := desync.NewS3IndexStore(
				s3Url,
				mkCredentials(s3),
				s3.Region,
				proxy.defaultStoreOptions(),
				minio.BucketLookupAuto,
			)
			if err != nil {
				proxy.log.Fatal("failed creating s3 index store",
					zap.Error(err),
					zap.String("url", s3.Url),
					zap.String("region", s3.Region),
				)
			}

			proxy.indexStores[namespace] = store.WrapS3IndexStore(index)

		case "pebble":
			if proxy.pebbleDB == nil {
				proxy.log.Fatal("namespace uses pebble store_type but chunks store_type is not pebble",
					zap.String("namespace", namespace),
				)
			}
			proxy.indexStores[namespace] = store.NewIndexStore(proxy.pebbleDB, namespace)
		}
	}
}

func mkCredentials(s3 *config.S3) *credentials.Credentials {
	return credentials.NewFileAWSCredentials(s3.CredentialsFile, s3.Profile)
}

func (proxy *Proxy) defaultStoreOptions() desync.StoreOptions {
	return desync.StoreOptions{
		N:          64,
		Timeout:    30 * time.Second,
		ErrorRetry: 1,
	}
}

// loadSymlinkBin reads the symlink binary from the Nix store path (injected
// via ldflags at build time) and caches it in memory. The binary is included
// directly in registry tarballs so clients don't need a separate fetchClosure.
func (proxy *Proxy) loadSymlinkBin() {
	if symlinkBinStorePath == "" {
		proxy.log.Warn("symlinkBinStorePath not set, registry flakes will not work")
		return
	}

	// Check if any namespace has registry enabled.
	hasRegistry := false
	for _, ns := range proxy.config.Namespaces {
		if ns.Registry != nil && ns.Registry.Enable {
			hasRegistry = true
			break
		}
	}
	if !hasRegistry {
		return
	}

	binPath := filepath.Join(symlinkBinStorePath, "bin", "symlink")
	data, err := os.ReadFile(binPath)
	if err != nil {
		proxy.log.Fatal("reading symlink binary", zap.Error(err), zap.String("path", binPath))
	}

	proxy.symlinkBinData = data
	proxy.log.Info("loaded symlink binary",
		zap.String("path", binPath),
		zap.Int("size", len(data)),
	)
}

func (proxy *Proxy) setupLogger() {
	if log, err := logger.SetupLogger(proxy.config.LogMode, proxy.config.LogLevel); err != nil {
		panic(err)
	} else {
		proxy.log = log
	}
}

func (proxy *Proxy) doCache(req *cacheRequest) {
	metricCacheJobsActive.Add(1)
	defer metricCacheJobsActive.Add(-1)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", req.url, nil)
	if err != nil {
		metricCacheJobs("error").Add(1)
		proxy.log.Error("failed creating request", zap.Error(err), zap.String("url", req.url))
		return
	}

	response, err := proxy.httpGetClient.Do(httpReq)
	if err != nil {
		metricCacheJobs("error").Add(1)
		proxy.log.Error("failed downloading file", zap.Error(err), zap.String("url", req.url))
		return
	}
	defer func() { _ = response.Body.Close() }()

	if !strings.HasPrefix(req.location, "indices/narinfo/") {
		proxy.doCacheNonNarinfo(ctx, req, response.Body)
		return
	}

	// Narinfo path: buffer body, optionally cache the upstream NAR first.
	body, err := io.ReadAll(response.Body)
	if err != nil {
		metricCacheJobs("error").Add(1)
		proxy.log.Error("reading narinfo response", zap.Error(err))
		return
	}

	info, err := narinfo.Parse(bytes.NewReader(body))
	if err != nil {
		proxy.log.Warn("parsing narinfo for eager caching", zap.Error(err))
		metricCacheJobs("error").Add(1)
		proxy.log.Warn("skipping narinfo cache: NAR was not cached",
			zap.String("location", req.location))
		return
	}

	// If the upstream NAR is compressed, download and cache it first.
	if info.Compression != "none" && info.URL != "" {
		if !proxy.cacheNarFromUpstream(ctx, info, req.url, req.namespace) {
			metricCacheJobs("error").Add(1)
			proxy.log.Warn("skipping narinfo cache: NAR was not cached",
				zap.String("location", req.location))
			return
		}
	}

	// NAR is cached (or was already uncompressed); now store the narinfo.
	if err := proxy.insert(ctx, req.namespace, req.location, bytes.NewReader(body)); err != nil {
		metricCacheJobs("error").Add(1)
		proxy.log.Error("failed caching narinfo",
			zap.String("location", req.location),
			zap.Error(err))
		return
	}
	metricCacheJobs("success").Add(1)
}

// cacheNarFromUpstream downloads a compressed NAR from the upstream URL derived
// from the narinfo, decompresses it, and inserts it into the local store.
// Returns true if the NAR was successfully cached.
func (proxy *Proxy) cacheNarFromUpstream(ctx context.Context, info *narinfo.NarInfo, reqURL, namespace string) bool {
	baseURL := extractBaseURL(reqURL)
	narURL, err := url.JoinPath(baseURL, info.URL)
	if err != nil {
		proxy.log.Error("constructing NAR URL",
			zap.String("base", baseURL),
			zap.String("narPath", info.URL),
			zap.Error(err))
		return false
	}

	narHashStr := info.NarHash.String()
	narHashParts := strings.SplitN(narHashStr, ":", 2)
	if len(narHashParts) != 2 {
		proxy.log.Error("malformed NarHash in narinfo", zap.String("narhash", narHashStr))
		return false
	}

	narHash52 := narHashParts[1]
	narLocation := fmt.Sprintf("indices/nar/%s/%s", narHash52[:4], narHash52)

	narReq, err := http.NewRequestWithContext(ctx, "GET", narURL, nil)
	if err != nil {
		proxy.log.Error("creating NAR request", zap.String("url", narURL), zap.Error(err))
		return false
	}

	narResp, err := proxy.httpGetClient.Do(narReq)
	if err != nil {
		proxy.log.Error("downloading NAR", zap.String("url", narURL), zap.Error(err))
		return false
	}
	defer func() { _ = narResp.Body.Close() }()

	if narResp.StatusCode != http.StatusOK {
		proxy.log.Warn("upstream NAR returned non-200",
			zap.String("url", narURL),
			zap.Int("status", narResp.StatusCode))
		return false
	}

	ext := narExtFromURL(info.URL)
	rc, err := decompressReader(ext, narResp.Body)
	if err != nil {
		proxy.log.Error("decompressing NAR", zap.String("url", narURL), zap.Error(err))
		return false
	}
	defer func() {
		if err := rc.Close(); err != nil {
			proxy.log.Warn("closing decompressor", zap.Error(err))
		}
	}()

	if err := proxy.insert(ctx, namespace, narLocation, rc); err != nil {
		proxy.log.Error("failed caching NAR", zap.String("url", narURL), zap.Error(err))
		return false
	}

	return true
}

// doCacheNonNarinfo handles caching of non-narinfo content (NARs, realisations, logs).
func (proxy *Proxy) doCacheNonNarinfo(ctx context.Context, req *cacheRequest, responseBody io.Reader) {
	var body io.Reader = responseBody
	if strings.HasPrefix(req.location, "indices/nar/") && req.ext != "" {
		rc, err := decompressReader(req.ext, responseBody)
		if err != nil {
			proxy.log.Error("decompressing cached NAR",
				zap.String("ext", req.ext),
				zap.Error(err))
			// Fall through with original body — insert will likely fail
			// but that matches the original behavior.
		} else {
			defer func() {
				if err := rc.Close(); err != nil {
					proxy.log.Warn("closing decompressor", zap.Error(err))
				}
			}()
			body = rc
		}
	}
	if err := proxy.insert(ctx, req.namespace, req.location, body); err != nil {
		metricCacheJobs("error").Add(1)
		proxy.log.Error("inserting", zap.String("location", req.location), zap.Error(err))
		return
	}
	metricCacheJobs("success").Add(1)
}

// extractBaseURL returns the base URL from a narinfo URL, preserving any path
// prefix by stripping only the filename component (path.Dir).
func extractBaseURL(u string) string {
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	parsed.Path = path.Dir(parsed.Path)
	return parsed.String()
}

// narExtFromURL returns the compression extension from a NAR URL path.
func narExtFromURL(u string) string {
	base := path.Base(u)
	for _, ext := range []string{".xz", ".zst", ".bz2", ".br"} {
		if strings.HasSuffix(base, ext) {
			return ext
		}
	}
	return ""
}

