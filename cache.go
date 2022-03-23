package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/folbricht/desync"
	"github.com/jamespfennell/xz"
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

func urlToMime(u string) string {
	switch filepath.Ext(u) {
	case ".nar", ".xz":
		return mimeNar
	case ".narinfo":
		return mimeNarinfo
	default:
		return mimeText
	}
}

func getIndex(index desync.IndexStore, url *url.URL) (i desync.Index, err error) {
	if name, err := urlToIndexName(url); err != nil {
		return i, err
	} else {
		return index.GetIndex(name)
	}
}

func storeIndex(index desync.IndexWriteStore, url *url.URL, idx desync.Index) error {
	if name, err := urlToIndexName(url); err != nil {
		return err
	} else {
		return index.StoreIndex(name, idx)
	}
}

func urlToIndexName(url *url.URL) (string, error) {
	name := url.EscapedPath()
	if strings.HasPrefix(name, "/cache/") {
		name = strings.Replace(name, "/cache/", "/", 1)
	}
	if strings.HasSuffix(name, ".nar.xz") {
		name = strings.Replace(name, ".nar.xz", ".nar", 1)
	}
	if name, err := filepath.Rel("/", name); err != nil {
		return name, err
	} else {
		return name, nil
	}
}

type cacheHandler struct {
	log         *zap.Logger
	handler     http.Handler
	store       desync.WriteStore
	index       desync.IndexWriteStore
	trustedKeys map[string]ed25519.PublicKey
	secretKeys  map[string]ed25519.PrivateKey
}

func withCacheHandler(
	log *zap.Logger,
	store desync.WriteStore,
	index desync.IndexWriteStore,
	trustedKeys map[string]ed25519.PublicKey,
	secretKeys map[string]ed25519.PrivateKey,
) func(http.Handler) http.Handler {
	if store == nil || index == nil {
		return func(h http.Handler) http.Handler {
			return h
		}
	}

	return func(h http.Handler) http.Handler {
		return &cacheHandler{handler: h,
			log:         log,
			store:       store,
			index:       index,
			trustedKeys: trustedKeys,
			secretKeys:  secretKeys,
		}
	}
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
	idx, err := getIndex(c.index, r.URL)
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
	idx, err := getIndex(c.index, r.URL)
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
		info := &Narinfo{}
		if err := info.Unmarshal(r.Body); err != nil {
			c.log.Error("unmarshaling narinfo", zap.Error(err))
			answer(w, http.StatusBadRequest, mimeText, err.Error())
		} else if infoRd, err := info.PrepareForStorage(c.trustedKeys, c.secretKeys); err != nil {
			c.log.Error("failed serializing narinfo", zap.Error(err))
			answer(w, http.StatusInternalServerError, mimeText, "failed serializing narinfo")
		} else {
			c.putCommon(w, r, infoRd)
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

func (c cacheHandler) putCommon(w http.ResponseWriter, r *http.Request, rd io.Reader) {
	if chunker, err := desync.NewChunker(rd, chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
		c.log.Error("making chunker", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, "making chunker")
	} else if idx, err := desync.ChunkStream(context.Background(), chunker, c.store, defaultThreads); err != nil {
		c.log.Error("chunking body", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, "chunking body")
	} else if err := storeIndex(c.index, r.URL, idx); err != nil {
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
	cacheChan    chan string
}

func withRemoteHandler(log *zap.Logger, substituters, exts []string, cacheChan chan string) func(http.Handler) http.Handler {
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
	exts := h.exts
	urlExt := filepath.Ext(r.URL.String())
	timeout := 30 * time.Minute
	switch urlExt {
	case ".nar":
	case ".xz":
		exts = []string{""}
	case ".narinfo":
		timeout = 10 * time.Second
		exts = []string{""}
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
			u, err := substituter.Parse(r.URL.String() + ext)
			if err != nil {
				h.log.Error("parsing url", zap.String("url", r.URL.String()+ext), zap.Error(err))
				continue
			}

			request, err := http.NewRequestWithContext(ctx, r.Method, u.String(), nil)
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
		h.cacheChan <- response.Request.URL.String()
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

func (proxy *Proxy) cacheUrl(urlStr string) error {
	u, err := url.Parse(urlStr)
	if err != nil {
		return errors.WithMessage(err, "parsing URL")
	}

	response, err := http.Get(urlStr)
	if err != nil {
		return errors.WithMessage(err, "getting URL")
	}

	if response.StatusCode/100 != 2 {
		return errors.WithMessagef(err, "received status %d", response.StatusCode)
	}

	defer response.Body.Close()

	if strings.HasSuffix(urlStr, ".nar") || strings.HasSuffix(urlStr, ".narinfo") {
		if chunker, err := desync.NewChunker(response.Body, chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
			return errors.WithMessage(err, "making chunker")
		} else if idx, err := desync.ChunkStream(context.Background(), chunker, proxy.localStore, defaultThreads); err != nil {
			return errors.WithMessage(err, "chunking body")
		} else if err := storeIndex(proxy.localIndex, u, idx); err != nil {
			return errors.WithMessage(err, "storing index")
		}
	} else if strings.HasSuffix(urlStr, ".nar.xz") {
		xzRd := xz.NewReader(response.Body)
		if chunker, err := desync.NewChunker(xzRd, chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
			return errors.WithMessage(err, "making chunker")
		} else if idx, err := desync.ChunkStream(context.Background(), chunker, proxy.localStore, defaultThreads); err != nil {
			return errors.WithMessage(err, "chunking body")
		} else if err := storeIndex(proxy.localIndex, u, idx); err != nil {
			return errors.WithMessage(err, "storing index")
		}
	} else {
		return fmt.Errorf("unexpected extension in url: %s", urlStr)
	}

	return nil
}

var (
	metricRemoteCachedFail = metrics.MustCounter("spongix_remote_cache_fail", "Number of upstream cache entries failed to copy")
	metricRemoteCachedOk   = metrics.MustCounter("spongix_remote_cache_ok", "Number of upstream cache entries copied")
)

func (proxy *Proxy) startCache() {
	for urlStr := range proxy.cacheChan {
		proxy.log.Info("Caching", zap.String("url", urlStr))
		if err := proxy.cacheUrl(urlStr); err != nil {
			metricRemoteCachedFail.Add(1)
			proxy.log.Error("Caching failed", zap.String("url", urlStr), zap.Error(err))
		} else {
			metricRemoteCachedOk.Add(1)
			proxy.log.Info("Cached", zap.String("url", urlStr))
		}
	}
}
