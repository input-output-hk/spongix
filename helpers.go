package main

import (
	"compress/bzip2"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DataDog/zstd"
	"github.com/andybalholm/brotli"
	"github.com/folbricht/desync"
	"github.com/kr/pretty"
	"github.com/pierrec/lz4"
	"github.com/pkg/errors"
	"github.com/ulikunitz/xz"
	"go.uber.org/zap"
)

func pp(v ...interface{}) {
	pretty.Println(v...)
}

func LoadNixPublicKeys(rawKeys []string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, rawKey := range rawKeys {
		name, value, err := parseNixPair(rawKey)
		if err != nil {
			return nil, errors.WithMessage(err, "While loading public keys")
		}
		keys[name] = ed25519.PublicKey(value)
	}

	return keys, nil
}

func LoadNixPrivateKeys(paths []string) (map[string]ed25519.PrivateKey, error) {
	pairs, err := readNixPairs(paths)
	if err != nil {
		return nil, errors.WithMessage(err, "While loading private keys")
	}

	keys := map[string]ed25519.PrivateKey{}
	for name, key := range pairs {
		keys[name] = ed25519.PrivateKey(key)
	}

	return keys, nil
}

func readNixPairs(paths []string) (map[string][]byte, error) {
	keys := map[string][]byte{}

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.WithMessagef(err, "Trying to read %q", path)
		}

		name, key, err := parseNixPair(string(raw))
		if err != nil {
			return nil, errors.WithMessagef(err, "Key parsing failed for %q", raw)
		}

		keys[name] = key
	}

	return keys, nil
}

func parseNixPair(input string) (string, []byte, error) {
	i := strings.IndexRune(input, ':')
	if i < 1 {
		return "", nil, errors.Errorf("Key has no name part in %q", input)
	}
	name := input[0:i]
	encoded := input[i+1:]
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, errors.Errorf("Key decoding failed for %q", encoded)
	}

	return name, value, nil
}

func internalServerError(w http.ResponseWriter, err error) bool {
	return respondError(w, err, http.StatusInternalServerError)
}

func respondError(w http.ResponseWriter, err error, status int) bool {
	if err == nil {
		return false
	}

	http.Error(w, err.Error(), status)
	return true
}

type notFound struct{}

func (n notFound) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	notFoundResponse(w, r)
}

func notFoundResponse(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(404)
	_, _ = w.Write([]byte(r.RequestURI + " not found"))
}

type parallelResponse struct {
	orig   io.ReadCloser
	trans  io.Reader
	cancel context.CancelFunc
	url    string
	code   int
}

func newParallelResponse(orig io.ReadCloser, url string, code int) (*parallelResponse, error) {
	self := &parallelResponse{
		orig: orig,
		url:  url,
		code: code,
	}
	if orig == nil {
		return self, nil
	}
	if err := self.prepare(); err != nil {
		return nil, err
	}

	return self, nil
}

func (pr *parallelResponse) prepare() error {
	ext := filepath.Ext(pr.url)
	switch ext {
	case ".nar", ".narinfo":
		pr.trans = pr.orig
	case ".xz":
		xzConf := xz.ReaderConfig{SingleStream: true}
		if err := xzConf.Verify(); err != nil {
			return err
		}
		xzRd, err := xz.NewReader(pr.orig)
		if err != nil {
			return err
		}
		pr.trans = xzRd
	case ".bzip2", ".bz2":
		pr.trans = bzip2.NewReader(pr.orig)
	case ".zstd", ".zst":
		pr.trans = zstd.NewReader(pr.orig)
	case ".lzip":
		// zip needs io.ReaderAt, we could work around that by using more memory or
		// disk, but for now it's more of a YAGNI thing.
		return errors.New("zip is not supported")
	case ".lz4":
		pr.trans = lz4.NewReader(pr.orig)
	case ".br":
		pr.trans = brotli.NewReader(pr.orig)
	default:
		return errors.Errorf("unknown ext: %q", ext)
	}

	return nil
}

func (pr parallelResponse) Close() error {
	rdCl, ok := pr.trans.(io.ReadCloser)
	if ok {
		_ = rdCl.Close()
	}
	return pr.orig.Close()
}

func (pr parallelResponse) Read(b []byte) (int, error) {
	return pr.trans.Read(b)
}

func (proxy *Proxy) parallelRequest(timeout time.Duration, method, path string) *parallelResponse {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	paths := []string{path}
	switch filepath.Ext(path) {
	case ".nar": // most upstream caches use xz
		paths = append(paths, path+".xz")
	}

	requests := len(proxy.Substituters) * len(paths)
	c := make(chan *parallelResponse, requests)

	go func() {
		if proxy.parallelRequestOrdered {
			for _, substituter := range proxy.Substituters {
				for _, path := range paths {
					proxy.recvParallelRequest(ctx, nil, c, method, substituter, path)
				}
			}
		} else {
			wg := &sync.WaitGroup{}
			wg.Add(requests)
			for _, substituter := range proxy.Substituters {
				for _, path := range paths {
					go proxy.recvParallelRequest(ctx, wg, c, method, substituter, path)
				}
			}
			wg.Wait()
		}
		close(c)
	}()

	select {
	case found := <-c:
		if found == nil {
			cancel()
		} else {
			found.cancel = cancel
		}
		return found
	case <-ctx.Done():
		cancel() // at this point, it's canceled already, but this shuts up a warning.
		return nil
	}
}

