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

	proxy.log.Info("server shutdown gracefully")
}

type Proxy struct {
	config *config.Config

	// derived from the above
	secretKeys  map[string]signature.SecretKey
	trustedKeys map[string][]signature.PublicKey

	s3Store   desync.WriteStore
	s3Indices map[string]desync.IndexWriteStore

	log       *zap.Logger
	headPool  *pond.WorkerPool
	cachePool *pond.WorkerPool
}

func NewProxy(config *config.Config) *Proxy {
	devLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	return &Proxy{
		config:      config,
		log:         devLog,
		headPool:    pond.New(10, 1000),
		cachePool:   pond.New(10, 1000),
		secretKeys:  map[string]signature.SecretKey{},
		trustedKeys: map[string][]signature.PublicKey{},
		s3Indices:   map[string]desync.IndexWriteStore{},
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

	store, err := desync.NewS3Store(
		s3Url,
		mkCredentials(s3),
		s3.Region,
		defaultStoreOptions(),
		minio.BucketLookupAuto)
	if err != nil {
		proxy.log.Fatal("failed creating s3 store",
			zap.Error(err),
			zap.String("url", s3Url.String()),
			zap.String("region", s3.Region),
		)
	}

	proxy.s3Store = store
}

func (proxy *Proxy) setupIndices() {
	for namespace, ns := range proxy.config.Namespaces {
		s3 := ns.S3
		s3Url, err := url.Parse(s3.Url)
		if err != nil {
			proxy.log.Fatal("couldn't parse S3 URL", zap.Error(err), zap.String("namespace", namespace), zap.String("url", s3.Url))
		}

		index, err := desync.NewS3IndexStore(
			s3Url,
			mkCredentials(s3),
			s3.Region,
			defaultStoreOptions(),
			minio.BucketLookupAuto,
		)
		if err != nil {
			proxy.log.Fatal("failed creating s3 index store",
				zap.Error(err),
				zap.String("url", s3.Url),
				zap.String("region", s3.Region),
			)
		}

		proxy.s3Indices[namespace] = index
	}
}

func mkCredentials(s3 *config.S3) *credentials.Credentials {
	return credentials.NewFileAWSCredentials(s3.CredentialsFile, s3.Profile)
}

func defaultStoreOptions() desync.StoreOptions {
	return desync.StoreOptions{
		N:          64,
		Timeout:    1 * time.Second,
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
	if response, err := http.Get(req.url); err != nil {
		proxy.log.Error("failed downloading file", zap.Error(err), zap.String("url", req.url))
	} else {
		defer response.Body.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := proxy.insert(ctx, req.namespace, req.location, response.Body); err != nil {
			proxy.log.Error("failed caching file", zap.Error(err), zap.String("url", req.url))
		}
	}
}
