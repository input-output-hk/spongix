package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
	"github.com/gorilla/mux"
	"github.com/julienp/httplog"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

var (
	narinfoPattern = regexp.MustCompile(`\A[0-9a-df-np-sv-z]{32}\.narinfo\z`)
)

func (proxy *Proxy) router() *mux.Router {
	log := logrus.StandardLogger()
	r := mux.NewRouter()
	r.NotFoundHandler = notFound{}
	r.Use(httplog.WithHTTPLogging(log.WithContext(context.Background())))

	// public cache
	r.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("GET")
	r.HandleFunc("/{key}", proxy.narinfoGet).Methods("GET")
	r.HandleFunc("/nar/{key}", proxy.narHead).Methods("HEAD")
	r.HandleFunc("/nar/{key}", proxy.narGet).Methods("GET")

	// S3 compat endpoints used by `nix copy`
	r.HandleFunc("/{bucket:[a-z-]+}/nix-cache-info", proxy.nixCacheInfo).Methods("GET")

	narinfo := "/{bucket:[a-z-]+}/{key}"
	r.HandleFunc(narinfo, proxy.narinfoPut).Methods("PUT")
	r.HandleFunc(narinfo, proxy.narinfoGet).Methods("GET")

	nar := `/{bucket:[a-z-]+}/nar/{key}`
	r.HandleFunc(nar, proxy.narHead).Methods("HEAD")
	r.HandleFunc(nar, proxy.narPut).Methods("PUT")
	r.HandleFunc(nar, proxy.narGet).Methods("GET")

	return r
}

func (proxy *Proxy) narinfoPath(r *http.Request) (string, string, error) {
	key, ok := mux.Vars(r)["key"]
	if ok && narinfoPattern.MatchString(key) {
		return filepath.Join(proxy.Dir, key), key, nil
	} else {
		return "", "", errors.New("Invalid narinfo name")
	}
}

func (proxy *Proxy) narPath(r *http.Request) (string, string, error) {
	key, ok := mux.Vars(r)["key"]
	if ok {
		return filepath.Join(proxy.Dir, "nar", key), filepath.Join("nar", key), nil
	} else {
		return "", "", errors.New("Invalid nar name")
	}
}

func (proxy *Proxy) nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/x-nix-cache-info")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(`StoreDir: /nix/store
WantMassQuery: 1
Priority: ` + strconv.FormatUint(proxy.CacheInfoPriority, 10)))
}

func (proxy *Proxy) narHead(w http.ResponseWriter, r *http.Request) {
	path, _, err := proxy.narPath(r)
	if badRequest(w, err) {
		return
	}

	if _, err := os.Stat(path); err != nil {
		w.Header().Add("Content-Type", "text/html")
		w.WriteHeader(404)
	} else {
		w.WriteHeader(200)
	}
}

func (proxy *Proxy) narPut(w http.ResponseWriter, r *http.Request) {
	path, s3Path, err := proxy.narPath(r)
	if badRequest(w, errors.WithMessage(err, "Calculating nar path")) {
		return
	}

	if !proxy.safeCreate(w, path, func(_w io.Writer) io.Reader {
		return r.Body
	}) {
		return
	}

	if proxy.uploader != nil {
		f, err := os.Open(path)
		if err != nil {
			log.Panicf("failed to open file %q, %v", path, err)
		}
		defer f.Close()

		result, err := proxy.uploader.Upload(&s3manager.UploadInput{
			Bucket: aws.String(proxy.BucketName),
			Key:    aws.String(s3Path),
			Body:   f,
		})
		if err != nil {
			log.Panicf("failed to upload file, %v", err)
		}
		log.Printf("file uploaded to %q\n", aws.StringValue(&result.Location))
	}

	w.WriteHeader(200)
}

func (proxy *Proxy) narGet(w http.ResponseWriter, r *http.Request) {
	path, remotePath, err := proxy.narPath(r)
	if badRequest(w, err) {
		return
	}

	if _, err := os.Stat(path); err == nil {
		log.Printf("Serving %q from disk\n", path)
		w.Header().Add("Content-Type", "application/x-nix-nar")
		http.ServeFile(w, r, path)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Hour)
	content := proxy.parallelRequest(ctx, remotePath)
	defer cancel()

	if content != nil {
		defer content.Close()

		proxy.safeCreate(w, path, func(output io.Writer) io.Reader {
			tee := io.TeeReader(content, w)
			w.Header().Add("Content-Type", "application/x-nix-nar")
			w.WriteHeader(200)
			return tee
		})

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.WriteHeader(404)
	_, _ = w.Write([]byte("404"))
}

func (proxy *Proxy) narinfoPut(w http.ResponseWriter, r *http.Request) {
	path, s3Path, err := proxy.narinfoPath(r)
	if badRequest(w, err) {
		return
	}

	body, err := io.ReadAll(r.Body)
	if badRequest(w, err) {
		return
	}

	info := &NarInfo{}
	if badRequest(w, info.Unmarshal(bytes.NewBuffer(body))) {
		return
	}

	if len(info.Sig) == 0 {
		for name, key := range proxy.secretKeys {
			if internalServerError(w, info.Sign(name, key)) {
				return
			}
		}
	} else if err = info.Verify(proxy.trustedKeys); err != nil {
		badRequest(w, errors.WithMessagef(err, "%s signatures are untrusted", info.StorePath))
		return
	}

	signed := &bytes.Buffer{}
	if internalServerError(w, info.Marshal(signed)) {
		return
	}

	if !proxy.safeCreate(w, path, func(io.Writer) io.Reader {
		return bytes.NewReader(signed.Bytes())
	}) {
		return
	}

	if proxy.uploader != nil {
		_, err = proxy.uploader.Upload(&s3manager.UploadInput{
			Bucket: aws.String(proxy.BucketName),
			Key:    aws.String(s3Path),
			Body:   signed,
		})

		if internalServerError(w, err) {
			return
		}
	}

	w.WriteHeader(200)
}

func (proxy *Proxy) narinfoGet(w http.ResponseWriter, r *http.Request) {
	path, remotePath, err := proxy.narinfoPath(r)
	if badRequest(w, err) {
		return
	}

	if _, err := os.Stat(path); err == nil {
		log.Printf("Serving %q from disk\n", path)
		w.Header().Add("Content-Type", "text/x-nix-narinfo")
		http.ServeFile(w, r, path)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	content := proxy.parallelRequest(ctx, remotePath)
	defer cancel()

	if content != nil {
		defer content.Close()

		proxy.safeCreate(w, path, func(output io.Writer) io.Reader {
			tee := io.TeeReader(content, w)
			w.Header().Add("Content-Type", "text/x-nix-narinfo")
			w.WriteHeader(200)
			return tee
		})

		return
	}

	vars := mux.Vars(r)
	w.WriteHeader(404)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>NoSuchKey</Code>
  <Message>The specified key does not exist.</Message>
  <Resource>` + r.URL.Path + `</Resource>
  <BucketName>` + vars["bucket"] + `</BucketName>
  <Key>` + vars["key"] + `</Key>
  <RequestId>16B81914FBB8345F</RequestId>
  <HostId>672a09d6-39bb-41a6-bcf3-b0375d351cfe</HostId>
</Error>`))
}

