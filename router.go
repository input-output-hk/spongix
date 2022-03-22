package main

import (
	"net/http"
	"strconv"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/pascaldekloe/metrics"
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
	r.Use(
		withHTTPLogging(proxy.log),
		handlers.RecoveryHandler(),
	)

	r.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("GET")
	r.HandleFunc("/metrics", metrics.ServeHTTP)

	narinfo := r.Name("narinfo").Path("/{hash:[0-9a-df-np-sv-z]{32}}.narinfo").Subrouter()
	narinfo.Use(
		proxy.withLocalCacheHandler(),
		proxy.withS3CacheHandler(),
		withRemoteHandler(proxy.log, proxy.Substituters, []string{""}, proxy.cacheChan),
	)
	narinfo.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)

	nar := r.Name("nar").Path("/nar/{hash:[0-9a-df-np-sv-z]{52}}{ext:\\.nar(?:\\.xz|)}").Subrouter()
	nar.Use(
		proxy.withLocalCacheHandler(),
		proxy.withS3CacheHandler(),
		withRemoteHandler(proxy.log, proxy.Substituters, []string{"", ".xz"}, proxy.cacheChan),
	)
	nar.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)

	return r
}

func (proxy *Proxy) withLocalCacheHandler() mux.MiddlewareFunc {
	return withCacheHandler(
		proxy.log,
		proxy.localStore,
		proxy.localIndex,
		proxy.trustedKeys,
		proxy.secretKeys,
	)
}

func (proxy *Proxy) withS3CacheHandler() mux.MiddlewareFunc {
	return withCacheHandler(
		proxy.log,
		proxy.s3Store,
		proxy.s3Index,
		proxy.trustedKeys,
		proxy.secretKeys,
	)
}

type notFound struct{}

func (n notFound) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	serveNotFound(w, r)
}

func serveNotFound(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerContentType, mimeText)
	w.Header().Set(headerCache, headerCacheMiss)
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("not found"))
}

// GET /nix-cache-info
func (proxy *Proxy) nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	answer(w, http.StatusOK, mimeNixCacheInfo, `StoreDir: /nix/store
WantMassQuery: 1
Priority: `+strconv.FormatUint(proxy.CacheInfoPriority, 10))
}
