package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/numtide/go-nix/nar"
	"github.com/pascaldekloe/metrics"
	"github.com/ulikunitz/xz"
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
	r.Use(withHTTPLogging(proxy.log), handlers.RecoveryHandler())

	r.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("GET")
	r.HandleFunc("/metrics", metrics.ServeHTTP)

	narinfo := "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo"
	r.HandleFunc(narinfo, proxy.narinfoHead).Methods("HEAD")
	r.HandleFunc(narinfo, proxy.narinfoGet).Methods("GET")
	r.HandleFunc(narinfo, proxy.narinfoPut).Methods("PUT")

	nar := "/nar/{hash:[0-9a-df-np-sv-z]{52}}{ext:\\.nar(?:\\.xz|)}"
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
	rd.Close()
	if internalServerError(w, err) {
		proxy.log.Error("unmarshaling narinfo", zap.Error(err))
		return
	}

	info.SanitizeNar()

	if len(info.Sig) == 0 {
		for name, key := range proxy.secretKeys {
			info.Sign(name, key)
		}
	} else {
		info.SanitizeSignatures(proxy.trustedKeys)
		if len(info.Sig) == 0 {
			// return
		}
	}

	fetchNars <- info

	w.Header().Add(headerContentType, mimeNarinfo)
	if err := info.Marshal(w); err != nil {
		proxy.log.Error("marshaling narinfo", zap.Error(err))
	}

	// proxy.serveChunks(w, r, mimeNarinfo, proxy.narinfoGetInner(w, r))
}

var fetchNars chan *Narinfo

func (proxy *Proxy) startNarFetchers() {
	fetchNars = make(chan *Narinfo, 1000)

	for n := 0; n < 2; n++ {
		go func() {
			for info := range fetchNars {
				url := info.URL

				_, err := proxy.getIndex(url)
				if err == nil {
					continue
				}

				narRes := proxy.parallelRequest(narGetTimeout, "GET", url)
				if narRes == nil {
					continue
				}

				proxy.log.Debug("Fetching NAR for narinfo", zap.String("nar", url), zap.String("name", info.Name))
				l, err := io.Copy(io.Discard, narRes)
				if err != nil {
					proxy.log.Error("copying NAR", zap.Error(err), zap.String("url", url), zap.Int64("len", l))
					continue
				}

				idx, err := proxy.getIndex(url)
				if err != nil {
					proxy.log.Error("get NAR index", zap.Error(err), zap.String("url", url))
					continue
				}

				narRd := nar.NewReader(newAssembler(proxy.localStore, idx))
				for {
					_, err := narRd.Next()
					if err != nil {
						if err == io.EOF {
							break
						}
						proxy.log.Error("checking NAR", zap.Error(err), zap.String("url", url))
						break
					}
				}
			}
		}()
	}
}

// PUT /<hash>.narinfo
func (proxy *Proxy) narinfoPut(w http.ResponseWriter, r *http.Request) {
	status, err := proxy.narinfoPutInner(w, r)
	w.Header().Add(headerContentType, mimeText)
	w.WriteHeader(status)
	if err != nil {
		w.Write([]byte(err.Error()))
	} else {
		w.Write([]byte("ok"))
	}
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
		w.Write([]byte("ok"))
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
		http.ServeFile(w, r, res.Name())
	case *parallelResponse:
		w.Header().Add(headerContentType, mime)
		w.WriteHeader(200)
		if n, err := io.Copy(w, res); err != nil {
			proxy.log.Error("failed copying chunks to response", zap.Error(err))
		} else {
			proxy.log.Debug("copied parallelResponse", zap.Int64("bytes", n))
		}
		res.cancel()
	case io.ReadCloser:
		wr := io.Writer(w)
		// NOTE: this is a hack to work around clients that have existing .narinfo
		// files that reference .nar.xz URLs.
		// Nix doesn't know which cache a .narinfo came from, so it will just ask
		// us for it.
		if filepath.Ext(r.URL.String()) == ".xz" {
			xzWr, err := xz.NewWriter(w)
			if err != nil {
				w.Header().Add(headerContentType, mimeText)
				w.WriteHeader(500)
				w.Write([]byte(err.Error()))
				return
			} else {
				wr = xzWr
				w.Header().Add(headerContentType, mime)
				w.WriteHeader(200)

				if n, err := io.Copy(wr, res); err != nil {
					proxy.log.Error("failed copying chunks to response", zap.Error(err))
				} else {
					proxy.log.Debug("copied io.ReadCloser through xz", zap.Int64("bytes", n))
				}
				res.Close()
				if err := xzWr.Close(); err != nil {
					proxy.log.Error("failed closing xz writer", zap.Error(err))
				}
				return
			}
		}

		w.Header().Add(headerContentType, mime)
		w.WriteHeader(200)

		if n, err := io.Copy(wr, res); err != nil {
			proxy.log.Error("failed copying chunks to response", zap.Error(err))
		} else {
			proxy.log.Debug("copied io.ReadCloser", zap.Int64("bytes", n))
		}
		res.Close()
	default:
		proxy.log.DPanic("unknown type", zap.Any("value", res))
	}
}
