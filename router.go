package main

import (
	"bufio"
	"bytes"
	"compress/bzip2"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/folbricht/desync"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"encoding/json"

	"github.com/input-output-hk/spongix/pkg/compress"
	"github.com/input-output-hk/spongix/pkg/store"
	"github.com/klauspost/compress/zstd"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/pascaldekloe/metrics"
	"github.com/ulikunitz/xz"
	"go.uber.org/zap"
)

const (
	mimeNarinfo      = "text/x-nix-narinfo"
	mimeNar          = "application/x-nix-nar"
	mimeText         = "text/plain; charset=utf-8"
	mimeNixCacheInfo = "text/x-nix-cache-info"
	mimeJson         = "application/json"

	matchNarinfo     = "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo"
	matchNar         = "/nar/{hash:[0-9a-df-np-sv-z]{52}}.nar{ext:(?:\\.[a-z][a-z0-9]*)?}"
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

	// Pin API: disabled by default. When enabled, requires bearer token auth.
	// These routes are global (not namespace-scoped) since pins are DB-wide.
	//
	// Threat model:
	//   - GC denial: attacker pins all hashes, preventing eviction
	//   - Store path probing: GET /pins reveals cached paths; PUT confirms existence
	//   - Disk exhaustion: preventing GC while pushing data
	// Mitigations: API disabled by default, bearer token required, token loaded
	// via LoadCredential (never in Nix store). Rate limiting can be layered via
	// reverse proxy.
	if proxy.config.PinAPI != nil && proxy.config.PinAPI.Enable && proxy.pebbleDB != nil {
		matchPinHash := "/pin/{hash:[0-9a-df-np-sv-z]{32}}"
		r.HandleFunc(matchPinHash, proxy.requireBearerToken(proxy.pinHash)).Methods("PUT")
		r.HandleFunc(matchPinHash, proxy.requireBearerToken(proxy.unpinHash)).Methods("DELETE")
		r.HandleFunc("/pins", proxy.requireBearerToken(proxy.listPins)).Methods("GET")
	}

	return r
}

// requireBearerToken wraps a handler with bearer token authentication.
func (p *Proxy) requireBearerToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		token := auth[len(prefix):]
		if token != p.config.PinAPI.BearerToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// pinHash handles PUT /pin/{hash}.
