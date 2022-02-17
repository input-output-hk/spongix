package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"fmt"
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

const threads = 2

func main() {
	proxy := defaultProxy()
	arg.MustParse(proxy)
	proxy.SetupDesync()
	proxy.SetupNix()
	proxy.SetupDB()
	proxy.SetupS3()
	go proxy.gc()
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
		Handler:      proxy.routerV2(),
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
	BucketURL         string   `arg:"--bucket-url,env:BUCKET_URL" help:"Bucket URL like s3+http://127.0.0.1:9000/ncp"`
	BucketRegion      string   `arg:"--bucket-region,env:BUCKET_REGION" help:"Region the bucket is in"`
	Dir               string   `arg:"--dir,env:CACHE_DIR" help:"directory for the cache"`
	Listen            string   `arg:"--listen,env:LISTEN_ADDR" help:"Listen on this address"`
	SecretKeyFiles    []string `arg:"--secret-key-files,required,env:NIX_SECRET_KEY_FILES" help:"Files containing your private nix signing keys"`
	Substituters      []string `arg:"--substituters,env:NIX_SUBSTITUTERS"`
	TrustedPublicKeys []string `arg:"--trusted-public-keys,env:NIX_TRUSTED_PUBLIC_KEYS"`
	CacheInfoPriority uint64   `arg:"--cache-info-priority,env:CACHE_INFO_PRIORITY" help:"Priority in nix-cache-info"`
	DatabaseDSN       string   `arg:"--database,env:DATABASE_DSN" help:"DSN for the db, like file:test.db"`
	AverageChunkSize  uint64   `arg:"--average-chunk-size,env:AVERAGE_CHUNK_SIZE" help:"Chunk size will be between /4 and *4 of this value"`

	// derived from the above
	secretKeys  map[string]ed25519.PrivateKey
	trustedKeys map[string]ed25519.PublicKey
	s3Store     desync.WriteStore
	narStore    desync.WriteStore
	narIndex    desync.IndexWriteStore
	db          *sql.DB
	log         *zap.Logger
}

func defaultProxy() *Proxy {
	logger, err := zap.NewDevelopment()
	fatal(err)

	return &Proxy{
		Dir:               "./cache",
		Listen:            ":7745",
		SecretKeyFiles:    []string{},
		TrustedPublicKeys: []string{},
		Substituters:      []string{},
		CacheInfoPriority: 50,
		log:               logger,
		DatabaseDSN:       "file:nix-cache-proxy.sqlite",
		AverageChunkSize:  65536,
	}
}

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
)

func (c *Proxy) Version() string { return buildVersion + " (" + buildCommit + ")" }

func (proxy *Proxy) Clean() {
	for _, name := range proxy.StateDirs() {
		dir := filepath.Join(proxy.Dir, name)
		proxy.log.Debug(fmt.Sprintf("Removing directory: %q\n", dir))
		os.RemoveAll(dir)
	}

	clean := func(path string, d os.DirEntry, err error) error {
		switch filepath.Ext(path) {
		case ".narinfo", ".xz", ".nar":
			return os.Remove(path)
		}
		return nil
	}

	if err := filepath.WalkDir(proxy.Dir, clean); err != nil {
		panic(err)
	}
}

func (proxy *Proxy) SetupDir(path string) {
	dir := filepath.Join(proxy.Dir, path)
	if _, err := os.Stat(dir); err != nil {
		proxy.log.Debug(fmt.Sprintf("Creating directory: %q\n", dir))
		fatal(os.MkdirAll(dir, 0o755))
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
	fatal(err)
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
	fatal(err)

	proxy.s3Store = store
}

func (proxy *Proxy) SetupNix() {
	secretKeys, err := LoadNixPrivateKeys(proxy.SecretKeyFiles)
	fatal(err)
	proxy.secretKeys = secretKeys

	publicKeys, err := LoadNixPublicKeys(proxy.TrustedPublicKeys)
	fatal(err)
	proxy.trustedKeys = publicKeys
}

func (proxy *Proxy) StateDirs() []string {
	return []string{"sync/store", "sync/index", "sync/tmp"}
}

func (proxy *Proxy) SetupDesync() {
	for _, name := range proxy.StateDirs() {
		proxy.SetupDir(name)
	}

	narStore, err := desync.NewLocalStore(filepath.Join(proxy.Dir, "sync/store"), desync.StoreOptions{
		N:            1,
		Timeout:      1 * time.Second,
		ErrorRetry:   0,
		Uncompressed: false,
		SkipVerify:   false,
	})
	fatal(err)
	narStore.UpdateTimes = true

	narIndex, err := desync.NewLocalIndexStore(filepath.Join(proxy.Dir, "sync/index"))
	fatal(err)

	proxy.narStore = narStore
	proxy.narIndex = narIndex
}
