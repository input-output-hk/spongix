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
	"github.com/input-output-hk/spongix/pkg/store"
	"github.com/minio/minio-go/v6"
	"github.com/minio/minio-go/v6/pkg/credentials"
	"go.uber.org/zap"
)

const (
	defaultThreads = 2
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

	// Drain worker pools before closing stores.
	// cachePool first (it writes to stores), then headPool (read-only).
	proxy.log.Info("draining worker pools")
	proxy.cachePool.StopAndWait()
	proxy.headPool.StopAndWait()

	// Close Pebble DB if any
	if proxy.pebbleDB != nil {
		if err := proxy.pebbleDB.Close(); err != nil {
			proxy.log.Error("closing pebble db", zap.Error(err))
		}
	}

	proxy.log.Info("server shutdown gracefully")
}

type Proxy struct {
	config *config.Config

	s3Store   desync.WriteStore
	s3Indices map[string]desync.IndexWriteStore

	log       *zap.Logger
	headPool  *pond.WorkerPool
	cachePool *pond.WorkerPool

	httpHeadClient *http.Client
	httpGetClient  *http.Client

	// pebbleDB is the shared Pebble database for chunks and indices.
	pebbleDB *store.DB
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
		s3Indices:      map[string]desync.IndexWriteStore{},
		httpHeadClient: httpHeadClient,
		httpGetClient:  httpGetClient,
	}
}

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
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

		s3Store, err := desync.NewS3Store(
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

		proxy.s3Store = s3Store

	case "pebble":
		db, err := store.Open(proxy.config.Chunks.PebbleDir)
		if err != nil {
			proxy.log.Fatal("failed opening pebble db",
				zap.Error(err),
				zap.String("dir", proxy.config.Chunks.PebbleDir),
			)
		}
		proxy.pebbleDB = db
		proxy.s3Store = store.NewChunkStore(db)
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

			proxy.s3Indices[namespace] = store.WrapS3IndexStore(index)

		case "pebble":
			if proxy.pebbleDB == nil {
				proxy.log.Fatal("namespace uses pebble store_type but chunks store_type is not pebble",
					zap.String("namespace", namespace),
				)
			}
			proxy.s3Indices[namespace] = store.NewIndexStore(proxy.pebbleDB, namespace)
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

func (proxy *Proxy) setupLogger() {
	if log, err := logger.SetupLogger(proxy.config.LogMode, proxy.config.LogLevel); err != nil {
		panic(err)
	} else {
		proxy.log = log
	}
}

func (proxy *Proxy) doCache(req *cacheRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", req.url, nil)
	if err != nil {
		proxy.log.Error("failed creating request", zap.Error(err), zap.String("url", req.url))
		return
	}

	response, err := proxy.httpGetClient.Do(httpReq)
	if err != nil {
		proxy.log.Error("failed downloading file", zap.Error(err), zap.String("url", req.url))
		return
	}
	defer func() { _ = response.Body.Close() }()

	if err := proxy.insert(ctx, req.namespace, req.location, response.Body); err != nil {
		proxy.log.Error("failed caching file", zap.Error(err), zap.String("url", req.url))
	}
}