func (proxy *Proxy) parallelRequest(ctx context.Context, path string) io.ReadCloser {
	contentChan := make(chan io.ReadCloser, len(proxy.Substituters))
	failureChan := make(chan error, len(proxy.Substituters))

	for _, sub := range proxy.Substituters {
		go manageParallelReqeust(ctx, contentChan, failureChan, path, sub)
	}

	for count := 0; count < len(proxy.Substituters); count++ {
		select {
		case content := <-contentChan:
			return content
		case failure := <-failureChan:
			log.Println(failure.Error())
		}
	}

	return nil
}

func manageParallelReqeust(
	ctx context.Context,
	contentChan chan io.ReadCloser,
	failureChan chan error,
	path, sub string,
) {
	if content, err := doParallelReqeust(ctx, path, sub); err != nil {
		failureChan <- err
	} else {
		contentChan <- content
	}
}

func doParallelReqeust(ctx context.Context, path, sub string) (io.ReadCloser, error) {
	subUrl, err := url.Parse(sub)
	if err != nil {
		return nil, err
	}
	subUrl.Path = path

	request, err := http.NewRequestWithContext(ctx, "GET", subUrl.String(), nil)
	if err != nil {
		return nil, err
	}

	res, err := http.DefaultClient.Do(request)
	if err != nil {
		urlErr, ok := err.(*url.Error)
		if ok && urlErr.Err.Error() == "context canceled" {
			return nil, err
		}

		return nil, err
	}

	switch res.StatusCode {
	case 200:
		return res.Body, nil
	case 404, 403:
		res.Body.Close()
		return nil, errors.Errorf("%s => %d", subUrl.String(), res.StatusCode)
	default:
		res.Body.Close()
		return nil, errors.Errorf("%s => %d", subUrl.String(), res.StatusCode)
	}
}

func (proxy *Proxy) safeCreate(
	w http.ResponseWriter,
	path string,
	fn func(io.Writer) io.Reader,
) bool {
	proxy.SetupDir("nar")

	partPath := path + ".partial"
	partial, err := os.Create(partPath)
	if internalServerError(w, errors.WithMessagef(err, "Creating path %q", partPath)) {
		return false
	}
	defer partial.Close()

	input := fn(partial)

	_, err = io.Copy(partial, input)
	if internalServerError(w, errors.WithMessagef(err, "Copying body to %q", partPath)) {
		os.Remove(partPath)
		return false
	}

	err = partial.Sync()
	if internalServerError(w, errors.WithMessagef(err, "Syncing %q", partPath)) {
		os.Remove(partPath)
		return false
	}

	err = partial.Close()
	if internalServerError(w, errors.WithMessagef(err, "Closing %q", partPath)) {
		os.Remove(partPath)
		return false
	}

	err = os.Rename(partPath, path)
	if internalServerError(w, errors.WithMessagef(err, "Renaming %q to %q", partPath, path)) {
		os.Remove(partPath)
		os.Remove(path)
		return false
	}

	return true
}
