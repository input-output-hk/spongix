package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/config"
	"github.com/input-output-hk/spongix/pkg/logger"
	"github.com/minio/minio-go/v6"
	"github.com/minio/minio-go/v6/pkg/credentials"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"go.uber.org/zap"
)

const (
	defaultThreads = 2
)

var chunkSizeAvg uint64 = 65536

func chunkSizeMin() uint64 { return chunkSizeAvg / 4 }
func chunkSizeMax() uint64 { return chunkSizeAvg * 4 }

func main() {
	// cpuprofile := "spongix.pprof"
	// f, err := os.Create(cpuprofile)
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// pprof.StartCPUProfile(f)
	// defer pprof.StopCPUProfile()

	c, err := config.LoadFile("config.json")
	if err != nil {
		panic(err)
	}

	proxy := NewProxy(c)

	arg.MustParse(proxy)
	chunkSizeAvg = proxy.config.AverageChunkSize

	proxy.setupLogger()
	proxy.setupDesync()
	proxy.setupKeys()
	proxy.setupS3()

	// go proxy.startCache()
	proxy.migrate()

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
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)

	go func() {
		proxy.log.Info("Server starting", zap.String("listen", proxy.config.Listen))
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			// Only log an error if it's not due to shutdown or close
			proxy.log.Fatal("error bringing up listener", zap.Error(err))
		}
	}()

	<-sc
	signal.Stop(sc)

	// Shutdown timeout should be max request timeout (with 1s buffer).
	ctxShutDown, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := srv.Shutdown(ctxShutDown); err != nil {
		proxy.log.Fatal("server shutdown failed", zap.Error(err))
	}

	proxy.log.Info("server shutdown gracefully")
}

type Proxy struct {
	config *config.Config

	// derived from the above
	secretKeys  map[string]signature.SecretKey
	trustedKeys map[string][]signature.PublicKey

	s3Store    desync.WriteStore
	localStore desync.WriteStore

	s3Indices    map[string]desync.IndexWriteStore
	localIndices map[string]desync.IndexWriteStore

	cacheChan chan *cacheRequest

	log *zap.Logger
}

func NewProxy(config *config.Config) *Proxy {
	devLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	return &Proxy{
		config:       config,
		cacheChan:    make(chan *cacheRequest, 10000),
		log:          devLog,
		s3Indices:    map[string]desync.IndexWriteStore{},
		localIndices: map[string]desync.IndexWriteStore{},
		secretKeys:   map[string]signature.SecretKey{},
		trustedKeys:  map[string][]signature.PublicKey{},
	}
}

func (p *Proxy) dbFile() string {
	return filepath.Join(p.config.Dir, "index.sqlite")
}

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
)

func (proxy *Proxy) Version() string {
	return buildVersion + " (" + buildCommit + ")"
}

func (proxy *Proxy) setupDir(dir string) {
	if _, err := os.Stat(dir); err != nil {
		proxy.log.Debug("Creating directory", zap.String("dir", dir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			proxy.log.Fatal("couldn't create directory", zap.String("dir", dir))
		}
	}
}

func (proxy *Proxy) setupS3() {
	cfg := proxy.config
	if cfg.S3BucketUrl == "" {
		log.Println("No bucket url given, will not upload files")
		return
	}

	if cfg.S3BucketRegion == "" {
		log.Println("No bucket region given, will not upload files")
		return
	}

	s3Url, err := url.Parse(cfg.S3BucketUrl)
	if err != nil {
		proxy.log.Fatal("couldn't parse bucket url", zap.Error(err), zap.String("url", cfg.S3BucketUrl))
	}
	creds := credentials.NewChainCredentials(
		[]credentials.Provider{
			&credentials.EnvMinio{},
			&credentials.EnvAWS{},
		},
	)

	store, err := desync.NewS3Store(s3Url, creds, cfg.S3BucketRegion,
		desync.StoreOptions{
			N:            1,
			Timeout:      1 * time.Second,
			ErrorRetry:   0,
			Uncompressed: false,
			SkipVerify:   false,
		}, minio.BucketLookupAuto)
	if err != nil {
		proxy.log.Fatal("failed creating s3 store",
			zap.Error(err),
			zap.String("url", s3Url.String()),
			zap.String("region", cfg.S3BucketRegion),
		)
	}

	proxy.s3Store = store
}

func (proxy *Proxy) setupKeys() {
	for namespace, ns := range proxy.config.Namespaces {
		if content, err := os.ReadFile(ns.SecretKeyFile); err != nil {
			proxy.log.Fatal("failed reading private key file", zap.Error(err), zap.String("file", ns.SecretKeyFile))
		} else if secretKey, err := signature.LoadSecretKey(string(content)); err != nil {
			proxy.log.Fatal("failed loading private keys", zap.Error(err), zap.String("file", ns.SecretKeyFile))
		} else {
			proxy.secretKeys[namespace] = secretKey
		}

		proxy.trustedKeys[namespace] = make([]signature.PublicKey, len(ns.TrustedPublicKeys))
		for _, trustedKeySource := range ns.TrustedPublicKeys {
			trustedKey, err := signature.ParsePublicKey(trustedKeySource)
			if err != nil {
				proxy.log.Fatal("failed loading trusted key", zap.Error(err), zap.String("key", trustedKeySource))
			}
			proxy.trustedKeys[namespace] = append(proxy.trustedKeys[namespace], trustedKey)
		}
	}
}

var defaultStoreOptions = desync.StoreOptions{
	N:            1,
	Timeout:      1 * time.Second,
	ErrorRetry:   0,
	Uncompressed: false,
	SkipVerify:   false,
}

func (proxy *Proxy) setupDesync() {
	for namespace := range proxy.config.Namespaces {
		indexDir := filepath.Join(proxy.config.Dir, "index", namespace)
		proxy.setupDir(filepath.Join(indexDir, "nar"))
		narIndex, err := desync.NewLocalIndexStore(indexDir)
		if err != nil {
			proxy.log.Fatal("failed creating local index", zap.Error(err), zap.String("dir", indexDir))
		}

		proxy.localIndices[namespace] = narIndex
	}

	storeDir := filepath.Join(proxy.config.Dir, "store")
	proxy.setupDir(storeDir)
	narStore, err := desync.NewLocalStore(storeDir, defaultStoreOptions)
	if err != nil {
		proxy.log.Fatal("failed creating local store", zap.Error(err), zap.String("dir", storeDir))
	}
	narStore.UpdateTimes = true
	proxy.localStore = narStore
}

func (proxy *Proxy) setupLogger() {
	if log, err := logger.SetupLogger(proxy.config.LogMode, proxy.config.LogLevel); err != nil {
		panic(err)
	} else {
		proxy.log = log
	}
}
