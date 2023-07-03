package main

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/alitto/pond"
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
	proxy.setupKeys()
	proxy.setupS3()

	go proxy.startCache()

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

	s3Store desync.WriteStore
	s3Index desync.IndexWriteStore

	cacheChan chan *cacheRequest

	log  *zap.Logger
	pool *pond.WorkerPool
}

func NewProxy(config *config.Config) *Proxy {
	devLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	return &Proxy{
		config:      config,
		cacheChan:   make(chan *cacheRequest, 10000),
		log:         devLog,
		pool:        pond.New(10, 1000),
		secretKeys:  map[string]signature.SecretKey{},
		trustedKeys: map[string][]signature.PublicKey{},
	}
}

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
)

func (proxy *Proxy) Version() string {
	return buildVersion + " (" + buildCommit + ")"
}

func (proxy *Proxy) setupS3() {
	cfg := proxy.config
	if cfg.S3BucketUrl == "" {
		proxy.log.Fatal("No bucket url given, will not upload files")
	}

	if cfg.S3BucketRegion == "" {
		proxy.log.Fatal("No bucket region given, will not upload files")
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

	options := desync.StoreOptions{
		N:          64,
		Timeout:    1 * time.Second,
		ErrorRetry: 1,
	}

	store, err := desync.NewS3Store(s3Url, creds, cfg.S3BucketRegion, options, minio.BucketLookupAuto)
	if err != nil {
		proxy.log.Fatal("failed creating s3 store",
			zap.Error(err),
			zap.String("url", s3Url.String()),
			zap.String("region", cfg.S3BucketRegion),
		)
	}

	proxy.s3Store = store

	index, err := desync.NewS3IndexStore(s3Url, creds, cfg.S3BucketRegion, options, minio.BucketLookupAuto)
	if err != nil {
		proxy.log.Fatal("failed creating s3 index store",
			zap.Error(err),
			zap.String("url", s3Url.String()),
			zap.String("region", cfg.S3BucketRegion),
		)
	}

	proxy.s3Index = index
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

func (proxy *Proxy) setupLogger() {
	if log, err := logger.SetupLogger(proxy.config.LogMode, proxy.config.LogLevel); err != nil {
		panic(err)
	} else {
		proxy.log = log
	}
}

// retrieve queued cache requests and cache them in our own store
// this is done in a separate goroutine to not block the main request handler
// for the time being, we only process one request at a time, and don't use the pool.
func (proxy *Proxy) startCache() {
	for req := range proxy.cacheChan {
		proxy.log.Info("Caching", zap.String("location", req.location), zap.String("namespace", req.namespace), zap.String("url", req.url))
		proxy.doCache(req)
	}
}

func (proxy *Proxy) doCache(req *cacheRequest) {
	if response, err := http.Get(req.url); err != nil {
		proxy.log.Error("failed downloading file", zap.Error(err), zap.String("url", req.url))
	} else {
		defer response.Body.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := proxy.insert(ctx, req.location, response.Body); err != nil {
			proxy.log.Error("failed caching file", zap.Error(err), zap.String("url", req.url))
		}
	}
}