// Race condition note: If a pin arrives while GC is mid-run and has already
// scanned past this hash in findRoots, the data may be evicted this cycle.
// The pin persists in Pebble and takes effect on the next GC cycle.
func (p *Proxy) pinHash(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	if err := p.pebbleDB.PinHash(hash); err != nil {
		p.log.Error("pinning hash", zap.String("hash", hash), zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unpinHash handles DELETE /pin/{hash}.
func (p *Proxy) unpinHash(w http.ResponseWriter, r *http.Request) {
	hash := mux.Vars(r)["hash"]
	if err := p.pebbleDB.UnpinHash(hash); err != nil {
		p.log.Error("unpinning hash", zap.String("hash", hash), zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listPins handles GET /pins.
func (p *Proxy) listPins(w http.ResponseWriter, r *http.Request) {
	pins, err := p.pebbleDB.ListPins()
	if err != nil {
		p.log.Error("listing pins", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	if pins == nil {
		pins = []string{}
	}
	w.Header().Set("Content-Type", mimeJson)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pins)
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
	w.WriteHeader(http.StatusMethodNotAllowed)
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

func answer(w http.ResponseWriter, status int, mime, msg string) {
	w.Header().Set(headerContentType, mime)
	w.WriteHeader(status)
	if msg != "" {
		_, _ = io.WriteString(w, msg)
	}
}

type zstdReadCloser struct{ *zstd.Decoder }

func (z *zstdReadCloser) Close() error { z.Decoder.Close(); return nil }

func decompressReader(ext string, body io.Reader) (io.ReadCloser, error) {
	switch ext {
	case "":
		return io.NopCloser(body), nil
	case ".xz":
		r, err := xz.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("creating xz reader: %w", err)
		}
		return io.NopCloser(r), nil
	case ".zst":
		r, err := zstd.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("creating zstd reader: %w", err)
		}
		return &zstdReadCloser{r}, nil
	case ".bz2":
		return io.NopCloser(bzip2.NewReader(body)), nil
	case ".br":
		return io.NopCloser(brotli.NewReader(body)), nil
	default:
		return nil, fmt.Errorf("unsupported compression extension: %q", ext)
	}
}

func (p *Proxy) nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	if namespace, ok := mux.Vars(r)["namespace"]; !ok {
		p.log.Warn("namespace not given", zap.String("url", r.URL.String()))
		serveNotFound(w, r)
	} else if ns, ok := p.config.Namespaces[namespace]; !ok {
		p.log.Warn("namespace not found", zap.String("namespace", namespace))
		serveNotFound(w, r)
	} else if r.Method == http.MethodHead {
		w.Header().Set(headerContentType, mimeNixCacheInfo)
		w.WriteHeader(http.StatusOK)
	} else {
		answer(w, http.StatusOK, mimeNixCacheInfo, `StoreDir: /nix/store
WantMassQuery: 1
Priority: `+strconv.FormatUint(ns.CacheInfoPriority, 10))
	}
}

func (p *Proxy) redirectToUpstream(prefix, location string, w http.ResponseWriter, r *http.Request) {
	if namespace, ok := mux.Vars(r)["namespace"]; !ok {
		p.log.Warn("namespace not given", zap.String("url", r.URL.String()))
		serveNotFound(w, r)
	} else if ns, ok := p.config.Namespaces[namespace]; !ok {
		p.log.Warn("namespace not found", zap.String("namespace", namespace))
		serveNotFound(w, r)
	} else {
		group := p.headPool.Group()
		first := make(chan string, len(ns.Substituters))

		for _, substituter := range ns.Substituters {
			substituterUrl, err := url.ParseRequestURI(substituter)
			if err != nil {
				p.log.Error("parsing substituterUrl", zap.String("substituter", substituter), zap.Error(err))
				continue
			}

			group.Submit(func() {
				substituterUrl.Path = filepath.Join(substituterUrl.Path, strings.TrimPrefix(r.URL.Path, "/"+namespace))
				substituterUrlString := substituterUrl.String()

				if response, err := p.httpHeadClient.Head(substituterUrlString); err == nil {
					defer func() { _ = response.Body.Close() }()
					if response.StatusCode == http.StatusOK {
						first <- substituterUrlString
					}
				}
			})
		}

		done := make(chan struct{})
		go func() {
			group.Wait()
			close(done)
		}()

		select {
		case found := <-first:
			go func() {
				defer func() {
					if r := recover(); r != nil {
						p.log.Warn("cache pool closed, dropping cache job", zap.String("url", found))
					}
				}()
				p.cachePool.Submit(func() {
					p.doCache(&cacheRequest{namespace: namespace, url: found, location: location, ext: mux.Vars(r)["ext"]})
				})
			}()

			// Narinfos must be proxied so we can rewrite compression
			// fields before the client sees them. Other content types
			// can redirect directly to the upstream URL.
			if prefix == narinfoPrefix {
				p.proxyNarinfo(found, w, r)
			} else {
				http.Redirect(w, r, found, http.StatusFound)
			}
		case <-done:
			serveNotFound(w, r)
		case <-time.After(500 * time.Millisecond):
			serveNotFound(w, r)
		}
	}
}

// proxyNarinfo fetches a narinfo from an upstream URL, rewrites compression
// fields to reflect uncompressed storage, and serves it to the client.
func (p *Proxy) proxyNarinfo(upstreamURL string, w http.ResponseWriter, r *http.Request) {
	resp, err := p.httpGetClient.Get(upstreamURL)
	if err != nil {
		p.log.Error("fetching narinfo from upstream", zap.String("url", upstreamURL), zap.Error(err))
		serveNotFound(w, r)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		serveNotFound(w, r)
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		p.log.Error("reading upstream narinfo", zap.String("url", upstreamURL), zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}

	body, err = p.rewriteNarinfo(body)
	if err != nil {
		p.log.Error("rewriting upstream narinfo", zap.String("url", upstreamURL), zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}

	w.Header().Set(headerContentType, mimeNarinfo)
	w.Header().Set(headerCache, headerCacheMiss)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (p *Proxy) largeHeadAndGet(prefix, mime string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// NARs are stored decompressed. If the client requests a compressed
		// variant (.nar.xz, .nar.zst, etc.), return 404 so it falls back
		// to the plain .nar URL.
		if prefix == narPrefix && mux.Vars(r)["ext"] != "" {
			serveNotFound(w, r)
			return
		}

		start := time.Now()
		location := indexPathFor(prefix, r)
		namespace := mux.Vars(r)["namespace"]
		defer metricRequestDuration(namespace, prefix).AddSince(start)

		if indices, ok := p.s3Indices[namespace]; !ok {
			serveNotFound(w, r)
		} else if index, err := indices.GetIndex(location); err != nil {
			if errors.Is(err, store.ErrIndexNotFound) {
				metricRequests(namespace, prefix, "miss").Add(1)
				p.redirectToUpstream(prefix, location, w, r)
			} else {
				metricRequests(namespace, prefix, "error").Add(1)
				p.log.Error("getting index", zap.String("index", location), zap.Error(err))
				answer(w, http.StatusInternalServerError, mimeText, err.Error())
			}
		} else {
			metricRequests(namespace, prefix, "hit").Add(1)
			w.Header().Set("Content-Type", mime)

			// Narinfos may have been stored with Compression: xz/zst before
			// we enforced uncompressed-only. Rewrite on the fly so clients
			// get URLs pointing to plain .nar files.
			if prefix == narinfoPrefix {
				rd := desync.NewIndexReadSeeker(index, p.s3Store)
				body, err := io.ReadAll(rd)
				if err != nil {
					p.log.Error("reading narinfo", zap.String("index", location), zap.Error(err))
					answer(w, http.StatusInternalServerError, mimeText, err.Error())
					return
				}
				body, err = p.rewriteNarinfo(body)
				if err != nil {
					p.log.Error("rewriting narinfo", zap.String("index", location), zap.Error(err))
					answer(w, http.StatusInternalServerError, mimeText, err.Error())
					return
				}
				http.ServeContent(w, r, r.URL.Path, time.Now(), bytes.NewReader(body))
			} else {
				rd := desync.NewIndexReadSeeker(index, p.s3Store)
				http.ServeContent(w, r, r.URL.Path, time.Now(), rd)
			}
		}
	}
}

func (p *Proxy) largePut(prefix string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		namespace := mux.Vars(r)["namespace"]
		location := indexPathFor(prefix, r)
		defer metricRequestDuration(namespace, prefix).AddSince(start)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		// NARs must be uploaded uncompressed; content-defined chunking
		// requires raw bytes for deduplication. Clients should use
		// compression=none in their store URI.
		if prefix == narPrefix && mux.Vars(r)["ext"] != "" {
			answer(w, http.StatusBadRequest, mimeText,
				"compressed NARs not accepted; use compression=none in your store URI")
			return
		}

		if err := p.insert(ctx, namespace, location, r.Body); err != nil {
			metricRequests(namespace, prefix, "error").Add(1)
			p.log.Error("inserting", zap.String("index", location), zap.Error(err))
			answer(w, http.StatusInternalServerError, mimeText, err.Error())
		} else {
			metricRequests(namespace, prefix, "uploaded").Add(1)
			p.log.Info("stored", zap.String("location", location))
			w.WriteHeader(http.StatusCreated)
		}
	}
}

func (p *Proxy) insert(ctx context.Context, namespace, location string, body io.Reader) error {
	indices, ok := p.s3Indices[namespace]
	if !ok {
		return fmt.Errorf("namespace '%s' not found", namespace)
	}

	// For narinfos, buffer the body to parse References for reference tracking.
	var narRefs []string
	isNarinfo := strings.HasPrefix(location, "indices/narinfo/")
	if isNarinfo {
		bodyBytes, err := io.ReadAll(body)
		if err != nil {
			return fmt.Errorf("failed reading narinfo body: %w", err)
		}
		bodyBytes, err = p.rewriteNarinfo(bodyBytes)
		if err != nil {
			return fmt.Errorf("failed rewriting narinfo: %w", err)
		}
		narRefs = parseNarinfoReferences(bodyBytes)
		body = bytes.NewReader(bodyBytes)
	}

	chunker, err := desync.NewChunker(body, p.config.Chunks.MinSize, p.config.Chunks.AvgSize, p.config.Chunks.MaxSize)
	if err != nil {
		return fmt.Errorf("failed creating chunker: %w", err)
	}

	if p.pebbleDB != nil {
		batcher := store.NewBatchingChunkStore(p.pebbleDB, p.config.Chunks.BatchMaxBytes)
		index, err := desync.ChunkStream(ctx, chunker, batcher, p.config.Chunks.Threads)
		if err != nil {
			return fmt.Errorf("failed chunking: %w", err)
		}
		if err := batcher.CommitWithIndex(location, index, narRefs); err != nil {
			return fmt.Errorf("failed committing batch: %w", err)
		}
		p.log.Info("stored", zap.String("location", location), zap.Int("chunks", len(index.Chunks)))
		return nil
	}

	// S3 path: unchanged
	index, err := desync.ChunkStream(ctx, chunker, p.s3Store, p.config.Chunks.Threads)
	if err != nil {
		return fmt.Errorf("failed chunking: %w", err)
	}

	// Use StoreIndexWithRefs for stores that support reference tracking.
	if rtis, ok := indices.(store.RefTrackingIndexStore); ok {
		if err := rtis.StoreIndexWithRefs(location, index, narRefs); err != nil {
			return fmt.Errorf("failed storing index: %w", err)
		}
	} else {
		if err := indices.StoreIndex(location, index); err != nil {
			return fmt.Errorf("failed storing index: %w", err)
		}
	}

	p.log.Info("stored", zap.String("location", location), zap.Int("chunks", len(index.Chunks)))
	return nil
}

// rewriteNarinfo rewrites a narinfo to reflect that the NAR is stored uncompressed.
// If the narinfo already indicates no compression, it is returned unchanged.
// Otherwise, it sets Compression to "none", adjusts FileHash/FileSize to match
// NarHash/NarSize, strips the compression extension from URL, clears old signatures,
// and re-signs with the proxy's signing key if configured.
func (p *Proxy) rewriteNarinfo(body []byte) ([]byte, error) {
	// Quick scan: if Compression is "none" or absent, return unchanged.
	scanner := bufio.NewScanner(bytes.NewReader(body))
	foundCompression := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "Compression:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "Compression:"))
			if value == "none" {
				return body, nil
			}
			foundCompression = true
			break
		}
	}
	if !foundCompression {
		return body, nil
	}

	info, err := narinfo.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parsing narinfo: %w", err)
	}

	info.Compression = "none"
	info.FileHash = info.NarHash
	info.FileSize = info.NarSize

	// Strip compression extension from URL.
	for _, ext := range []string{".xz", ".zst", ".bz2", ".br"} {
		if strings.HasSuffix(info.URL, ext) {
			info.URL = strings.TrimSuffix(info.URL, ext)
			break
		}
	}

	// Clear old signatures and re-sign if a signing key is configured.
	info.Signatures = nil
	if p.signingKey != nil {
		fingerprint := info.Fingerprint()
		sig, err := p.signingKey.Sign(nil, fingerprint)
		if err != nil {
			return nil, fmt.Errorf("signing narinfo: %w", err)
		}
		info.Signatures = []signature.Signature{sig}
	}

	return []byte(info.String()), nil
}

// parseNarinfoReferences extracts store path hashes from a narinfo's References field.
// References is a space-separated list of store paths like "hash1-name1 hash2-name2".
// Returns the hash portion (first 32 chars) of each reference.
func parseNarinfoReferences(body []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "References:") {
			refsStr := strings.TrimPrefix(line, "References:")
			refsStr = strings.TrimSpace(refsStr)
			if refsStr == "" {
				return nil
			}
			parts := strings.Fields(refsStr)
			refs := make([]string, 0, len(parts))
			for _, part := range parts {
				// Store paths look like "hash-name", extract the hash (before first '-')
				if idx := strings.IndexByte(part, '-'); idx > 0 {
					refs = append(refs, part[:idx])
				}
			}
			return refs
		}
	}
	return nil
}
