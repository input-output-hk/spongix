package main

import (
	"context"
	"database/sql"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/folbricht/desync"
	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func (proxy *Proxy) routerV2() *mux.Router {
	r := mux.NewRouter()
	r.NotFoundHandler = notFound{}
	r.Use(withHTTPLogging(proxy.log))

	r.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("GET")

	narinfo := "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo"
	r.HandleFunc(narinfo, proxy.narinfoHeadV2).Methods("HEAD")
	r.HandleFunc(narinfo, proxy.narinfoGetV2).Methods("GET")
	r.HandleFunc(narinfo, proxy.narinfoPutV2).Methods("PUT")

	nar := "/nar/{hash:[0-9a-df-np-sv-z]{52}}.{ext:nar}"
	r.HandleFunc(nar, proxy.narHeadV2).Methods("HEAD")
	r.HandleFunc(nar, proxy.narGetV2).Methods("GET")
	r.HandleFunc(nar, proxy.narPutV2).Methods("PUT")

	return r
}

func (proxy *Proxy) nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "text/x-nix-cache-info")
	w.WriteHeader(200)
	_, _ = w.Write([]byte(`StoreDir: /nix/store
WantMassQuery: 1
Priority: ` + strconv.FormatUint(proxy.CacheInfoPriority, 10)))
}

func (proxy *Proxy) narHeadV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNar(hash)

	_, err := proxy.narIndex.GetIndex(hash)
	if err != nil {
		if err = proxy.deleteNarinfos(map[string]struct{}{hash: {}}); err != nil {
			proxy.log.Error("removing missing db entry", zap.Error(err))
		}

		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	w.Header().Add("Content-Type", "application/x-nix-nar")
	w.WriteHeader(200)
}

func (proxy *Proxy) narGetV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNar(hash)
	tmpFile, err := ioutil.TempFile(filepath.Join(proxy.Dir, "sync/tmp"), hash)
	if internalServerError(w, err) {
		return
	}
	defer func() {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
	}()

	index, err := proxy.narIndex.GetIndex(hash)
	if err != nil {
		if err = proxy.deleteNarinfos(map[string]struct{}{hash: {}}); err != nil {
			proxy.log.Error("removing missing db entry", zap.Error(err))
		}

		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		_, _ = w.Write([]byte("not found"))
		return
	}

	var cache desync.Store
	if proxy.s3Store != nil {
		cache = desync.NewCache(proxy.s3Store, proxy.narStore)
	} else {
		cache = proxy.narStore
	}

	_, err = desync.AssembleFile(
		context.Background(),
		tmpFile.Name(),
		index,
		cache,
		nil,
		threads,
		nil)

	if internalServerError(w, err) {
		proxy.log.Error("Failed to assemble file", zap.Error(err))
		return
	}

	w.Header().Add("Content-Type", "application/x-nix-nar")
	http.ServeFile(w, r, tmpFile.Name())
}

func (proxy *Proxy) narPutV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNar(hash)
	// ext := vars["ext"]
	if r.ContentLength < 1 {
		w.WriteHeader(http.StatusLengthRequired)
		return
	}

	chunker, err := desync.NewChunker(r.Body, proxy.AverageChunkSize/4, proxy.AverageChunkSize, proxy.AverageChunkSize*4)
	if internalServerError(w, errors.WithMessagef(err, "NewChunker %q", hash)) {
		proxy.log.Error("failed creating chunker", zap.String("hash", hash), zap.Error(err))
		return
	}

	index, err := desync.ChunkStream(context.Background(), chunker, proxy.narStore, 8)
	if internalServerError(w, errors.WithMessagef(err, "ChunkStream %q", hash)) {
		proxy.log.Error("failed chunking stream", zap.String("hash", hash), zap.Error(err))
		return
	}

	err = proxy.narIndex.StoreIndex(hash, index)
	if internalServerError(w, errors.WithMessagef(err, "StoreIndex %q", hash)) {
		proxy.log.Error("failed storing index", zap.String("hash", hash), zap.Error(err))
		return
	}

	if proxy.s3Store != nil {
		for _, indexChunk := range index.Chunks {
			chunk, err := proxy.narStore.GetChunk(indexChunk.ID)
			if err != nil {
				proxy.log.Error("Couldn't get chunk", zap.Error(err))
				w.WriteHeader(500)
				return
			}

			err = proxy.s3Store.StoreChunk(chunk)
			if err != nil {
				proxy.log.Error("Couldn't store chunk in S3", zap.Error(err))
				w.WriteHeader(500)
				return
			}
		}
	}

	w.WriteHeader(200)
}

func (proxy *Proxy) narinfoHeadV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]

	proxy.touchNarinfo(hash)

	narHash := proxy.selectNarHash(hash)

	if narHash == "" {
		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	_, err := proxy.narIndex.GetIndex(narHash)
	if err != nil {
		if err = proxy.deleteNarinfos(map[string]struct{}{narHash: {}}); err != nil {
			proxy.log.Error("removing missing db entry", zap.Error(err))
		}

		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	w.Header().Add("Content-Type", "text/x-nix-narinfo")
	w.WriteHeader(200)
}

func (proxy *Proxy) narinfoGetV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]

	proxy.touchNarinfo(hash)

	info, err := proxy.selectNarinfo(hash)
	if err == sql.ErrNoRows {
		w.Header().Add("Content-Type", "text/html")
		w.WriteHeader(404)
		_, _ = w.Write([]byte("not found in db"))
		return
	} else if internalServerError(w, err) {
		proxy.log.Error("Failed to get narinfo", zap.Error(err))
		return
	}

	info.SanitizeSignatures(proxy.trustedKeys)

	for name, key := range proxy.secretKeys {
		if internalServerError(w, info.Sign(name, key)) {
			proxy.log.Error("Failed signing narinfo", zap.Error(err), zap.String("hash", hash))
			return
		}
	}

	if err != nil {
		proxy.log.Error("Invalid signature", zap.Error(err))
		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(info.StorePath + " signatures are untrusted"))
		return
	} else {
		for name, key := range proxy.secretKeys {
			if internalServerError(w, info.Sign(name, key)) {
				proxy.log.Error("Failed signing narinfo", zap.Error(err), zap.String("hash", hash))
				return
			}
		}
	}

	w.Header().Add("Content-Type", "text/x-nix-narinfo")
	w.WriteHeader(200)
	err = info.Marshal(w)
	if internalServerError(w, err) {
		proxy.log.Error("Failed sending narinfo", zap.Error(err))
		return
	}
}

func (proxy *Proxy) narinfoPutV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]

	info := &Narinfo{}
	err := info.Unmarshal(r.Body)
	if badRequest(w, errors.WithMessagef(err, "Parsing narinfo %q", hash)) {
		proxy.log.Error("Failed parsing narinfo", zap.String("hash", hash), zap.Error(err))
		return
	}

	err = proxy.insertNarinfo(info)
	if internalServerError(w, err) {
		proxy.log.Error("insertNarinfo", zap.Error(err), zap.String("hash", hash))
		return
	}

	w.WriteHeader(200)
}
