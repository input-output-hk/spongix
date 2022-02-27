package main

import (
	"context"
	"crypto/ed25519"
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
	"github.com/minio/minio-go/v6"
	"github.com/minio/minio-go/v6/pkg/credentials"
	"go.uber.org/zap"
)

const (
	defaultThreads      = 2
	defaultChunkAverage = 65536
)

func main() {
	proxy := NewProxy()
	arg.MustParse(proxy)
	proxy.SetupLogger()
	proxy.SetupDesync()
	proxy.SetupKeys()
	proxy.SetupS3()
	go proxy.gc()
	go proxy.verify()
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

	const timeout = 15 * time.Second

	srv := &http.Server{
		Handler:      proxy.router(),
		Addr:         proxy.Listen,
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
		proxy.log.Info("Server starting", zap.String("listen", proxy.Listen))
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
	BucketURL         string        `arg:"--bucket-url,env:BUCKET_URL" help:"Bucket URL like s3+http://127.0.0.1:9000/ncp"`
	BucketRegion      string        `arg:"--bucket-region,env:BUCKET_REGION" help:"Region the bucket is in"`
	Dir               string        `arg:"--dir,env:CACHE_DIR" help:"directory for the cache"`
	Listen            string        `arg:"--listen,env:LISTEN_ADDR" help:"Listen on this address"`
	SecretKeyFiles    []string      `arg:"--secret-key-files,required,env:NIX_SECRET_KEY_FILES" help:"Files containing your private nix signing keys"`
	Substituters      []string      `arg:"--substituters,env:NIX_SUBSTITUTERS"`
	TrustedPublicKeys []string      `arg:"--trusted-public-keys,env:NIX_TRUSTED_PUBLIC_KEYS"`
	CacheInfoPriority uint64        `arg:"--cache-info-priority,env:CACHE_INFO_PRIORITY" help:"Priority in nix-cache-info"`
	AverageChunkSize  uint64        `arg:"--average-chunk-size,env:AVERAGE_CHUNK_SIZE" help:"Chunk size will be between /4 and *4 of this value"`
	CacheSize         uint64        `arg:"--cache-size,env:CACHE_SIZE" help:"Number of gigabytes to keep in the disk cache"`
	VerifyInterval    time.Duration `arg:"--verify-interval,env:VERIFY_INTERVAL" help:"Seconds between verification runs"`
	GcInterval        time.Duration `arg:"--gc-interval,env:GC_INTERVAL" help:"Seconds between store garbage collection runs"`
	LogLevel          string        `arg:"--log-level,env:LOG_LEVEL" help:"One of debug, info, warn, error, dpanic, panic, fatal"`
	LogMode           string        `arg:"--log-mode,env:LOG_MODE" help:"development or production"`

	// derived from the above
	secretKeys  map[string]ed25519.PrivateKey
	trustedKeys map[string]ed25519.PublicKey

	s3Store desync.WriteStore
	s3Index desync.IndexWriteStore

	localStore desync.WriteStore
	localIndex desync.IndexWriteStore

	log *zap.Logger
}

func NewProxy() *Proxy {
	devLog, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}

	return &Proxy{
		Dir:               "./cache",
		Listen:            ":7745",
		SecretKeyFiles:    []string{},
		TrustedPublicKeys: []string{},
		Substituters:      []string{},
		CacheInfoPriority: 50,
		DatabaseDSN:       "file:spongix.sqlite",
		AverageChunkSize:  defaultChunkAverage,
		VerifyInterval:    time.Hour,
		GcInterval:        time.Minute,
		log:               devLog,
		LogLevel:          "debug",
		LogMode:           "production",
	}
}

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
)

func (proxy *Proxy) Version() string {
	return buildVersion + " (" + buildCommit + ")"
}

func (proxy Proxy) minChunkSize() uint64 {
	return proxy.AverageChunkSize / 4
}

func (proxy Proxy) avgChunkSize() uint64 {
	return proxy.AverageChunkSize
}

func (proxy Proxy) maxChunkSize() uint64 {
	return proxy.AverageChunkSize * 4
}

func (proxy *Proxy) SetupDir(path string) {
	dir := filepath.Join(proxy.Dir, path)
	if _, err := os.Stat(dir); err != nil {
		proxy.log.Debug("Creating directory", zap.String("dir", dir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			proxy.log.Fatal("couldn't create directory", zap.String("dir", dir))
		}
	}
}

func (proxy *Proxy) SetupS3() {
	if proxy.BucketURL == "" {
		log.Println("No bucket name given, will not upload files")
		return
	}

	if proxy.BucketRegion == "" {
		log.Println("No bucket region given, will not upload files")
		return
	}

	s3Url, err := url.Parse(proxy.BucketURL)
	if err != nil {
		proxy.log.Fatal("couldn't parse bucket url", zap.Error(err), zap.String("url", proxy.BucketURL))
	}
	creds := credentials.NewChainCredentials(
		[]credentials.Provider{
			&credentials.EnvMinio{},
			&credentials.EnvAWS{},
		},
	)

	store, err := desync.NewS3Store(s3Url, creds, proxy.BucketRegion,
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
			zap.String("region", proxy.BucketRegion),
		)
	}

	proxy.s3Store = store
}

func (proxy *Proxy) SetupKeys() {
	secretKeys, err := LoadNixPrivateKeys(proxy.SecretKeyFiles)
	if err != nil {
		proxy.log.Fatal("failed loading private keys", zap.Error(err), zap.Strings("files", proxy.SecretKeyFiles))
	}
	proxy.secretKeys = secretKeys

	publicKeys, err := LoadNixPublicKeys(proxy.TrustedPublicKeys)
	if err != nil {
		proxy.log.Fatal("failed loading public keys", zap.Error(err), zap.Strings("files", proxy.TrustedPublicKeys))
	}
	proxy.trustedKeys = publicKeys
}

func (proxy *Proxy) StateDirs() []string {
	return []string{"store", "index", "tmp"}
}

func (proxy *Proxy) SetupDesync() {
	for _, name := range proxy.StateDirs() {
		proxy.SetupDir(name)
	}

	storeDir := filepath.Join(proxy.Dir, "store")
	narStore, err := desync.NewLocalStore(storeDir, desync.StoreOptions{
		N:            1,
		Timeout:      1 * time.Second,
		ErrorRetry:   0,
		Uncompressed: false,
		SkipVerify:   false,
	})
	if err != nil {
		proxy.log.Fatal("failed creating local store", zap.Error(err), zap.String("dir", storeDir))
	}
	narStore.UpdateTimes = true

	indexDir := filepath.Join(proxy.Dir, "index")
	narIndex, err := desync.NewLocalIndexStore(indexDir)
	if err != nil {
		proxy.log.Fatal("failed creating local index", zap.Error(err), zap.String("dir", indexDir))
	}

	proxy.localStore = narStore
	proxy.localIndex = narIndex
}

func (proxy *Proxy) SetupLogger() {
	lvl := zap.NewAtomicLevel()
	lvl.UnmarshalText([]byte(proxy.LogLevel))
	development := proxy.LogMode == "development"
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
	proxy.log, err = l.Build()
	if err != nil {
		panic(err)
	}
}
