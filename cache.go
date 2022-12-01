package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/folbricht/desync"
	"github.com/gorilla/mux"
	"github.com/jamespfennell/xz"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

const (
	headerCache         = "X-Cache"
	headerCacheHit      = "HIT"
	headerCacheRemote   = "REMOTE"
	headerCacheMiss     = "MISS"
	headerCacheUpstream = "X-Cache-Upstream"
	headerContentType   = "Content-Type"
)

var (
	narSuffix = regexp.MustCompile(".nar.xz$")
)

func urlToMime(u string) string {
	switch filepath.Ext(u) {
	case ".nar", ".xz":
		return mimeNar
	case ".narinfo":
		return mimeNarinfo
	case ".doi":
		return mimeJson
	default:
		return mimeText
	}
}

func getIndex(index desync.IndexStore, r *http.Request) (i desync.Index, err error) {
	return index.GetIndex(urlToIndexName(r))
}

func urlToIndexName(r *http.Request) string {
	vars := mux.Vars(r)
	switch vars["ext"] {
	case ".narinfo":
		return vars["hash"] + vars["ext"]
	case ".doi":
		return vars["hash"] + "!" + vars["output"] + vars["ext"]
	default:
		return narSuffix.ReplaceAllLiteralString("nar/"+vars["hash"]+vars["ext"], ".nar")
	}
}

func withCacheHandler(
	log *zap.Logger,
	store desync.WriteStore,
	index desync.IndexWriteStore,
	trustedKeys []signature.PublicKey,
	secretKeys signature.SecretKey,
) func(http.Handler) http.Handler {
	if store == nil || index == nil {
		return func(h http.Handler) http.Handler {
			return h
		}
	}

	return func(h http.Handler) http.Handler {
		return &cacheHandler{
			handler:     h,
			log:         log,
			store:       store,
			index:       index,
			trustedKeys: trustedKeys,
			secretKey:   secretKeys,
		}
	}
}

type cacheHandler struct {
	log         *zap.Logger
	handler     http.Handler
	store       desync.WriteStore
	index       desync.IndexWriteStore
	trustedKeys []signature.PublicKey
	secretKey   signature.SecretKey
}

func (c cacheHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "HEAD":
		c.Head(w, r)
	case "GET":
		c.Get(w, r)
	case "PUT":
		c.Put(w, r)
	default:
		c.handler.ServeHTTP(w, r)
	}
}

func (c cacheHandler) Head(w http.ResponseWriter, r *http.Request) {
	idx, err := getIndex(c.index, r)
	if err != nil {
		c.handler.ServeHTTP(w, r)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(idx.Length(), 10))
	w.Header().Set(headerCache, headerCacheHit)
	w.Header().Set(headerContentType, urlToMime(r.URL.String()))
	w.WriteHeader(200)
}

func (c cacheHandler) Get(w http.ResponseWriter, r *http.Request) {
	idx, err := getIndex(c.index, r)
	if err != nil {
		c.handler.ServeHTTP(w, r)
		return
	}

	wr := io.Writer(w)
	if filepath.Ext(r.URL.String()) == ".xz" {
		xzWr := xz.NewWriterLevel(w, xz.BestSpeed)
		defer xzWr.Close()
		wr = xzWr
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(idx.Length(), 10))
	}

	w.Header().Set(headerCache, headerCacheHit)
	w.Header().Set(headerContentType, urlToMime(r.URL.String()))
	for _, indexChunk := range idx.Chunks {
		if chunk, err := c.store.GetChunk(indexChunk.ID); err != nil {
			c.log.Error("while getting chunk", zap.Error(err))
			break
		} else if data, err := chunk.Data(); err != nil {
			c.log.Error("while reading chunk data", zap.Error(err))
			break
		} else if _, err := wr.Write(data); err != nil {
			c.log.Error("while writing chunk data", zap.Error(err))
			break
		}
	}
}

