package main

import (
	"bytes"
	"compress/bzip2"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"crawshaw.io/sqlite"
	"github.com/andybalholm/brotli"
	"github.com/folbricht/desync"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/jamespfennell/xz"
	"github.com/klauspost/compress/zstd"
	mh "github.com/multiformats/go-multihash/core"
	"github.com/nix-community/go-nix/pkg/hash"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"golang.org/x/crypto/blake2b"
)

const (
	mimeNarinfo      = "text/x-nix-narinfo"
	mimeNar          = "application/x-nix-nar"
	mimeText         = "text/plain; charset=utf-8"
	mimeNixCacheInfo = "text/x-nix-cache-info"
	mimeJson         = "application/json"

	matchNarinfo     = "/{url:[0-9a-df-np-sv-z]{32}\\.narinfo}"
	matchNar         = "/{url:nar/[0-9a-df-np-sv-z]{52}\\.nar(?:|\\.zst|\\.xz)}"
	matchRealisation = "/{url:realisations/sha256:[0-9a-f]{64}![^.]+\\.doi}"
	matchLog         = "/{url:log/[0-9a-df-np-sv-z]{32}-[^.]+\\.drv}"
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
		namespace.HandleFunc(matchNarinfo, proxy.commonHead).Methods("HEAD")
		namespace.HandleFunc(matchNarinfo, proxy.commonGet).Methods("GET")
		namespace.HandleFunc(matchNarinfo, proxy.commonPut).Methods("PUT")

		namespace.HandleFunc(matchNar, proxy.commonHead).Methods("HEAD")
		namespace.HandleFunc(matchNar, proxy.commonGet).Methods("GET")
		namespace.HandleFunc(matchNar, proxy.commonPut).Methods("PUT")

		namespace.HandleFunc(matchRealisation, proxy.commonHead).Methods("HEAD")
		namespace.HandleFunc(matchRealisation, proxy.commonGet).Methods("GET")
		namespace.HandleFunc(matchRealisation, proxy.commonPut).Methods("PUT")

		namespace.HandleFunc(matchLog, proxy.commonPut).Methods("PUT")
		namespace.HandleFunc(matchLog, proxy.commonGet).Methods("GET")
	}

	return r
}

func (p *Proxy) commonHead(w http.ResponseWriter, r *http.Request) {
	log := p.log.With(zap.String("method", r.Method), zap.String("url", r.URL.String()))
	h := w.Header()
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]
	log.Debug("head", zap.String("namespace", namespace), zap.String("url", url))

	any := false
	if err := p.withDbReadOnly(func(db *sqlite.Conn) error {
		query := db.Prep(`
			SELECT files.content_type, LENGTH(files.data)
			FROM files
			WHERE files.namespace IS :namespace AND files.url IS :url
			UNION
			SELECT indices.content_type, indices.size
			FROM indices
			WHERE indices.namespace IS :namespace AND indices.url IS :url
			LIMIT 1
		`)
		if err := query.Reset(); err != nil {
			return errors.WithMessage(err, "while resetting the query to select files")
		}

		query.SetText(":namespace", namespace)
		query.SetText(":url", url)

		defer query.Step()

		if hasRow, err := query.Step(); err != nil {
			return errors.WithMessage(err, "while executing the query to select files")
		} else if hasRow {
			h.Set(headerCache, headerCacheHit)
			h.Set(headerContentType, query.ColumnText(0))
			// h.Set(headerContentLength, fmt.Sprintf("%d", query.ColumnInt64(1)))
			h.Set(headerCacheUpstream, headerCacheHit)
			w.WriteHeader(http.StatusOK)
			any = true
			return nil
		}

		return nil
	}); err != nil {
		log.Error("on HEAD", zap.Error(err))
		w.WriteHeader(http.StatusInternalServerError)
	} else if !any {
		p.maybeCacheUpstream(w, r, namespace, url, true)
	}
}

