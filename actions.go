package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/folbricht/desync"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

var (
	narHeadTimeout    = 5 * time.Second
	narGetTimeout     = 30 * time.Minute
	narinfoGetTimeout = 5 * time.Second
)

const (
	headerCache         = "X-Cache"
	headerCacheHit      = "HIT"
	headerCacheRemote   = "REMOTE"
	headerCacheMiss     = "MISS"
	headerCacheUpstream = "X-Cache-Upstream"
	headerContentType   = "Content-Type"
)

// FIXME: this may panic?
func narName(r *http.Request) (string, string, string) {
	vars := mux.Vars(r)
	narHash := vars["hash"]
	narFileName := narHash + ".nar"
	narUrl := "nar/" + narFileName
	return narHash, narFileName, narUrl
}

// FIXME: this may panic?
func narinfoName(r *http.Request) (string, string, string) {
	vars := mux.Vars(r)
	narHash := vars["hash"]
	narFileName := narHash + ".narinfo"
	narUrl := narFileName
	return narHash, narFileName, narUrl
}

func (proxy *Proxy) tempFile() (*os.File, error) {
	dir := filepath.Join(proxy.Dir, "tmp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, "*.tmp")
}

func (proxy *Proxy) serveIndex(hash string, index desync.Index) *os.File {
	tmpFile, err := proxy.tempFile()
	if err != nil {
		proxy.log.Error("tempFile creation failed", zap.Error(err))
		return nil
	}

	var cache desync.Store
	if proxy.s3Store != nil {
		cache = desync.NewCache(proxy.s3Store, proxy.localStore)
	} else {
		cache = proxy.localStore
	}

	_, err = desync.AssembleFile(
		context.Background(),
		tmpFile.Name(),
		index,
		cache,
		nil,
		defaultThreads,
		nil)

	if err != nil {
		proxy.log.Error("desync.AssembleFile failed", zap.Error(err))
		return nil
	}

	return tmpFile
}

// HEAD /nar/<hash>.nar
func (proxy *Proxy) narinfoHeadInner(w http.ResponseWriter, r *http.Request) bool {
	_, _, url := narinfoName(r)

	if _, err := proxy.getIndex(url); err == nil {
		return true
	} else if !errors.Is(err, os.ErrNotExist) {
		proxy.log.Error("localIndex.GetIndex failed", zap.Error(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), narHeadTimeout)
	defer cancel()

	remoteBody := proxy.parallelRequest(ctx, "HEAD", url)
	if remoteBody != nil {
		w.Header().Add(headerCache, headerCacheRemote)
		w.Header().Add(headerCacheUpstream, remoteBody.url)
		return true
	}

	return false
}

// GET /<hash>.narinfo
func (proxy *Proxy) narinfoGetInner(w http.ResponseWriter, r *http.Request) io.Reader {
	_, _, url := narinfoName(r)

	if index, err := proxy.getIndex(url); err != nil {
		proxy.log.Error("getIndex failed", zap.Error(err))
	} else if content, err := assemble(proxy.getCache(), index); err != nil {
		proxy.log.Error("assemble failed", zap.Error(err))
	} else {
		w.Header().Add(headerCache, headerCacheHit)
		return bytes.NewBuffer(content)
	}

	ctx, cancel := context.WithTimeout(context.Background(), narinfoGetTimeout)
	defer cancel()

	response := proxy.parallelRequest(ctx, "GET", url)
	if response == nil {
		return nil
	}

	w.Header().Add(headerCacheUpstream, response.url)
	w.Header().Add(headerCache, headerCacheRemote)
	return proxy.storeChunksAsync(url, response.body)
}

// PUT /<hash>.narinfo
func (proxy *Proxy) narinfoPutInner(w http.ResponseWriter, r *http.Request) int {
	hash, _, url := narinfoName(r)

	info := Narinfo{}
	if err := info.Unmarshal(r.Body); err != nil {
		proxy.log.Error("parsing narinfo failed", zap.String("hash", hash), zap.Error(err))
		return http.StatusBadRequest
	}

	defer r.Body.Close()

	buf := &bytes.Buffer{}
	if err := info.Marshal(buf); err != nil {
		proxy.log.Error("marshaling narinfo failed", zap.Error(err))
		return http.StatusBadRequest
	}

	err := proxy.storeChunks(url, buf)
	if err != nil {
		proxy.log.Error("storing narinfo failed", zap.Error(err))
		return http.StatusInternalServerError
	}

	return http.StatusOK
}

// HEAD /nar/<hash>.nar
func (proxy *Proxy) narHeadInner(w http.ResponseWriter, r *http.Request) bool {
	_, _, url := narName(r)

	if _, err := proxy.localIndex.GetIndex(url); err == nil {
		w.Header().Add(headerCache, headerCacheHit)
		return true
	} else if !errors.Is(err, os.ErrNotExist) {
		proxy.log.Error("localIndex.GetIndex failed", zap.Error(err))
	}

	if _, err := proxy.s3Index.GetIndex(url); err == nil {
		w.Header().Add(headerCache, headerCacheHit)
		return true
	} else if !errors.Is(err, os.ErrNotExist) {
		proxy.log.Error("s3Index.GetIndex failed", zap.Error(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), narHeadTimeout)
	defer cancel()

	remoteBody := proxy.parallelRequest(ctx, "HEAD", url)
	if remoteBody != nil {
		w.Header().Add(headerCache, headerCacheRemote)
		w.Header().Add(headerCacheUpstream, remoteBody.url)
		return true
	}

	return false
}

// GET /nar/<hash>.nar
func (proxy *Proxy) narGetInner(w http.ResponseWriter, r *http.Request) io.Reader {
	hash, _, url := narName(r)

	if index, err := proxy.localIndex.GetIndex(url); err == nil {
		w.Header().Add(headerCache, headerCacheHit)
		return proxy.serveIndex(hash, index)
	} else if !errors.Is(err, os.ErrNotExist) {
		proxy.log.Error("localIndex.GetIndex failed", zap.Error(err))
	}

	if index, err := proxy.s3Index.GetIndex(url); err == nil {
		w.Header().Add(headerCache, headerCacheHit)
		return proxy.serveIndex(hash, index)
	} else if !errors.Is(err, os.ErrNotExist) {
		proxy.log.Error("s3Index.GetIndex failed", zap.Error(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), narGetTimeout)
	defer cancel()

	response := proxy.parallelRequest(ctx, "GET", url)
	if response == nil {
		return nil
	}

	w.Header().Add(headerCacheUpstream, response.url)
	w.Header().Add(headerCache, headerCacheRemote)
	return proxy.storeChunksAsync(url, response.body)
}

// PUT /nar/<hash>.nar
func (proxy *Proxy) narPutInner(w http.ResponseWriter, r *http.Request) bool {
	_, _, url := narName(r)

	err := proxy.storeChunks(url, r.Body)
	if err != nil {
		proxy.log.Error("storing NAR failed", zap.Error(err))
		return false
	}
	return true
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

	idx, err := desync.ChunkStream(context.Background(), chunker, store, defaultThreads)
	if err != nil {
		return idx, errors.WithMessage(err, "while running desync.ChunkStream")
	}

	if err = index.StoreIndex(name, idx); err != nil {
		return idx, errors.WithMessage(err, "while storing index")
	}

	return idx, nil
}

func (proxy *Proxy) storeChunksAsync(url string, body io.ReadCloser) io.Reader {
	localRd, localWr := io.Pipe()
	localTee := io.TeeReader(body, localWr)

	s3Rd, s3Wr := io.Pipe()
	s3Tee := io.TeeReader(localTee, s3Wr)

	go func() {
		wg := sync.WaitGroup{}
		wg.Add(2)

		go func() {
			defer wg.Done()
			_, err := proxy.storeIndex(url, localRd, proxy.localStore, proxy.localIndex)
			if err != nil {
				proxy.log.Error("caching upstream response locally", zap.Error(err))
				// ensure we don't block other reads going on in case of failure
				_, _ = io.Copy(io.Discard, localRd)
			}
			s3Wr.Close()
			s3Rd.Close()
		}()

		go func() {
			defer wg.Done()
			_, err := proxy.storeIndex(url, s3Rd, proxy.s3Store, proxy.s3Index)
			if err != nil {
				proxy.log.Error("caching upstream response on s3", zap.Error(err))
				// ensure we don't block other reads going on in case of failure
				_, _ = io.Copy(io.Discard, s3Rd)
			}
			localWr.Close()
			localRd.Close()
		}()

		wg.Wait()
		proxy.log.Debug("caching upstream done")
	}()

	return s3Tee
}

func (proxy *Proxy) storeChunks(url string, body io.Reader) error {
	idx, err := proxy.storeIndex(url, body, proxy.localStore, proxy.localIndex)
	if err != nil {
		return err
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