func answer(w http.ResponseWriter, status int, mime, msg string) {
	w.Header().Set(headerContentType, mime)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

func (c cacheHandler) Put(w http.ResponseWriter, r *http.Request) {
	urlExt := filepath.Ext(r.URL.String())
	switch urlExt {
	case ".narinfo":
		if info, err := narinfo.Parse(r.Body); err != nil {
			c.log.Error("unmarshaling narinfo", zap.Error(err))
			answer(w, http.StatusBadRequest, mimeText, err.Error())
		} else if err := c.signUnsignedNarinfo(info); err != nil {
			c.log.Error("failed signing narinfo", zap.Error(err))
			answer(w, http.StatusInternalServerError, mimeText, "failed signing narinfo")
		} else {
			narExt := filepath.Ext(info.URL)
			if narExt != ".nar" {
				info.URL = strings.TrimSuffix(info.URL, narExt)
			}
			info.Compression = "none"
			c.putCommon(w, r, strings.NewReader(info.String()))
		}
	case ".doi":
		rs := realisation{}
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&rs); err != nil {
			c.log.Error("failed decoding realisation", zap.Error(err))
			answer(w, http.StatusInternalServerError, mimeText, "failed decoding realisation")
		} else if enc, err := json.Marshal(rs); err != nil {
			c.log.Error("failed encoding realisation", zap.Error(err))
			answer(w, http.StatusInternalServerError, mimeText, "failed encoding realisation")
		} else {
			pp(rs)
			c.putCommon(w, r, bytes.NewReader(enc))
		}
	case ".nar":
		c.putCommon(w, r, r.Body)
	case ".xz":
		xzRd := xz.NewReader(r.Body)
		defer xzRd.Close()
		c.putCommon(w, r, xzRd)
	default:
		answer(w, http.StatusBadRequest, mimeText, "compression is not supported\n")
	}
}

type realisation struct {
	DependentRealisations map[string]string `json:"dependentRealisations"`
	ID                    string            `json:"id"`
	OutPath               string            `json:"outPath"`
	Signatures            []string
}

func (c cacheHandler) signUnsignedNarinfo(info *narinfo.NarInfo) error {
	if len(info.Signatures) > 0 {
		return nil
	}
	if sig, err := c.secretKey.Sign(nil, info.Fingerprint()); err != nil {
		return errors.WithMessagef(err, "signing narinfo for '%s'", info.StorePath)
	} else {
		info.Signatures = []signature.Signature{sig}
		return nil
	}
}

func (c cacheHandler) putCommon(w http.ResponseWriter, r *http.Request, rd io.Reader) {
	if chunker, err := desync.NewChunker(rd, chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
		c.log.Error("making chunker", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, "making chunker")
	} else if idx, err := desync.ChunkStream(context.Background(), chunker, c.store, defaultThreads); err != nil {
		c.log.Error("chunking body", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, "chunking body")
	} else if err := c.index.StoreIndex(urlToIndexName(r), idx); err != nil {
		c.log.Error("storing index", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, "storing index")
	} else {
		answer(w, http.StatusOK, mimeText, "ok\n")
	}
}

type remoteHandler struct {
	log          *zap.Logger
	handler      http.Handler
	substituters []*url.URL
	exts         []string
	cacheChan    chan *cacheRequest
}

func withRemoteHandler(log *zap.Logger, substituters, exts []string, cacheChan chan *cacheRequest) func(http.Handler) http.Handler {
	parsedSubstituters := []*url.URL{}
	for _, raw := range substituters {
		u, err := url.Parse(raw)
		if err != nil {
			panic(err)
		}
		parsedSubstituters = append(parsedSubstituters, u)
	}

	return func(h http.Handler) http.Handler {
		return &remoteHandler{
			log:          log,
			handler:      h,
			exts:         exts,
			substituters: parsedSubstituters,
			cacheChan:    cacheChan,
		}
	}
}