func (p *Proxy) commonGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]
	h := w.Header()

	any := false
	if err := p.withDbReadOnly(func(db *sqlite.Conn) error {
		selectFiles := db.Prep(`
			SELECT data, content_type
			FROM files
			WHERE namespace = :namespace AND url = :url
			LIMIT 1
		`)
		if err := selectFiles.Reset(); err != nil {
			return err
		}
		selectFiles.SetText(":namespace", namespace)
		selectFiles.SetText(":url", url)
		defer selectFiles.Step()

		if hasRow, err := selectFiles.Step(); err != nil {
			return errors.WithMessage(err, "while selecting files")
		} else if hasRow {
			wr, err := compress(w, filepath.Ext(r.URL.EscapedPath()))
			if err != nil {
				return errors.WithMessage(err, "compressing files body")
			}

			h.Set(headerContentType, selectFiles.ColumnText(1))
			h.Set(headerCache, headerCacheHit)
			h.Set(headerCacheStorage, headerCacheFile)
			h.Set("access-control-allow-origin", "*")

			// < last-modified: Sun, 04 Dec 2022 11:16:37 GMT
			// < etag: "f1a9a5b4b91490fe819360b691ad7aeb"
			// < content-type: application/x-nix-nar
			// < server: AmazonS3
			// < via: 1.1 varnish, 1.1 varnish
			// < accept-ranges: bytes
			// < date: Mon, 19 Dec 2022 13:41:21 GMT
			// < age: 99464
			// < x-served-by: cache-iad-kiad7000044-IAD, cache-vie6323-VIE
			// < x-cache: HIT, HIT
			// < x-cache-hits: 255, 1
			// < x-timer: S1671457282.763505,VS0,VE0
			// < access-control-allow-origin: *
			// < content-length: 2749092

			n, err := io.Copy(wr, selectFiles.ColumnReader(0))
			if err != nil {
				return err
			}
			if n != int64(selectFiles.ColumnLen(0)) {
				return errors.Errorf("files data copy incorrect: %d != %d", n, selectFiles.ColumnLen(1))
			}

			if filepath.Ext(r.URL.EscapedPath()) == ".zst" {
				zwr := wr.(*zstd.Encoder)
				_ = zwr.Close()
			}

			any = true
			return nil
		}

		selectChunks := db.Prep(`
			SELECT chunks.id, indices.content_type FROM indices
			LEFT JOIN indices_chunks ON indices_chunks.index_url = indices.url
			LEFT JOIN chunks ON chunks.hash = indices_chunks.chunk_hash
			WHERE indices.url = :url AND indices.namespace = :namespace
			ORDER BY indices_chunks.offset
		`)
		if err := selectChunks.Reset(); err != nil {
			return errors.WithMessage(err, "while resetting selectChunks")
		}
		selectChunks.SetText(":namespace", namespace)
		selectChunks.SetText(":url", url)

		for {
			if hasRow, err := selectChunks.Step(); err != nil {
				return errors.WithMessage(err, "on selectChunks.Step")
			} else if !hasRow {
				break
			} else if blob, err := db.OpenBlob("", "chunks", "data", selectChunks.ColumnInt64(0), false); err != nil {
				return errors.WithMessage(err, "on opening a chunk")
			} else {
				wr, err := compress(w, filepath.Ext(r.URL.EscapedPath()))
				if err != nil {
					return errors.WithMessage(err, "compressing files body")
				}

				h.Set(headerContentType, selectChunks.ColumnText(1))
				h.Set(headerCache, headerCacheHit)
				h.Set(headerCacheStorage, headerCacheIndices)
				if _, err := io.Copy(wr, blob); err != nil {
					return errors.WithMessage(err, "on copying a chunk")
				} else if err := blob.Close(); err != nil {
					return errors.WithMessage(err, "on closing a chunk")
				} else {
					if filepath.Ext(r.URL.EscapedPath()) == ".zst" {
						zwr := wr.(*zstd.Encoder)
						_ = zwr.Close()
					}
					any = true
				}
			}
		}

		return nil
	}); err != nil {
		p.log.Error("on GET", zap.Error(err), zap.String("url", r.URL.String()))
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else if !any {
		p.maybeCacheUpstream(w, r, namespace, url, false)
	}
}

