package main

import (
	"net/http"
	"path/filepath"
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
	r.MethodNotAllowedHandler = notAllowed{}
	r.Use(
		withHTTPLogging(proxy.log),
		handlers.RecoveryHandler(handlers.PrintRecoveryStack(true)),
	)

	r.HandleFunc("/metrics", metrics.ServeHTTP)

	newDockerHandler(proxy.log, proxy.localStore, proxy.localIndex, filepath.Join(proxy.Dir, "oci"), r)

	// backwards compat
	for _, prefix := range []string{"/cache", ""} {
		r.HandleFunc(prefix+"/nix-cache-info", proxy.nixCacheInfo).Methods("GET")

		narinfo := r.Name("narinfo").Path(prefix + "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo").Subrouter()
		narinfo.Use(
			proxy.withLocalCacheHandler(),
			proxy.withS3CacheHandler(),
			withRemoteHandler(proxy.log, proxy.Substituters, []string{""}, proxy.cacheChan),
		)
		narinfo.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)

		nar := r.Name("nar").Path(prefix + "/nar/{hash:[0-9a-df-np-sv-z]{52}}{ext:\\.nar(?:\\.xz|)}").Subrouter()
		nar.Use(
			proxy.withLocalCacheHandler(),
			proxy.withS3CacheHandler(),
			withRemoteHandler(proxy.log, proxy.Substituters, []string{"", ".xz"}, proxy.cacheChan),
		)
		nar.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)
	}

	return r
}

func (proxy *Proxy) withLocalCacheHandler() mux.MiddlewareFunc {
	return withCacheHandler(
		proxy.log,
		proxy.localStore,
		proxy.localIndexies[""], // default
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

type notAllowed struct{}

func (n notAllowed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pp("*** 405", r.Method, r.URL.Path, mux.Vars(r))
}

type notFound struct{}

func (n notFound) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	serveNotFound(w, r)
}

func serveNotFound(w http.ResponseWriter, r *http.Request) {
	pp("*** 404", r.Method, r.URL.Path, mux.Vars(r))
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