func (h *remoteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	hash := vars["hash"]
	exts := h.exts
	timeout := 30 * time.Minute
	switch vars["ext"] {
	case ".nar", ".nar.xz":
		exts = []string{".nar", ".nar.xz"}
	case ".narinfo":
		timeout = 10 * time.Second
		exts = []string{".narinfo"}
	case "":
		h.handler.ServeHTTP(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	routines := len(h.substituters) * len(exts)
	resChan := make(chan *http.Response, routines)
	wg := &sync.WaitGroup{}

	for _, substituter := range h.substituters {
		for _, ext := range exts {
			var substituterAndPath string
			if ext == ".narinfo" {
				substituterAndPath = substituter.String() + "/" + hash + ext
			} else {
				substituterAndPath = substituter.String() + "/nar/" + hash + ext
			}

			request, err := http.NewRequestWithContext(ctx, r.Method, substituterAndPath, nil)
			if err != nil {
				h.log.Error("creating request", zap.String("url", request.URL.String()), zap.Error(err))
				continue
			}

			wg.Add(1)
			go func(request *http.Request) {
				defer wg.Done()
				res, err := http.DefaultClient.Do(request)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						h.log.Error("fetching upstream", zap.String("url", request.URL.String()), zap.Error(err))
					}
				} else if res.StatusCode/100 == 2 {
					select {
					case resChan <- res:
					case <-ctx.Done():
					}
				}
			}(request)
		}
	}

	allDone := make(chan bool)
	go func() {
		wg.Wait()
		select {
		case allDone <- true:
		case <-ctx.Done():
		}
	}()

	select {
	case <-allDone:
		// got no good responses
	case <-ctx.Done():
		// ran out of time
	case response := <-resChan:
		h.cacheChan <- &cacheRequest{
			namespace: namespace,
			url:       response.Request.URL.String(),
			indexName: urlToIndexName(r),
		}
		// w.Header().Set("Content-Length", strconv.FormatInt(idx.Length(), 10))
		w.Header().Set(headerCache, headerCacheRemote)
		w.Header().Set(headerContentType, urlToMime(response.Request.URL.String()))
		w.Header().Set(headerCacheUpstream, response.Request.URL.String())

		body := response.Body
		if strings.HasSuffix(r.URL.String(), ".nar") && strings.HasSuffix(response.Request.URL.String(), ".xz") {
			body = xz.NewReader(response.Body)
		}

		_, _ = io.Copy(w, body)
		return
	}

	h.handler.ServeHTTP(w, r)
}

func (proxy *Proxy) cacheUrl(cr *cacheRequest) error {
	response, err := http.Get(cr.url)
	if err != nil {
		return errors.WithMessage(err, "getting URL")
	}

	if response.StatusCode/100 != 2 {
		return errors.WithMessagef(err, "received status %d", response.StatusCode)
	}

	defer response.Body.Close()

	if strings.HasSuffix(cr.url, ".nar") || strings.HasSuffix(cr.url, ".narinfo") {
		if chunker, err := desync.NewChunker(response.Body, chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
			return errors.WithMessage(err, "making chunker")
		} else if idx, err := desync.ChunkStream(context.Background(), chunker, proxy.localStore, defaultThreads); err != nil {
			return errors.WithMessage(err, "chunking body")
		} else if err := proxy.localIndices[cr.namespace].StoreIndex(cr.indexName, idx); err != nil {
			return errors.WithMessage(err, "storing index")
		}
	} else if strings.HasSuffix(cr.url, ".nar.xz") {
		xzRd := xz.NewReader(response.Body)
		if chunker, err := desync.NewChunker(xzRd, chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
			return errors.WithMessage(err, "making chunker")
		} else if idx, err := desync.ChunkStream(context.Background(), chunker, proxy.localStore, defaultThreads); err != nil {
			return errors.WithMessage(err, "chunking body")
		} else if err := proxy.localIndices[cr.namespace].StoreIndex(cr.indexName, idx); err != nil {
			return errors.WithMessage(err, "storing index")
		}
	} else {
		return fmt.Errorf("unexpected extension in url: %s", cr.url)
	}

	return nil
}

var (
	metricRemoteCachedFail = metrics.MustCounter("spongix_remote_cache_fail", "Number of upstream cache entries failed to copy")
	metricRemoteCachedOk   = metrics.MustCounter("spongix_remote_cache_ok", "Number of upstream cache entries copied")
)

type cacheRequest struct {
	namespace string
	url       string
	indexName string
}

func (proxy *Proxy) startCache() {
	for cr := range proxy.cacheChan {
		proxy.log.Info("Caching", zap.String("namespace", cr.namespace), zap.String("url", cr.url))
		if err := proxy.cacheUrl(cr); err != nil {
			metricRemoteCachedFail.Add(1)
			proxy.log.Error("Caching failed", zap.String("namespace", cr.namespace), zap.String("url", cr.url), zap.Error(err))
		} else {
			metricRemoteCachedOk.Add(1)
			proxy.log.Info("Cached", zap.String("namespace", cr.namespace), zap.String("url", cr.url))
		}
	}
}
