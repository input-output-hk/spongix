package main

import (
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/pascaldekloe/metrics"
	"go.uber.org/zap"
)

const (
	mimeNarinfo      = "text/x-nix-narinfo"
	mimeNar          = "application/x-nix-nar"
	mimeText         = "text/plain"
	mimeNixCacheInfo = "text/x-nix-cache-info"
)

func (proxy *Proxy) router() *mux.Router {
	r := mux.NewRouter()
	r.NotFoundHandler = notFound{}
	r.Use(withHTTPLogging(proxy.log))

	r.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("GET")
	r.HandleFunc("/metrics", metrics.ServeHTTP)

	narinfo := "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo"
	r.HandleFunc(narinfo, proxy.narinfoHead).Methods("HEAD")
	r.HandleFunc(narinfo, proxy.narinfoGet).Methods("GET")
	r.HandleFunc(narinfo, proxy.narinfoPut).Methods("PUT")

	nar := "/nar/{hash:[0-9a-df-np-sv-z]{52}}.{ext:nar}"
	r.HandleFunc(nar, proxy.narHead).Methods("HEAD")
	r.HandleFunc(nar, proxy.narGet).Methods("GET")
	r.HandleFunc(nar, proxy.narPut).Methods("PUT")

	return r
}

// GET /nix-cache-info
func (proxy *Proxy) nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Add(headerContentType, mimeNixCacheInfo)
	w.WriteHeader(200)
	_, _ = w.Write([]byte(`StoreDir: /nix/store
WantMassQuery: 1
Priority: ` + strconv.FormatUint(proxy.CacheInfoPriority, 10)))
}

// Narinfo handling

// HEAD /<hash>.narinfo
func (proxy *Proxy) narinfoHead(w http.ResponseWriter, r *http.Request) {
	if proxy.narinfoHeadInner(w, r) {
		w.Header().Add(headerCache, headerCacheHit)
		w.Header().Add(headerContentType, mimeNarinfo)
		w.WriteHeader(200)
	} else {
		w.Header().Add(headerCache, headerCacheMiss)
		w.Header().Add(headerContentType, mimeText)
		w.WriteHeader(404)
	}
}

// GET /<hash>.narinfo
func (proxy *Proxy) narinfoGet(w http.ResponseWriter, r *http.Request) {
	rd := proxy.narinfoGetInner(w, r)
	if rd == nil {
		w.Header().Add(headerCache, headerCacheMiss)
		w.Header().Add(headerContentType, mimeText)
		w.WriteHeader(404)
		w.Write([]byte("not found"))
		return
	}

	info := &Narinfo{}
	err := info.Unmarshal(rd)
	if internalServerError(w, err) {
		return
	}

	if len(info.Sig) == 0 {
		for name, key := range proxy.secretKeys {
			info.Sign(name, key)
		}
	} else {
		info.SanitizeSignatures(proxy.trustedKeys)
		if len(info.Sig) == 0 {
			return
		}
	}

	w.Header().Add(headerContentType, mimeNarinfo)
	if err := info.Marshal(w); err != nil {
		proxy.log.Error("marshaling narinfo", zap.Error(err))
	}

	// proxy.serveChunks(w, r, mimeNarinfo, proxy.narinfoGetInner(w, r))
}

// PUT /<hash>.narinfo
func (proxy *Proxy) narinfoPut(w http.ResponseWriter, r *http.Request) {
	w.Header().Add(headerContentType, mimeText)
	w.WriteHeader(proxy.narinfoPutInner(w, r))
}

// NAR handling

// HEAD /nar/<hash>.nar
func (proxy *Proxy) narHead(w http.ResponseWriter, r *http.Request) {
	if proxy.narHeadInner(w, r) {
		w.Header().Add(headerContentType, mimeNar)
		w.WriteHeader(200)
	} else {
		w.Header().Add(headerCache, headerCacheMiss)
		w.Header().Add(headerContentType, mimeText)
		w.WriteHeader(404)
	}
}

// GET /nar/<hash>.nar
func (proxy *Proxy) narGet(w http.ResponseWriter, r *http.Request) {
	proxy.serveChunks(w, r, mimeNar, proxy.narGetInner(w, r))
}

// PUT /nar/<hash>.nar
func (proxy *Proxy) narPut(w http.ResponseWriter, r *http.Request) {
	if proxy.narPutInner(w, r) {
		w.Header().Add(headerContentType, mimeText)
		w.WriteHeader(200)
	} else {
		w.Header().Add(headerContentType, mimeText)
		w.WriteHeader(500)
	}
}

func (proxy *Proxy) serveChunks(w http.ResponseWriter, r *http.Request, mime string, result io.Reader) {
	switch res := result.(type) {
	case nil:
		w.Header().Add(headerCache, headerCacheMiss)
		w.Header().Add(headerContentType, mimeText)
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	case *os.File:
		defer func() {
			res.Close()
			os.Remove(res.Name())
		}()

		w.Header().Add(headerContentType, mime)
		w.WriteHeader(200)
		http.ServeFile(w, r, res.Name())
	case io.Reader:
		w.Header().Add(headerContentType, mime)
		w.WriteHeader(200)
		if _, err := io.Copy(w, res); err != nil {
			proxy.log.Error("failed copying chunks to response", zap.Error(err))
		}
	default:
		proxy.log.DPanic("unknown type", zap.Any("value", res))
	}
}
