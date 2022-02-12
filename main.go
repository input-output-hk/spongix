package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/alexflint/go-arg"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/folbricht/desync"
	"go.uber.org/zap"
)

const threads = 2

func main() {
	proxy := defaultProxy()
	arg.MustParse(proxy)
	// proxy.SetupAWS()
	proxy.SetupDir("nar")
	proxy.SetupNix()
	proxy.SetupDesync()
	proxy.SetupDB()
	// go proxy.validateStore()
	go proxy.gc()
	go func() {
		t := time.Tick(5 * time.Second)
		for range t {
			_ = proxy.log.Sync()
		}
	}()

	r := proxy.routerV2()
	proxy.log.Info("Server running", zap.String("listen", proxy.Listen))
	proxy.log.Error("server died", zap.Error(http.ListenAndServe(proxy.Listen, r)))
	_ = proxy.log.Sync()
}

type Proxy struct {
	BucketName        string   `arg:"--bucket-name,env:AWS_BUCKET_NAME" help:"Bucket to upload to"`
	BucketRegion      string   `arg:"--bucket-region,env:AWS_BUCKET_REGION" help:"AWS region the bucket is in"`
	AWSProfile        string   `arg:"--aws-profile,env:AWS_PROFILE" help:"Profile to use for authentication"`
	Dir               string   `arg:"--dir,env:CACHE_DIR" help:"directory for the cache"`
	Listen            string   `arg:"--listen,env:LISTEN_ADDR" help:"Listen on this address"`
	SecretKeyFiles    []string `arg:"--secret-key-files,required,env:NIX_SECRET_KEY_FILES" help:"Files containing your private nix signing keys"`
	Substituters      []string `arg:"--substituters,env:NIX_SUBSTITUTERS"`
	TrustedPublicKeys []string `arg:"--trusted-public-keys,env:NIX_TRUSTED_PUBLIC_KEYS"`
	CacheInfoPriority uint64   `arg:"--cache-info-priority,env:CACHE_INFO_PRIORITY" help:"Priority in nix-cache-info"`

	// derived from the above
	secretKeys  map[string]ed25519.PrivateKey
	trustedKeys map[string]ed25519.PublicKey
	downloader  *s3manager.Downloader
	uploader    *s3manager.Uploader

	narStore desync.WriteStore
	narIndex desync.IndexWriteStore
	db       *sql.DB
	log      *zap.Logger

	// used for testing
	awsCredentialsFile string
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
	}
}

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
)

func (c *Proxy) Version() string { return buildVersion + " (" + buildCommit + ")" }

func (proxy *Proxy) Clean() {
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
		log.Printf("Creating directory: %q\n", dir)
		fatal(os.MkdirAll(dir, 0o755))
	}
}

func (proxy *Proxy) SetupAWS() {
	if proxy.BucketName == "" {
		log.Println("No bucket name given, will not upload files there")
		return
	}

	clientRegion, set := os.LookupEnv("AWS_DEFAULT_REGION")
	if !set {
		clientRegion = "eu-central-1"
	}

	sess := session.Must(session.NewSession(&aws.Config{
		Region:      aws.String(clientRegion),
		Credentials: credentials.NewSharedCredentials(proxy.awsCredentialsFile, proxy.AWSProfile),
	}))

	res, err := s3manager.GetBucketRegionWithClient(context.Background(), s3.New(sess), proxy.BucketName)
	fatal(err)
	proxy.BucketRegion = res

	proxy.uploader = s3manager.NewUploader(sess)
	proxy.downloader = s3manager.NewDownloader(sess)
}

func (proxy *Proxy) SetupNix() {
	secretKeys, err := LoadNixPrivateKeys(proxy.SecretKeyFiles)
	fatal(err)
	proxy.secretKeys = secretKeys

	publicKeys, err := LoadNixPublicKeys(proxy.TrustedPublicKeys)
	fatal(err)
	proxy.trustedKeys = publicKeys
}

func (proxy *Proxy) SetupDB() {
	dsn, err := url.Parse("file:test.db")
	fatal(err)
	query := dsn.Query()
	// query.Add("cache", "shared")
	// query.Add("mode", "memory")
	query.Add("_auto_vacuum", "incremental")
	query.Add("_foreign_keys", "true")
	query.Add("_journal_mode", "wal")
	query.Add("_synchronous", "FULL")
	query.Add("_loc", "UTC")
	dsn.RawQuery = query.Encode()

	db, err := sql.Open("sqlite3", dsn.String())
	fatal(err)

	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS narinfos (
	name text PRIMARY KEY,
  store_path text,
	url text,
	compression text,
	file_hash_type text,
	file_hash text,
	file_size integer,
	nar_hash_type text,
	nar_hash text,
	nar_size integer,
	deriver text,
	ca text,
	created_at datetime,
	accessed_at datetime
);
`)
	fatal(err)

	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS refs (
  parent text,
	child text,
  FOREIGN KEY(parent) REFERENCES narinfos(name) DEFERRABLE INITIALLY DEFERRED
);
`)
	fatal(err)

	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS signatures (
  name text,
	signature text,
	FOREIGN KEY(name) REFERENCES narinfos(name) DEFERRABLE INITIALLY DEFERRED
);
`)
	fatal(err)

	proxy.db = db
}

func (proxy *Proxy) SetupDesync() {
	proxy.SetupDir("sync/store")
	proxy.SetupDir("sync/index")
	proxy.SetupDir("sync/tmp")

	narStore, err := desync.NewLocalStore(filepath.Join(proxy.Dir, "sync/store"), desync.StoreOptions{
		ErrorRetry:   1,
		Uncompressed: false,
	})
	fatal(err)
	narStore.UpdateTimes = true

	narIndex, err := desync.NewLocalIndexStore(filepath.Join(proxy.Dir, "sync/index"))
	fatal(err)

	proxy.narStore = narStore
	proxy.narIndex = narIndex
}