func (p *Proxy) commonPut(w http.ResponseWriter, r *http.Request) {
	log := p.log.With(zap.String("method", r.Method), zap.String("url", r.URL.String()))

	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]

	// in order to deduplicate more efficiently, we have to decompress the NARs before storing them.
	// The compression has to be re-applied on retrieval, because the specified
	// compression in the Narinfo and the NAR have to match
	// (URL/FileHash/FileSize would differ).
	// Because the URL depends on the FileHash, different compression also
	// results in a different storage location, which is something we'd want to
	// avoid.
	rd, err := uncompress(r.Body, filepath.Ext(r.URL.String()))
	if err != nil {
		// this should never happen, we don't use any options that may fail, and
		// don't reuse the reader.
		log.Panic("failed creating uncompressor", zap.Error(err))
	}

	if _, err := p.addToCache(namespace, url, uint64(r.ContentLength), rd); err != nil {
		log.Error("adding to cache", zap.Error(err), zap.String("url", r.URL.String()))
		w.Header().Set(headerContentType, mimeText)
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, err.Error())
	} else {
		w.Header().Set(headerContentType, mimeText)
		io.WriteString(w, "ok\n")
	}
}

// An upstream Narinfo usually references a compressed NAR. Since the transfer
// compression for Spongix is independent from upstream, we must modify the
// Narinfo, and also store the associated NAR without compression.
// This introduces a lot of latency for proxying if we also store the NAR
// before responding with the Narinfo.
// The tricky part here is, that the uncompressed file will have a different
// `FileHash` and `FileSize` from the Narinfo.
// So even if we'd respond immediately with the Narinfo, it would cause Nix to
// fail later, because some values don't match anymore.
// That means we must download the NAR and Narinfo from upstream first. The
// only saving grace is that the signature only covers uncompressed values, so
// we don't have to sign again.
//
// One approach here is to simply respond with the original Narinfo, and also
// respond to any URL the original Narinfo references, by forwarding the
// original compressed NAR.
// This would avoid issues downstream, and once the requested Narinfo is
// updated (when the NAR was eventually cached), it can point to the correct
// location.
// A somewhat hairy issue is that the Narinfo will potentially be cached
// infinitely, and we always have to be able to respond to any compression
// scheme by distinguishing the URL file extension.
//
// In the end, the goal of a cache is to speed things up, and again there is a
// tough choice. Compressing e.g. xz on the fly is very slow. But waiting for
// the upstream to be replicated in Spongix is also slow. One however is O(n)
// while the other is O(1).
// So for this reason I opt for the initial slowness and won't simply stream
// through responses.
//
// requesting an upstream NAR should only happen if we didn't cache the NAR for
// the Narinfo that must have been requested before.
// Having a NAR without associated Narinfo renders the cache invalid, and must
// be avoided.
// There is also generally no way to go from a NAR URL to the Narinfo URL,
// because Narinfo is named after the store path, and the NAR by the NAR Hash.
// So we cannot even attempt to find the upstream Narinfo.
// TODO: make parallel
func (p *Proxy) maybeCacheUpstream(w http.ResponseWriter, r *http.Request, namespace, url string, sendBody bool) {
	h := w.Header()
	log := p.log.With(zap.String("url", url), zap.String("namespace", namespace))

	for _, substituter := range p.config.Namespaces[namespace].Substituters {
		upstream := substituter + strings.TrimPrefix(r.URL.String(), "/"+namespace)

		upstreamResponse, err := http.Get(upstream)
		if err != nil || upstreamResponse.StatusCode != 200 {
			continue
		}

		log.Debug("cache upstream", zap.String("substituter", substituter), zap.String("upstream", upstream))

		switch filepath.Ext(upstream) {
		case ".narinfo":
			info, err := p.addRemoteNarinfoToCache(namespace, url, upstream, upstreamResponse.Body)
			if err != nil {
				panic(err)
			}

			h.Set(headerContentType, mimeNarinfo)
			h.Set(headerCache, headerCacheRemote)
			h.Set(headerCacheUpstream, upstream)
			if !sendBody {
				io.WriteString(w, info.String())
			}
			return
		case ".nar":
			panic("requested NAR from upstream")
			// TODO: don't even get here.
		case ".doi":
			// These are harmless (so far) realisations.
			body, err := io.ReadAll(upstreamResponse.Body)
			if err != nil {
				panic(err)
			}
			p.addToCache(namespace, url, uint64(r.ContentLength), bytes.NewReader(body))

			h.Set(headerContentType, mimeJson)
			h.Set(headerCache, headerCacheRemote)
			h.Set(headerCacheUpstream, upstream)
			if !sendBody {
				_, _ = io.Copy(w, bytes.NewReader(body))
			}
			return
		default:
			panic("unknown upstream file extension: '" + filepath.Ext(upstream) + "'")
		}
	}

	h.Set(headerCache, headerCacheMiss)
	h.Set(headerContentType, mimeText)
	w.WriteHeader(http.StatusNotFound)
}

