package main

import (
	"context"
	"crypto/ed25519"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/alexflint/go-arg"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

var (
	buildVersion = "dev"
	buildCommit  = "dirty"
)

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

	// used for testing
	awsCredentialsFile string
}

func defaultProxy() *Proxy {
	return &Proxy{
		Dir:               "./cache",
		Listen:            ":7745",
		SecretKeyFiles:    []string{},
		TrustedPublicKeys: []string{},
		Substituters:      []string{},
		CacheInfoPriority: 50,
	}
}

func (c *Proxy) Version() string { return buildVersion + " (" + buildCommit + ")" }

func (proxy *Proxy) Clean() {
	clean := func(path string, d os.DirEntry, err error) error {
		switch filepath.Ext(path) {
		case ".narinfo", ".xz":
			return os.Remove(path)
		}
		return nil
	}

	if err := filepath.WalkDir(proxy.Dir, clean); err != nil {
		panic(err)
	}
}

func (proxy *Proxy) SetupDir() {
	dir := filepath.Join(proxy.Dir, "nar")
	if _, err := os.Stat(dir); err != nil {
		log.Printf("Creating directory: %q\n", dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Panic(err)
		}
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
	if err != nil {
		log.Panic(err)
	}
	proxy.BucketRegion = res

	proxy.uploader = s3manager.NewUploader(sess)
	proxy.downloader = s3manager.NewDownloader(sess)
}

func (proxy *Proxy) SetupNix() {
	secretKeys, err := LoadNixPrivateKeys(proxy.SecretKeyFiles)
	if err != nil {
		log.Panic(err)
	}
	proxy.secretKeys = secretKeys

	publicKeys, err := LoadNixPublicKeys(proxy.TrustedPublicKeys)
	if err != nil {
		log.Panic(err)
	}
	proxy.trustedKeys = publicKeys
}

func main() {
	proxy := defaultProxy()
	arg.MustParse(proxy)
	proxy.SetupAWS()
	proxy.SetupDir()
	proxy.SetupNix()
	proxy.validateStore()

	r := proxy.router()
	log.Printf("Running on %q", proxy.Listen)
	log.Panic(http.ListenAndServe(proxy.Listen, r))
}
