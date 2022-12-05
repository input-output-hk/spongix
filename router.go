package main

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"crawshaw.io/sqlite"
	"github.com/folbricht/desync"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

const (
	mimeNarinfo      = "text/x-nix-narinfo"
	mimeNar          = "application/x-nix-nar"
	mimeText         = "text/plain"
	mimeNixCacheInfo = "text/x-nix-cache-info"
	mimeJson         = "application/json"

	matchNarinfo = "/{url:[0-9a-df-np-sv-z]{32}\\.narinfo}"
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

	for name := range proxy.config.Namespaces {
		namespace := r.Name("namespace").PathPrefix("/{namespace:" + name + "}").Subrouter()
		namespace.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("GET", "HEAD")
		namespace.HandleFunc(matchNarinfo, proxy.commonGet).Methods("GET")
		namespace.HandleFunc(matchNarinfo, proxy.commonHead).Methods("HEAD")
		namespace.HandleFunc(matchNarinfo, proxy.commonPut).Methods("PUT")

		// narinfo := r.Name("narinfo").Path(prefix + "/{hash:[0-9a-df-np-sv-z]{32}}{ext:\\.narinfo}").Subrouter()
		// narinfo.Use(
		// 	proxy.withLocalCacheHandler(namespace),
		// 	proxy.withS3CacheHandler(namespace),
		// 	withRemoteHandler(proxy.log, ns.Substituters, []string{""}, proxy.cacheChan),
		// )
		// narinfo.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)

		// nar := r.Name("nar").Path(prefix + "/nar/{hash:[0-9a-df-np-sv-z]{52}}{ext:\\.nar(?:\\.xz|)}").Subrouter()
		// nar.Use(
		// 	proxy.withLocalCacheHandler(namespace),
		// 	proxy.withS3CacheHandler(namespace),
		// 	withRemoteHandler(proxy.log, ns.Substituters, []string{"", ".xz"}, proxy.cacheChan),
		// )
		// nar.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)

		// realisations := r.Name("realisations").Path(prefix + "/realisations/sha256:{hash:[0-9a-f]{64}}!{output:[^.]+}{ext:\\.doi}").Subrouter()
		// realisations.Use(
		// 	proxy.withLocalCacheHandler(namespace),
		// 	proxy.withS3CacheHandler(namespace),
		// 	withRemoteHandler(proxy.log, ns.Substituters, []string{""}, proxy.cacheChan),
		// )
		// realisations.Methods("HEAD", "GET", "PUT").HandlerFunc(serveNotFound)
	}

	return r
}

