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
	mimeJson         = "application/json"
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

	for namespace, ns := range proxy.config.Namespaces {
		prefix := "/{namespace:" + namespace + "}"
		r.HandleFunc(prefix+"/nix-cache-info", proxy.nixCacheInfo).Methods("GET")

		narinfo := r.Name("narinfo").Path(prefix + "/{hash:[0-9a-df-np-sv-z]{32}}{ext:\\.narinfo}").Subrouter()
		narinfo.Use(
			proxy.withLocalCacheHandler(namespace),
			proxy.withS3CacheHandler(namespace),
			withRemoteHandler(proxy.log, ns.Substituters, []string{""}, proxy.cacheChan),
		)
		narinfo.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)

		nar := r.Name("nar").Path(prefix + "/nar/{hash:[0-9a-df-np-sv-z]{52}}{ext:\\.nar(?:\\.xz|)}").Subrouter()
		nar.Use(
			proxy.withLocalCacheHandler(namespace),
			proxy.withS3CacheHandler(namespace),
			withRemoteHandler(proxy.log, ns.Substituters, []string{"", ".xz"}, proxy.cacheChan),
		)
		nar.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)

		realisations := r.Name("realisations").Path(prefix + "/realisations/sha256:{hash:[0-9a-f]{64}}!{output:[^.]+}{ext:\\.doi}").Subrouter()
		realisations.Use(
			proxy.withLocalCacheHandler(namespace),
			proxy.withS3CacheHandler(namespace),
			withRemoteHandler(proxy.log, ns.Substituters, []string{""}, proxy.cacheChan),
		)
		realisations.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)
	}

	return r
}

func (proxy *Proxy) withLocalCacheHandler(namespace string) mux.MiddlewareFunc {
	return withCacheHandler(
		proxy.log,
		proxy.localStore,
		proxy.localIndices[namespace],
		proxy.trustedKeys[namespace],
		proxy.secretKeys[namespace],
	)
}

func (proxy *Proxy) withS3CacheHandler(namespace string) mux.MiddlewareFunc {
	return withCacheHandler(
		proxy.log,
		proxy.s3Store,
		proxy.s3Indices[namespace],
		proxy.trustedKeys[namespace],
		proxy.secretKeys[namespace],
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
	if namespace, ok := mux.Vars(r)["namespace"]; !ok {
		panic("namespace not given")
	} else if ns, ok := proxy.config.Namespaces[namespace]; !ok {
		panic("namespace not found")
	} else {
		answer(w, http.StatusOK, mimeNixCacheInfo, `StoreDir: /nix/store
WantMassQuery: 1
Priority: `+strconv.FormatUint(ns.CacheInfoPriority, 10))
	}
}
