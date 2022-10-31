package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
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

	for _, namespace := range proxy.Namespaces {
		prefix := "/{namespace:" + namespace + "}"
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

		realisations := r.Name("realisations").Path(prefix + "/realisations/{hash:[^/]+}.doi").Subrouter()
		realisations.Methods("HEAD").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pp(r.Method, r.URL.String(), r.Header)
			panic("unexpected HEAD")
		})

		realisations.Methods("PUT").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Expect") == "100-continue" {
				vars := mux.Vars(r)
				hash := vars["hash"]
				namespace := vars["namespace"]
				realisation := Realisation{ID: hash, Namespace: namespace}
				if err := json.NewDecoder(r.Body).Decode(&realisation); err != nil {
					panic(err)
				} else if _, err := proxy.db.NamedExec(`
					INSERT OR REPLACE INTO realisations
					( id
					, out_path
					, signatures
					, dependent_realisations
					, namespace
					)
					VALUES
					( :id
					, :out_path
					, :signatures
					, :dependent_realisations
					, :namespace
					);
					`, realisation); err != nil {
					panic(err)
				}
			} else {
				w.WriteHeader(200)
			}
			// {
			//   "dependentRealisations": {},
			//   "id": "sha256:3a131766aea6f9d31731a47fca66da875c224c88c79b28b7aa32621e2aacc365!out",
			//   "outPath": "qxs7l1miypr09s8gf2id9bdkzsnnskpp-lolcat-100.0.1",
			//   "signatures": []
			// }
		})
		realisations.Methods("GET").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			pp(r.Method, r.URL.String())
			vars := mux.Vars(r)
			hash := vars["hash"]
			namespace := vars["namespace"]
			res := proxy.db.QueryRowx(`SELECT * FROM realisations WHERE id IS ? AND namespace IS ?`, hash, namespace)
			realisation := Realisation{ID: hash, Namespace: namespace}
			if err := res.StructScan(&realisation); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					w.WriteHeader(404)
					return
				}
				panic(err)
			}
			pp(realisation)
			if err := json.NewEncoder(w).Encode(realisation); err != nil {
				panic(err)
			}
			w.WriteHeader(200)
		})
	}

	return r
}

func (proxy *Proxy) withLocalCacheHandler() mux.MiddlewareFunc {
	return withCacheHandler(
		proxy.log,
		proxy.localStore,
		proxy.localIndices,
		proxy.trustedKeys,
		proxy.secretKeys,
		proxy.db,
	)
}

func (proxy *Proxy) withS3CacheHandler() mux.MiddlewareFunc {
	return withCacheHandler(
		proxy.log,
		proxy.s3Store,
		proxy.s3Indices,
		proxy.trustedKeys,
		proxy.secretKeys,
		proxy.db,
	)
}

type notAllowed struct{}

func (n notAllowed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pp("*** 403", r.Method, r.URL.Path, mux.Vars(r))
	w.Header().Set(headerContentType, mimeText)
	w.Header().Set(headerCache, headerCacheMiss)
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("not allowed"))
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