func (p *Proxy) addRemoteNarinfoToCache(namespace, ourNarinfoUrl, upstreamNarinfoUrl string, narinfoBody io.Reader) (*narinfo.NarInfo, error) {
	info, err := narinfo.Parse(narinfoBody)

	secretKeyRaw, err := os.ReadFile(p.config.Namespaces[namespace].SecretKeyFile)
	if err != nil {
		return nil, err
	}

	secretKey, err := signature.LoadSecretKey(string(secretKeyRaw))
	if err != nil {
		return nil, err
	}

	narUrl := narinfoToNarUrl(info)

	if err != nil {
		return nil, err
		// TODO: needs https://github.com/nix-community/go-nix/pull/99
		// } else if err := info.Check(); err != nil {
		// 	return err
	} else if upstreamNarinfoParsedUrl, err := url.Parse(upstreamNarinfoUrl); err != nil {
		return nil, err
	} else if upstreamNarUrl, err := upstreamNarinfoParsedUrl.Parse(info.URL); err != nil {
		return nil, err
	} else if narResponse, err := http.Get(upstreamNarUrl.String()); err != nil {
		return nil, err
	} else {
		var rd io.Reader
		switch info.Compression {
		case "none":
			rd = narResponse.Body
		case "br":
			rd = brotli.NewReader(narResponse.Body)
		case "bzip2":
			rd = bzip2.NewReader(narResponse.Body)
		case "xz":
			rd = xz.NewReader(narResponse.Body)
		case "zstd":
			rd, err = zstd.NewReader(narResponse.Body)
			if err != nil {
				return nil, errors.WithMessage(err, "creating zstd reader")
			}
		default:
			return nil, fmt.Errorf("compression %v is not supported", info.Compression)
		}

		p.log.Debug("cache upstream NAR",
			zap.String("url", info.URL),
			zap.String("fixed_url", narinfoToNarUrl(info)),
			zap.Uint64("nar_size", info.NarSize))
		if narUrl, err = p.addToCache(namespace, narUrl, info.NarSize, rd); err != nil {
			p.log.Error("failed to cache upstream NAR", zap.String("url", narinfoToNarUrl(info)), zap.Error(err))
			return nil, err
		}
	}

	narinfoRemoveCompression(info)

	signature, err := secretKey.Sign(nil, info.Fingerprint())
	if err != nil {
		panic(err)
	}
	info.Signatures = append(info.Signatures, signature)
	pp(narUrl)
	info.URL = narUrl

	infoStr := info.String()
	if _, err := p.addToCache(namespace, ourNarinfoUrl, uint64(len(infoStr)), strings.NewReader(infoStr)); err != nil {
		p.log.Error("failed to cache upstream Narinfo", zap.Error(err))
		return nil, err
	}

	return info, nil
}

