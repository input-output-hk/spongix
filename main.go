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
	"github.com/jmoiron/sqlx"
	"github.com/minio/minio-go/v6"
	"github.com/minio/minio-go/v6/pkg/credentials"
	"go.uber.org/zap"
)

const (
	defaultThreads = 2
)

var chunkSizeAvg uint64 = 65536

func chunkSizeMin() uint64 { return chunkSizeAvg / 4 }
func chunkSizeMax() uint64 { return chunkSizeAvg * 4 }

func main() {
	sshServer()
	return
	experiment()
	// cpuprofile := "spongix.pprof"
	// f, err := os.Create(cpuprofile)
	// if err != nil {
	// 	log.Fatal(err)
	// }
	// pprof.StartCPUProfile(f)
	// defer pprof.StopCPUProfile()

	proxy := NewProxy()

	arg.MustParse(proxy)
	chunkSizeAvg = proxy.AverageChunkSize

	proxy.setupLogger()
	proxy.setupDatabase()
	proxy.setupDesync()
	proxy.setupKeys()
	proxy.setupS3()

	go proxy.startCache()
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

	const timeout = 15 * time.Minute

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
	Namespaces        []string      `arg:"--namespaces,env:NAMESPACES" help:"Namespaces takes one or many strings to setup private caching"`
	CacheInfoPriority uint64        `arg:"--cache-info-priority,env:CACHE_INFO_PRIORITY" help:"Priority in nix-cache-info"`
	AverageChunkSize  uint64        `arg:"--average-chunk-size,env:AVERAGE_CHUNK_SIZE" help:"Chunk size will be between /4 and *4 of this value"`
	CacheSize         uint64        `arg:"--cache-size,env:CACHE_SIZE" help:"Number of gigabytes to keep in the disk cache"`
	VerifyInterval    time.Duration `arg:"--verify-interval,env:VERIFY_INTERVAL" help:"Time between verification runs"`
	GcInterval        time.Duration `arg:"--gc-interval,env:GC_INTERVAL" help:"Time between store garbage collection runs"`
	LogLevel          string        `arg:"--log-level,env:LOG_LEVEL" help:"One of debug, info, warn, error, dpanic, panic, fatal"`
	LogMode           string        `arg:"--log-mode,env:LOG_MODE" help:"development or production"`
	DSN               string        `arg:"--dsn,env:DSN" help:"SQlite DNS"`

	// derived from the above
	secretKeys  map[string]ed25519.PrivateKey
	trustedKeys map[string]ed25519.PublicKey

	s3Store    desync.WriteStore
	localStore desync.WriteStore

	s3Indices    map[string]desync.IndexWriteStore
	localIndices map[string]desync.IndexWriteStore

	cacheChan chan cacheRequest

	db *sqlx.DB

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
		SecretKeyFiles:    nil,
		TrustedPublicKeys: nil,
		Substituters:      nil,
		Namespaces:        nil,
		CacheInfoPriority: 50,
		AverageChunkSize:  chunkSizeAvg,
		VerifyInterval:    time.Hour,
		GcInterval:        time.Hour,
		cacheChan:         make(chan cacheRequest, 10000),
		log:               devLog,
		LogLevel:          "debug",
		LogMode:           "production",
		s3Indices:         map[string]desync.IndexWriteStore{},
		localIndices:      map[string]desync.IndexWriteStore{},
	}
}

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
)

func (proxy *Proxy) Version() string {
	return buildVersion + " (" + buildCommit + ")"
}

