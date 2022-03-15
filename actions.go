package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/folbricht/desync"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

var (
	narHeadTimeout     = 1 * time.Minute
	narGetTimeout      = 30 * time.Minute
	narinfoHeadTimeout = 1 * time.Minute
	narinfoGetTimeout  = 1 * time.Minute
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
	hash := vars["hash"]
	ext := vars["ext"]
	fileName := hash + ext
	url := "nar/" + fileName
	return hash, fileName, url
}

// FIXME: this may panic?
func narinfoName(r *http.Request) (string, string, string) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	fileName := hash + ".narinfo"
	url := fileName
	return hash, fileName, url
}

func (proxy *Proxy) tempFile() (*os.File, error) {
	dir := filepath.Join(proxy.Dir, "tmp")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	return os.CreateTemp(dir, "*.tmp")
}

func (proxy *Proxy) serveIndex(hash string, index desync.Index) io.ReadCloser {
	var cache desync.Store
	if proxy.s3Store != nil {
		cache = desync.NewCache(proxy.s3Store, proxy.localStore)
	} else {
		cache = proxy.localStore
	}

	return newAssembler(cache, index)
}

// HEAD /<hash>.narinfo
func (proxy *Proxy) narinfoHeadInner(w http.ResponseWriter, r *http.Request) bool {
	_, _, url := narinfoName(r)

	if _, err := proxy.getIndex(url); err == nil {
		return true
	} else if err.Error() != "not found in any index" {
		proxy.log.Error("localIndex.GetIndex failed", zap.Error(err))
	}

	remoteBody := proxy.parallelRequest(narinfoHeadTimeout, "HEAD", url)
	if remoteBody != nil {
		w.Header().Add(headerCache, headerCacheRemote)
		w.Header().Add(headerCacheUpstream, remoteBody.url)
		return true
	}

	return false
}

// GET /<hash>.narinfo
func (proxy *Proxy) narinfoGetInner(w http.ResponseWriter, r *http.Request) io.ReadCloser {
	_, _, url := narinfoName(r)

	if index, err := proxy.getIndex(url); err != nil {
		if err.Error() != "not found in any index" {
			proxy.log.Error("getIndex failed", zap.Error(err))
		}
	} else {
		w.Header().Add(headerCache, headerCacheHit)
		return newAssembler(proxy.getCache(), index)
	}

	response := proxy.parallelRequest(narinfoGetTimeout, "GET", url)
	if response == nil {
		return nil
	}

	w.Header().Add(headerCacheUpstream, response.url)
	w.Header().Add(headerCache, headerCacheRemote)
	return proxy.storeChunksAsync(url, response)
}

// PUT /<hash>.narinfo
func (proxy *Proxy) narinfoPutInner(w http.ResponseWriter, r *http.Request) (int, error) {
	hash, _, url := narinfoName(r)

	info := Narinfo{}
	if err := info.Unmarshal(r.Body); err != nil {
		// proxy.log.Error("parsing narinfo failed", zap.String("hash", hash), zap.Error(err))
		return http.StatusBadRequest, err
	}

	defer r.Body.Close()

	buf := &bytes.Buffer{}
	if err := info.Marshal(buf); err != nil {
		proxy.log.Error("marshaling narinfo failed", zap.String("hash", hash), zap.Error(err))
		return http.StatusBadRequest, err
	}

	err := proxy.storeChunks(url, buf)
	if err != nil {
		proxy.log.Error("storing narinfo failed", zap.String("hash", hash), zap.Error(err))
		return http.StatusInternalServerError, err
	}

	return http.StatusOK, nil
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

	if proxy.s3Index != nil {
		if _, err := proxy.s3Index.GetIndex(url); err == nil {
			w.Header().Add(headerCache, headerCacheHit)
			return true
		} else if !errors.Is(err, os.ErrNotExist) {
			proxy.log.Error("s3Index.GetIndex failed", zap.Error(err))
		}
	}

	remoteBody := proxy.parallelRequest(narHeadTimeout, "HEAD", url)
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

	if index, err := proxy.getIndex(url); err == nil {
		w.Header().Add(headerCache, headerCacheHit)
		return proxy.serveIndex(hash, index)
	} else if err.Error() != "not found in any index" {
		proxy.log.Error("getIndex failed", zap.Error(err))
	}

	response := proxy.parallelRequest(narGetTimeout, "GET", url)
	if response == nil {
		return nil
	}

	w.Header().Add(headerCacheUpstream, response.url)
	w.Header().Add(headerCache, headerCacheRemote)
	return proxy.storeChunksAsync(url, response)
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

func dumpIndex(fd *os.File, index desync.Index, store desync.Store) {
	for _, indexChunk := range index.Chunks {
		chunk, err := store.GetChunk(indexChunk.ID)
		if err != nil {
			panic(err)
		}
		data, err := chunk.Data()
		if err != nil {
			panic(err)
		}
		_, err = fd.Write(data)
		if err != nil {
			panic(err)
		}
	}
	fail(fd.Sync())
	fail(fd.Close())
}

func fail(err error) {
	if err != nil {
		panic(err)
	}
}

func mini() {
	os.MkdirAll("test/index", 0755)
	os.MkdirAll("test/store", 0755)

	index, err := desync.NewLocalIndexStore("test/index")
	fail(err)

	store, err := desync.NewLocalStore("test/store", desync.StoreOptions{})
	fail(err)

	orig, err := os.Open("1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar")
	fail(err)

	localTee := newTeeReader()
	tee := &teeCombiner{reader: orig, readers: []*teeReader{localTee}}

	go io.Copy(io.Discard, tee)

	chunker, err := desync.NewChunker(localTee, defaultChunkAverage/4, defaultChunkAverage, defaultChunkAverage*4)
	fail(err)

	idx, err := desync.ChunkStream(context.Background(), chunker, store, 1)
	fail(err)

	err = index.StoreIndex("1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar", idx)
	fail(err)

	fd, err := os.Create("1s50rvmv1n79lk7nhn2w2xvzin0jd1l67bkz5c93xjrc3knl187s.nar.dump")
	if err != nil {
		panic(err)
	}
	dumpIndex(fd, idx, store)
}