func (p *Proxy) addToCache(namespace, url string, length uint64, body io.Reader) (string, error) {
	switch filepath.Ext(url) {
	case ".narinfo":
		info, err := narinfo.Parse(body)
		if err != nil {
			return "", err
		}
		body = strings.NewReader(info.String())
	}

	dest := blake2b.Sum256([]byte(url))
	path := filepath.Join(os.TempDir(), fmt.Sprintf("spongix/inbox/%x", dest))

	if err := os.MkdirAll(filepath.Dir(path), 0744); err != nil {
		return "", err
	}

	fd, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer fd.Close()

	if _, err = io.Copy(fd, body); err != nil {
		return "", err
	} else if err = fd.Sync(); err != nil {
		return "", err
	} else if _, err = fd.Seek(0, 0); err != nil {
		return "", err
	}

	narHash, err := hash.New(mh.SHA2_256)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(narHash, fd); err != nil {
		return "", err
	}

	cmd := exec.Command("nix", "hash", "file", "--base32", path)
	uncompressedHashRaw, err := cmd.Output()
	if err != nil {
		return "", err
	}
	uncompressedHash := strings.TrimSpace(string(uncompressedHashRaw))

	pp("url:", url, "path:", path, fmt.Sprintf("len: %d, min: %d", length, chunkSizeMin()))
	pp("our:", nixbase32.EncodeToString(narHash.Digest()))
	pp("nix:", uncompressedHash)

	if _, err = fd.Seek(0, 0); err != nil {
		return "", err
	}

	if length < chunkSizeMin() {
		return url, p.addToCacheUnchunked(namespace, url, uncompressedHash, fd)
	}

	return p.addToCacheChunked(namespace, url, uncompressedHash, fd)
}

func (p *Proxy) addToCacheUnchunked(namespace, url, uncompressedHash string, body io.Reader) error {
	log := p.log.With(zap.String("namespace", namespace), zap.String("url", url))
	log.Debug("add unchunked")

	return p.withDbReadWrite(func(db *sqlite.Conn) error {
		now := time.Now().UnixNano()

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
		insertFiles.SetText(":content_type", urlToMime(url))
		insertFiles.SetInt64(":ctime", now)
		insertFiles.SetInt64(":atime", now)

		if data, err := io.ReadAll(body); err != nil {
			log.Error("reading body", zap.Error(err))
			return err
		} else {
			insertFiles.SetBytes(":data", data)
		}

		_, err := insertFiles.Step()
		if err != nil {
			log.Error("inserting files", zap.Error(err))
		}
		return err
	})
}