func (proxy *Proxy) setupDir(path string) {
	dir := filepath.Join(proxy.Dir, path)
	if _, err := os.Stat(dir); err != nil {
		proxy.log.Debug("Creating directory", zap.String("dir", dir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			proxy.log.Fatal("couldn't create directory", zap.String("dir", dir))
		}
	}
}

func (proxy *Proxy) setupS3() {
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

func (proxy *Proxy) setupKeys() {
	secretKeys, err := loadNixPrivateKeys(proxy.SecretKeyFiles)
	if err != nil {
		proxy.log.Fatal("failed loading private keys", zap.Error(err), zap.Strings("files", proxy.SecretKeyFiles))
	}
	proxy.secretKeys = secretKeys

	publicKeys, err := loadNixPublicKeys(proxy.TrustedPublicKeys)
	if err != nil {
		proxy.log.Fatal("failed loading public keys", zap.Error(err), zap.Strings("files", proxy.TrustedPublicKeys))
	}
	proxy.trustedKeys = publicKeys
}

func (proxy *Proxy) stateDirs() []string {
	stateDirs := []string{"store", "index/nar", "privateIndex/nar"}
	for _, namespace := range proxy.Namespaces {
		stateDirs = append(stateDirs, "namespace/"+namespace+"/nar")
	}

	return stateDirs
}

var defaultStoreOptions = desync.StoreOptions{
	N:            1,
	Timeout:      1 * time.Second,
	ErrorRetry:   0,
	Uncompressed: false,
	SkipVerify:   false,
}

func (proxy *Proxy) setupDesync() {
	for _, name := range proxy.stateDirs() {
		proxy.setupDir(name)
	}

	setupLocalStoreAndIndices(proxy)
	setupNamespaceIndices(proxy)
}

func setupLocalStoreAndIndices(proxy *Proxy) {
	storeDir := filepath.Join(proxy.Dir, "store")
	narStore, err := desync.NewLocalStore(storeDir, defaultStoreOptions)
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
	proxy.localIndices[""] = narIndex
}

func setupNamespaceIndices(proxy *Proxy) {
	privateIndexDir := filepath.Join(proxy.Dir, "privateIndex")

	for _, namespace := range proxy.Namespaces {
		privateNarIndex, err := desync.NewLocalIndexStore(privateIndexDir)
		if err != nil {
			proxy.log.Fatal("failed creating local private index", zap.Error(err), zap.String("dir", privateIndexDir))
		} else {
			proxy.localIndices[namespace] = privateNarIndex
		}
	}
}

func (proxy *Proxy) setupLogger() {
	lvl := zap.NewAtomicLevel()
	if err := lvl.UnmarshalText([]byte(proxy.LogLevel)); err != nil {
		panic(err)
	}
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

func (proxy *Proxy) setupDatabase() {
	// migrations := []migrate.Migration{
	// 	{
	// 		ID: "0001_initialize",
	// 		Migrate: func(ctx context.Context, tx *sql.Tx) error {
	// 			// Using strict schemas would be nice, but there's no support for doing
	// 			// time handling with it yet.
	// 			_, err := tx.ExecContext(ctx, `
	// 				CREATE TABLE narinfos
	// 				( id INTEGER PRIMARY KEY ASC
	// 				, name TEXT NOT NULL
	// 				, store_path TEXT NOT NULL
	// 				, url TEXT NOT NULL
	// 				, compression TEXT
	// 				, file_hash TEXT NOT NULL
	// 				, file_size INTEGER NOT NULL
	// 				, nar_hash TEXT NOT NULL
	// 				, nar_size INTEGER NOT NULL
	// 				, deriver TEXT NOT NULL
	// 				, ca TEXT
	// 				, namespace TEXT NOT NULL
	// 				, ctime DATETIME NOT NULL
	// 				, atime DATETIME NOT NULL
	// 				);

	// 				CREATE INDEX narinfos_name ON narinfos(name);
	// 				CREATE UNIQUE INDEX narinfos_name_namespace ON narinfos(name, namespace);

	// 				CREATE TABLE narinfo_refs
	// 				( narinfo_id INTEGER NOT NULL
	// 				, ref TEXT NOT NULL
	// 				, FOREIGN KEY(narinfo_id) REFERENCES narinfo(id)
	// 				);
	// 				CREATE INDEX narinfos_refs_narinfo_id ON narinfo_refs(narinfo_id);

	// 				CREATE TABLE narinfo_sigs
	// 				( narinfo_id TEXT NOT NULL
	// 				, sig TEXT NOT NULL
	// 				, FOREIGN KEY(narinfo_id) REFERENCES narinfo(id)
	// 				);
	// 				CREATE INDEX narinfos_sigs_narinfo_id ON narinfo_sigs(narinfo_id);

	// 				CREATE TABLE realisations
	// 				( id TEXT NOT NULL PRIMARY KEY
	// 				, out_path TEXT NOT NULL
	// 				, signatures TEXT
	// 				, dependent_realisations TEXT
	// 				, namespace TEXT NOT NULL
	// 				);
	// 				`)
	// 			return err
	// 		},
	// 		Rollback: func(ctx context.Context, tx *sql.Tx) error {
	// 			_, err := tx.Exec(`
	// 				DROP TABLE narinfos;
	// 				DROP TABLE narinfo_refs;
	// 				DROP TABLE narinfo_sigs;
	// 				DROP TABLE realisations;
	// 			`)
	// 			return err
	// 		},
	// 		Timeout: 5,
	// 	},
	// }

	// if db, err := sqlx.Open("sqlite3", proxy.DSN); err != nil {
	// 	proxy.log.Fatal("failed creating database", zap.Error(err), zap.String("DSN", proxy.DSN))
	// } else if err := db.Ping(); err != nil {
	// 	proxy.log.Fatal("failed connecting to database", zap.Error(err), zap.String("DSN", proxy.DSN))
	// } else {
	// 	engine := migrate.NewEngine(db.DB, migrations, migrate.DOLLAR, true)
	// 	sugar := proxy.log.Sugar()
	// 	engine.Printf = func(format string, a ...interface{}) (int, error) {
	// 		sugar.Debugf(strings.TrimSuffix(format, "\n"), a...)
	// 		return 0, nil
	// 	}
	// 	if err := engine.Migrate(context.Background(), "", false); err != nil {
	// 		proxy.log.Fatal("failed migrating", zap.Error(err), zap.String("DSN", proxy.DSN))
	// 	} else {
	// 		proxy.db = db
	// 	}
	// }
}