func (p *Proxy) commonHead(w http.ResponseWriter, r *http.Request) {
	log := p.log.With(zap.String("method", r.Method), zap.String("url", r.URL.String()))
	h := w.Header()

	vars := mux.Vars(r)
	pp(vars)
	namespace := vars["namespace"]
	url := vars["url"]

	if err := p.withDbReadOnly(func(db *sqlite.Conn) error {
		selectFiles := db.Prep(`
			SELECT
			COALESCE(files.content_type, indices.content_type)
			FROM files, indices
			WHERE (  files.namespace IS :namespace AND   files.url IS :url) 
			   OR (indices.namespace IS :namespace AND indices.url IS :url)
			LIMIT 1
		`)
		if err := selectFiles.Reset(); err != nil {
			return errors.WithMessage(err, "while resetting the query to select files")
		}

		selectFiles.SetText(":namespace", namespace)
		selectFiles.SetText(":url", url)

		defer selectFiles.Step()

		if hasRow, err := selectFiles.Step(); err != nil {
			pp(hasRow, err)
			return errors.WithMessage(err, "while executing the query to select files")
		} else if hasRow {
			pp(hasRow)
			h.Set(headerCache, headerCacheHit)
			h.Set(headerContentType, selectFiles.ColumnText(0))
			h.Set(headerCacheUpstream, headerCacheHit)
			w.WriteHeader(http.StatusOK)
			return nil
		}
		pp("no row")

		for _, substituter := range p.config.Namespaces[namespace].Substituters {
			upstream := substituter + strings.TrimPrefix(r.URL.String(), "/"+namespace)

			upstreamResponse, err := http.Head(upstream)
			if err != nil || upstreamResponse.StatusCode != 200 {
				continue
			}

			h.Set(headerContentType, upstreamResponse.Header.Get(headerContentType))
			h.Set(headerCache, headerCacheRemote)
			h.Set(headerCacheUpstream, upstream)
			w.WriteHeader(http.StatusOK)
			return nil
		}

		h.Set(headerCache, headerCacheMiss)
		h.Set(headerContentType, mimeText)
		w.WriteHeader(http.StatusNotFound)

		return nil
	}); err != nil {
		log.Error("on HEAD", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
	}
}

func (p *Proxy) commonGet(w http.ResponseWriter, r *http.Request) {
	panic("GET not implemented")
}

func (p *Proxy) commonPut(w http.ResponseWriter, r *http.Request) {
	log := p.log.With(zap.String("method", r.Method), zap.String("url", r.URL.String()))

	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]
	pp(vars)

	now := time.Now().UnixNano()

	if err := p.withDbReadWrite(func(db *sqlite.Conn) error {
		if r.ContentLength < int64(chunkSizeMin()) {
			insertFiles := db.Prep(`
				INSERT OR IGNORE INTO files
				( url,  namespace,  content_type,  data,  ctime,  atime) VALUES
				(:url, :namespace, :content_type, :data, :ctime, :atime)
			`)
			if err := insertFiles.Reset(); err != nil {
				return err
			}
			defer insertFiles.Step()

			insertFiles.SetText(":url", url)
			insertFiles.SetText(":namespace", namespace)
			insertFiles.SetText(":content_type", urlToMime(r.URL.String()))
			insertFiles.SetInt64(":ctime", now)
			insertFiles.SetInt64(":atime", now)

			data := make([]byte, r.ContentLength)

			if n, err := r.Body.Read(data); err != nil {
				return err
			} else if n != int(r.ContentLength) {
				return errors.New("couldn't read the full body")
			} else {
				insertFiles.SetBytes(":data", data)
			}

			_, err := insertFiles.Step()
			return err
		}

		chunker, err := desync.NewChunker(r.Body, chunkSizeMin(), chunkSizeAvg, chunkSizeMax())
		if err != nil {
			return err
		}

		insertIndices := db.Prep(`
			INSERT INTO indices
	 		( url,  namespace,  content_type,  size, ctime,  atime) VALUES
	 		(:url, :namespace, :content_type, :size, :ctime, :atime)
	 		ON CONFLICT(url, namespace)
	 		DO UPDATE SET atime = :atime
	 		RETURNING ctime
 		`)
		if err := insertIndices.Reset(); err != nil {
			return err
		}
		insertIndices.SetText(":url", url)
		insertIndices.SetText(":namespace", namespace)
		insertIndices.SetText(":content_type", r.Header.Get("Content-Type"))
		insertIndices.SetInt64(":size", r.ContentLength)
		insertIndices.SetText(":namespace", namespace)
		insertIndices.SetInt64(":ctime", now)
		insertIndices.SetInt64(":atime", now)
		if hasRow, err := insertIndices.Step(); err != nil {
			return err
		} else if hasRow {
			if insertIndices.GetInt64("ctime") != now {
				if _, err := insertIndices.Step(); err != nil {
					return err
				}
				return nil
			} else {
				insertIndices.Step()
			}
		}

		insertChunks := db.Prep(`
			INSERT INTO chunks
			( hash,  data,  ctime,  atime) VALUES
			(:hash, :data, :ctime, :atime)
			ON CONFLICT(hash) DO UPDATE SET atime = :atime
		`)

		insertIndicesChunks := db.Prep(`
			INSERT OR IGNORE INTO indices_chunks
			( index_url,  chunk_hash,  offset) VALUES
			(:index_url, :chunk_hash, :offset)
		`)

		for {
			offset, chunk, err := chunker.Next()
			if err != nil {
				return err
			}

			chunkHash := desync.Digest.Sum(chunk)
			chunkHashHex := fmt.Sprintf("%x", chunkHash)

			if len(chunk) == 0 {
				break
			}

			if err := insertChunks.Reset(); err != nil {
				return err
			}
			insertChunks.SetText(":hash", chunkHashHex)
			insertChunks.SetBytes(":data", chunk)
			insertChunks.SetInt64(":ctime", now)
			insertChunks.SetInt64(":atime", now)
			if _, err := insertChunks.Step(); err != nil {
				return err
			} else {
				if err := insertIndicesChunks.Reset(); err != nil {
					return err
				}
				insertIndicesChunks.SetText(":index_url", url)
				insertIndicesChunks.SetText(":chunk_hash", chunkHashHex)
				insertIndicesChunks.SetInt64(":offset", int64(offset))

				if _, err := insertIndicesChunks.Step(); err != nil {
					return err
				}
			}
		}

		return nil
	}); err != nil {
		log.Error("modifying database", zap.Error(err), zap.String("url", r.URL.String()))
		w.WriteHeader(500)
	} else {
		w.WriteHeader(200)
	}
}

// func (proxy *Proxy) withLocalCacheHandler(namespace string) mux.MiddlewareFunc {
// 	return withCacheHandler(
// 		proxy.log,
// 		proxy.localStore,
// 		proxy.localIndices[namespace],
// 		proxy.trustedKeys[namespace],
// 		proxy.secretKeys[namespace],
// 	)
// }
//
// func (proxy *Proxy) withS3CacheHandler(namespace string) mux.MiddlewareFunc {
// 	return withCacheHandler(
// 		proxy.log,
// 		proxy.s3Store,
// 		proxy.s3Indices[namespace],
// 		proxy.trustedKeys[namespace],
// 		proxy.secretKeys[namespace],
// 	)
// }

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

func answer(w http.ResponseWriter, status int, mime, msg string) {
	w.Header().Set(headerContentType, mime)
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg)
}

func (proxy *Proxy) nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	if namespace, ok := mux.Vars(r)["namespace"]; !ok {
		panic("namespace not given")
	} else if ns, ok := proxy.config.Namespaces[namespace]; !ok {
		panic("namespace not found")
	} else if r.Method == http.MethodHead {
		w.Header().Set(headerContentType, mimeNixCacheInfo)
		w.WriteHeader(http.StatusOK)
	} else {
		answer(w, http.StatusOK, mimeNixCacheInfo, `StoreDir: /nix/store
WantMassQuery: 1
Priority: `+strconv.FormatUint(ns.CacheInfoPriority, 10))
	}
}