func (proxy *Proxy) recvParallelRequest(ctx context.Context, wg *sync.WaitGroup, c chan *parallelResponse, method, sub, path string) {
	res, err := doParallelRequest(ctx, method, sub, path)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			proxy.log.Error("parallel request error", zap.Error(err))
		}
	} else {
		if res.code/100 == 2 {
			select {
			case c <- res:
			case <-ctx.Done():
				proxy.log.Warn("timeout", zap.String("url", res.url))
			}
		}
	}

	if wg != nil {
		wg.Done()
	}
}

func doParallelRequest(ctx context.Context, method, sub, path string) (*parallelResponse, error) {
	subURL, err := url.Parse(sub)
	if err != nil {
		return nil, err
	}
	subURL.Path = path

	request, err := http.NewRequestWithContext(ctx, method, subURL.String(), nil)
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

	if res.StatusCode/100 == 2 && method != "HEAD" {
		return newParallelResponse(res.Body, subURL.String(), res.StatusCode)
	}
	res.Body.Close()
	return newParallelResponse(nil, subURL.String(), res.StatusCode)
}

func (proxy *Proxy) storeIndex(name string, rd io.Reader, store desync.WriteStore, index desync.IndexWriteStore) (i desync.Index, err error) {
	chunker, err := desync.NewChunker(
		rd,
		proxy.minChunkSize(),
		proxy.avgChunkSize(),
		proxy.maxChunkSize())
	if err != nil {
		return i, errors.WithMessage(err, "while making desync.NewChunker")
	}

	chunks := []desync.IndexChunk{}
	size := uint64(0)
	for {
		start, b, err := chunker.Next()

		if err != nil {
			return desync.Index{}, err
		}

		if len(b) == 0 {
			break
		}
		size += uint64(len(b))

		chunk := desync.NewChunk(b)

		idxChunk := desync.IndexChunk{Start: start, Size: uint64(len(b)), ID: chunk.ID()}
		chunks = append(chunks, idxChunk)

		if err := store.StoreChunk(chunk); err != nil {
			return i, err
		}
	}

	idx := desync.Index{
		Index: desync.FormatIndex{
			FormatHeader: desync.FormatHeader{Size: size, Type: desync.CaFormatIndex},
			FeatureFlags: desync.CaFormatExcludeNoDump | desync.CaFormatSHA512256,
			ChunkSizeMin: chunker.Min(),
			ChunkSizeAvg: chunker.Avg(),
			ChunkSizeMax: chunker.Max(),
		},
		Chunks: chunks,
	}

	if err = index.StoreIndex(name, idx); err != nil {
		return idx, errors.WithMessage(err, "while storing index")
	}

	return idx, nil
}

func (proxy *Proxy) storeChunksAsync(url string, body io.ReadCloser) io.ReadCloser {
	localTee := newTeeReader()
	tee := newTeeCombiner(body, localTee)

	var s3Tee *teeReader
	if proxy.s3Index != nil {
		s3Tee = newTeeReader()
		tee.AddReader(s3Tee)
	}

	go func() {
		wg := &sync.WaitGroup{}
		wg.Add(1)
		go func() {
			_, err := proxy.storeIndex(url, localTee, proxy.localStore, proxy.localIndex)
			if err != nil {
				proxy.log.Error("caching local", zap.String("url", url), zap.Error(err))
				_, _ = io.Copy(io.Discard, localTee)
			}
			localTee.Close()
			wg.Done()
		}()

		wg.Add(1)
		go func() {
			if proxy.s3Index != nil {
				_, err := proxy.storeIndex(url, s3Tee, proxy.s3Store, proxy.s3Index)
				if err != nil {
					proxy.log.Error("caching S3", zap.String("url", url), zap.Error(err))
					_, _ = io.Copy(io.Discard, s3Tee)
				}
				s3Tee.Close()
			}
			wg.Done()
		}()

		wg.Wait()
	}()

	return teeCloser{tee: tee, body: body}
}

func (proxy *Proxy) storeChunks(url string, body io.Reader) error {
	idx, err := proxy.storeIndex(url, body, proxy.localStore, proxy.localIndex)
	if err != nil {
		return err
	}

	if proxy.s3Index == nil {
		return nil
	}

	ids := []desync.ChunkID{}
	for _, chunk := range idx.Chunks {
		ids = append(ids, chunk.ID)
	}

	if err = proxy.s3Index.StoreIndex(url, idx); err != nil {
		return errors.WithMessage(err, "while storing index")
	}

	return desync.Copy(
		context.Background(),
		ids,
		proxy.localStore,
		proxy.s3Store,
		defaultThreads,
		nil)
}