func (p *Proxy) addToCacheChunked(namespace, url, uncompressedHash string, body io.Reader) (string, error) {
	log := p.log.With(zap.String("namespace", namespace), zap.String("url", url))
	log.Debug("add chunked")

	err := p.withDbReadWrite(func(db *sqlite.Conn) error {
		now := time.Now().UnixNano()
		log.Debug("now", zap.Int64("now", now))

		chunker, err := desync.NewChunker(body, chunkSizeMin(), chunkSizeAvg, chunkSizeMax())
		if err != nil {
			return err
		}
		log.Debug("chunker ok")

		var length int64
		seen := map[string]bool{}
		chunks := []*chunkEntry{}

		log.Debug("check ext")
		isNar := false
		if strings.HasSuffix(url, ".nar") {
			isNar = true
		}

		log.Debug("make nix hash")
		narHash, err := hash.New(mh.SHA2_256)
		if err != nil {
			panic(err)
		}

		for {
			offset, chunk, err := chunker.Next()
			if err != nil {
				return err
			}

			chunkLength := len(chunk)
			if chunkLength > 0 {
				length += int64(chunkLength)
			} else {
				break
			}

			chunkHash := desync.Digest.Sum(chunk)
			chunkHashHex := fmt.Sprintf("%x", chunkHash)

			if isNar {
				_, _ = narHash.Write(chunk)
			}

			if _, found := seen[chunkHashHex]; !found {
				seen[chunkHashHex] = true
			} else {
				continue
			}

			insertChunks := db.Prep(`
				INSERT INTO chunks
				( hash,  data,  ctime,  atime) VALUES
				(:hash, :data, :ctime, :atime)
				ON CONFLICT(hash) DO UPDATE SET atime = :atime
			`)

			if err := insertChunks.Reset(); err != nil {
				return errors.WithMessage(err, "resetting insertChunks")
			}

			insertChunks.SetText(":hash", chunkHashHex)
			insertChunks.SetBytes(":data", chunk)
			insertChunks.SetInt64(":ctime", now)
			insertChunks.SetInt64(":atime", now)

			if _, err := insertChunks.Step(); err != nil {
				return errors.WithMessage(err, "stepping insertChunks")
			}

			chunks = append(chunks, &chunkEntry{hash: chunkHashHex, offset: offset})
		}

		txBegin := db.Prep(`BEGIN`)
		txBegin.Reset()
		txBegin.Step()
		txCommit := db.Prep(`COMMIT`)
		txCommit.Reset()
		defer txCommit.Step()

		insertIndicesChunks := db.Prep(`
			INSERT OR IGNORE INTO indices_chunks
			( index_url,  chunk_hash,  offset) VALUES
			(:index_url, :chunk_hash, :offset)
		`)

		narName := nixbase32.EncodeToString(narHash.Digest())
		pp(url, narName)
		if isNar {
			url = "nar/" + narName + ".nar.zst"
		}

		for _, chunkEntry := range chunks {
			if err := insertIndicesChunks.Reset(); err != nil {
				return errors.WithMessage(err, "resetting insertIndicesChunks after step")
			} else {
				insertIndicesChunks.SetText(":index_url", url)
				insertIndicesChunks.SetText(":chunk_hash", chunkEntry.hash)
				insertIndicesChunks.SetInt64(":offset", int64(chunkEntry.offset))

				if _, err := insertIndicesChunks.Step(); err != nil {
					return errors.WithMessage(err, "resetting insertIndicesChunks after step")
				}
			}
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
		insertIndices.SetText(":content_type", urlToMime(url))
		insertIndices.SetInt64(":size", length)
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

		return nil
	})

	return url, err
}

type chunkEntry struct {
	hash   string
	offset uint64
}

func (p *Proxy) addRemoteToCache(namespace, urlPath, contentType string) {
	pp("addRemoteToCache", "namespace:", namespace, "urlPath:", urlPath, "contentType:", contentType)

	switch {
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

func narinfoToNarUrl(info *narinfo.NarInfo) string {
	idx := strings.Index(info.NarHash.NixString(), ":")
	return "nar/" + info.NarHash.NixString()[(idx+1):] + ".nar"
}

func narinfoRemoveCompression(info *narinfo.NarInfo) {
	if info.Compression == "none" {
		return
	}

	info.FileHash = nil
	info.FileSize = 0
	info.Compression = "zstd"
	info.URL = narinfoToNarUrl(info)
}

// Create a reader that uncompresses the given input. The output is always
// deterministic.
func uncompress(input io.Reader, ext string) (io.Reader, error) {
	switch ext {
	case ".nar", ".narinfo", ".doi", ".drv":
		return input, nil
	case ".xz":
		return xz.NewReader(input), nil
	case ".zst":
		return zstd.NewReader(input)
	default:
		return nil, errors.Errorf("Unknown extension for decompression: %v", ext)
	}
}

// Create a writer that compresses the given input. The output is not
// deterministic, depending on the compression used. So this cannot be used to
// generate NARs on the fly as the FileHash/FileSize/URL of the Narinfo may not
// match the actual output.
func compress(output io.Writer, ext string) (io.Writer, error) {
	switch ext {
	case ".nar", ".narinfo", ".doi", ".drv":
		return output, nil
	case ".xz":
		return xz.NewWriter(output), nil
	case ".zst":
		return zstd.NewWriter(output)
	default:
		return nil, errors.Errorf("Unknown extension for compression: %v", ext)
	}
}
