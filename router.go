package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/input-output-hk/spongix/pkg/compress"

	"github.com/folbricht/desync"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

const (
	mimeNarinfo      = "text/x-nix-narinfo"
	mimeNar          = "application/x-nix-nar"
	mimeText         = "text/plain; charset=utf-8"
	mimeNixCacheInfo = "text/x-nix-cache-info"
	mimeJson         = "application/json"

	matchNarinfo     = "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo"
	matchNar         = "/nar/{hash:[0-9a-df-np-sv-z]{52}}.nar{ext:.?[a-z]*}"
	matchRealisation = "/realisations/sha256:{hash:[0-9a-f]{64}![^.]+}.doi"
	matchLog         = "/log/{hash:[0-9a-df-np-sv-z]{32}-[^.]+}.drv"

	narPrefix         = "nar"
	narinfoPrefix     = "narinfo"
	realisationPrefix = "realisations"
	logPrefix         = "log"
)

func (proxy *Proxy) router() *mux.Router {
	r := mux.NewRouter()
	r.NotFoundHandler = notFound{}
	r.MethodNotAllowedHandler = notAllowed{}
	r.Use(
		withHTTPLogging(proxy.log),
		handlers.RecoveryHandler(handlers.PrintRecoveryStack(true)),
	)
	r.Use(compress.CompressHandler)

	r.HandleFunc("/metrics", metrics.ServeHTTP)

	for name := range proxy.config.Namespaces {
		namespace := r.Name("namespace").PathPrefix("/{namespace:" + name + "}").Subrouter()

		namespace.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("HEAD", "GET")

		namespace.HandleFunc(matchNarinfo, proxy.largeHeadAndGet(narinfoPrefix, mimeNarinfo)).Methods("HEAD", "GET")
		namespace.HandleFunc(matchNarinfo, proxy.largePut(narinfoPrefix)).Methods("PUT")

		namespace.HandleFunc(matchNar, proxy.largeHeadAndGet(narPrefix, mimeNar)).Methods("HEAD", "GET")
		namespace.HandleFunc(matchNar, proxy.largePut(narPrefix)).Methods("PUT")

		namespace.HandleFunc(matchRealisation, proxy.largeHeadAndGet(realisationPrefix, mimeJson)).Methods("HEAD", "GET")
		namespace.HandleFunc(matchRealisation, proxy.largePut(realisationPrefix)).Methods("PUT")

		namespace.HandleFunc(matchLog, proxy.largeHeadAndGet(logPrefix, mimeText)).Methods("HEAD", "GET")
		namespace.HandleFunc(matchLog, proxy.largePut(logPrefix)).Methods("PUT")
	}

	return r
}

func indexPathFor(kind string, r *http.Request) string {
	vars := mux.Vars(r)
	hash := vars["hash"]
	if len(hash) > 4 {
		return filepath.Join("indices", kind, hash[0:4], hash)
	} else {
		return filepath.Join("indices", kind, hash)
	}
}

type NarEntry struct {
	Header *nar.Header
	Index  *desync.Index
}

type Chunk struct {
	SHA256 []byte
	Offset uint64
	Size   int
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

func answer(w http.ResponseWriter, status int, mime, msg string) {
	w.Header().Set(headerContentType, mime)
	w.WriteHeader(status)
	if msg != "" {
		_, _ = io.WriteString(w, msg)
	}
}

func (p *Proxy) nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	if namespace, ok := mux.Vars(r)["namespace"]; !ok {
		panic("namespace not given")
	} else if ns, ok := p.config.Namespaces[namespace]; !ok {
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

func (p *Proxy) redirectToUpstream(location string, w http.ResponseWriter, r *http.Request) {
	if namespace, ok := mux.Vars(r)["namespace"]; !ok {
		panic("namespace not given")
	} else if ns, ok := p.config.Namespaces[namespace]; !ok {
		panic("namespace not found")
	} else {
		group := p.headPool.Group()
		first := make(chan string, len(ns.Substituters))

		for _, substituter := range ns.Substituters {
			pp(substituter)
			substituterUrl, err := url.ParseRequestURI(substituter)
			if err != nil {
				panic(err)
			}

			client := http.Client{}
			client.Timeout = 1 * time.Second

			group.Submit(func() {
				substituterUrl.Path = filepath.Join(substituterUrl.Path, strings.TrimPrefix(r.URL.Path, "/"+namespace))
				substituterUrlString := substituterUrl.String()
				p.log.Info("URL", zap.String("url", substituterUrlString))

				if response, err := client.Head(substituterUrlString); err == nil {
					defer response.Body.Close()
					if response.StatusCode == http.StatusOK {
						first <- substituterUrlString
					}
				}
			})
		}

		group.Wait()

		// p.pool.GroupContext

		select {
		case found := <-first:
			p.cachePool.TrySubmit(func() {
				p.doCache(&cacheRequest{namespace: namespace, url: found, location: location})
			})

			http.Redirect(w, r, found, http.StatusFound)
		case <-time.After(500 * time.Millisecond):
			serveNotFound(w, r)
		}
	}
}

func (p *Proxy) largeHeadAndGet(prefix, mime string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		location := indexPathFor(prefix, r)
		namespace := mux.Vars(r)["namespace"]

		if indices, ok := p.s3Indices[namespace]; !ok {
			serveNotFound(w, r)
		} else if index, err := indices.GetIndex(location); err != nil {
			if err.Error() == "reading index: The specified key does not exist." {
				p.redirectToUpstream(location, w, r)
			} else {
				// p.log.Error("getting index", zap.String("index", location), zap.Error(err))
				serveNotFound(w, r)
			}
		} else {
			w.Header().Set("Content-Type", mime)
			rd := desync.NewIndexReadSeeker(index, p.s3Store)
			http.ServeContent(w, r, r.URL.Path, time.Now(), rd)
		}
	}
}

func (p *Proxy) largePut(prefix string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		namespace := mux.Vars(r)["namespace"]
		location := indexPathFor(prefix, r)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		if err := p.insert(ctx, namespace, location, r.Body); err != nil {
			p.log.Error("inserting", zap.String("index", location), zap.Error(err))
			answer(w, http.StatusInternalServerError, mimeText, err.Error())
		} else {
			p.log.Info("stored", zap.String("location", location))
			w.WriteHeader(http.StatusCreated)
		}
	}
}

func (p *Proxy) insert(ctx context.Context, namespace, location string, body io.Reader) error {
	if indices, ok := p.s3Indices[namespace]; !ok {
		return errors.Errorf("namespace '%s' not found", namespace)
	} else if chunker, err := desync.NewChunker(body, p.config.Chunks.MinSize, p.config.Chunks.AvgSize, p.config.Chunks.MaxSize); err != nil {
		return errors.WithMessage(err, "failed creating chunker")
	} else if index, err := desync.ChunkStream(ctx, chunker, p.s3Store, defaultThreads); err != nil {
		return errors.WithMessage(err, "failed chunking")
	} else if err := indices.StoreIndex(location, index); err != nil {
		return errors.WithMessage(err, "failed storing index")
	} else {
		p.log.Info("stored", zap.String("location", location), zap.Int("chunks", len(index.Chunks)))
		return nil
	}
}
