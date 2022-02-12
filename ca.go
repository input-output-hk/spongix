package main

import (
	"context"
	"database/sql"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/folbricht/desync"
	"github.com/gorilla/mux"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

// LogRecord warps a http.ResponseWriter and records the status
type LogRecord struct {
	http.ResponseWriter
	status int
}

func (r *LogRecord) Write(p []byte) (int, error) {
	return r.ResponseWriter.Write(p)
}

// WriteHeader overrides ResponseWriter.WriteHeader to keep track of the response code
func (r *LogRecord) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// WithHTTPLogging adds HTTP request logging to the Handler h
func WithHTTPLogging(log *zap.Logger) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			record := &LogRecord{
				ResponseWriter: w,
				status:         200,
			}
			h.ServeHTTP(record, r)

			level := log.Info
			if record.status >= 500 {
				level = log.Error
			}

			level("Request",
				zap.Int("status_code", record.status),
				zap.String("ident", r.Host),
				zap.String("url", r.URL.String()),
				zap.String("method", r.Method),
				zap.Duration("duration", time.Since(start)),
			)
		})
	}
}

func (proxy *Proxy) touchNarinfo(name string) {
	_, err := proxy.db.Exec(`UPDATE narinfos SET accessed_at = ? WHERE name = ?`, time.Now(), name)
	if err != nil {
		proxy.log.Error("failed to touch narinfo in db", zap.String("name", name), zap.Error(err))
	}
}

func (proxy *Proxy) touchNar(name string) {
	_, err := proxy.db.Exec(`UPDATE narinfos SET accessed_at = ? WHERE nar_hash = ?`, time.Now(), name)
	if err != nil {
		proxy.log.Error("failed to touch nar in db", zap.String("name", name), zap.Error(err))
	}
}

func (proxy *Proxy) routerV2() *mux.Router {
	r := mux.NewRouter()
	r.NotFoundHandler = notFound{}
	r.Use(WithHTTPLogging(proxy.log))

	r.HandleFunc("/nix-cache-info", proxy.nixCacheInfo).Methods("GET")

	narinfo := "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo"
	r.HandleFunc(narinfo, proxy.narinfoHeadV2).Methods("HEAD")
	r.HandleFunc(narinfo, proxy.narinfoGetV2).Methods("GET")
	r.HandleFunc(narinfo, proxy.narinfoPutV2).Methods("PUT")

	nar := "/nar/{hash:[0-9a-df-np-sv-z]{52}}.{ext:nar}"
	r.HandleFunc(nar, proxy.narHeadV2).Methods("HEAD")
	r.HandleFunc(nar, proxy.narGetV2).Methods("GET")
	r.HandleFunc(nar, proxy.narPutV2).Methods("PUT")

	return r
}

func (proxy *Proxy) narHeadV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNar(hash)

	_, err := proxy.narIndex.GetIndex(hash)
	if err != nil {
		if err = proxy.deleteNarinfos(map[string]struct{}{hash: {}}); err != nil {
			proxy.log.Error("removing missing db entry", zap.Error(err))
		}

		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	w.Header().Add("Content-Type", "application/x-nix-nar")
	w.WriteHeader(200)
}

func (proxy *Proxy) narGetV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNar(hash)
	tmpFile, err := ioutil.TempFile(filepath.Join(proxy.Dir, "sync/tmp"), hash)
	if internalServerError(w, err) {
		return
	}
	defer func() {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
	}()

	index, err := proxy.narIndex.GetIndex(hash)
	if err != nil {
		if err = proxy.deleteNarinfos(map[string]struct{}{hash: {}}); err != nil {
			proxy.log.Error("removing missing db entry", zap.Error(err))
		}

		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	_, err = desync.AssembleFile(
		context.Background(),
		tmpFile.Name(),
		index,
		proxy.narStore,
		nil,
		threads,
		nil)

	if internalServerError(w, err) {
		proxy.log.Error("Failed to assemble file", zap.Error(err))
		return
	}

	w.Header().Add("Content-Type", "application/x-nix-nar")
	http.ServeFile(w, r, tmpFile.Name())
}

func (proxy *Proxy) narPutV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNar(hash)
	// ext := vars["ext"]
	if r.ContentLength < 1 {
		badRequest(w, errors.New("No Content-Length set"))
		return
	}

	chunker, err := desync.NewChunker(r.Body, 16*1024, 64*1024, 256*1024)
	if internalServerError(w, errors.WithMessagef(err, "NewChunker %q", hash)) {
		proxy.log.Error("failed creating chunker", zap.String("hash", hash), zap.Error(err))
		return
	}

	index, err := desync.ChunkStream(context.Background(), chunker, proxy.narStore, 8)
	if internalServerError(w, errors.WithMessagef(err, "ChunkStream %q", hash)) {
		proxy.log.Error("failed chunking stream", zap.String("hash", hash), zap.Error(err))
		return
	}

	err = proxy.narIndex.StoreIndex(hash, index)
	if internalServerError(w, errors.WithMessagef(err, "StoreIndex %q", hash)) {
		proxy.log.Error("failed storing index", zap.String("hash", hash), zap.Error(err))
		return
	}

	w.WriteHeader(200)
}

func (proxy *Proxy) narinfoHeadV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNarinfo(hash)
	res := proxy.db.QueryRow(`SELECT nar_hash FROM narinfos WHERE name = ?`, hash)
	var narHash string
	err := res.Scan(&narHash)
	if err == sql.ErrNoRows {
		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	_, err = proxy.narIndex.GetIndex(narHash)
	if err != nil {
		if err = proxy.deleteNarinfos(map[string]struct{}{narHash: {}}); err != nil {
			proxy.log.Error("removing missing db entry", zap.Error(err))
		}

		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	w.Header().Add("Content-Type", "text/x-nix-narinfo")
	w.WriteHeader(200)
}

func (proxy *Proxy) narinfoGetV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]
	proxy.touchNarinfo(hash)
	res := proxy.db.QueryRow(`
	SELECT
		name,
		store_path,
		url,
		compression,
		file_hash_type,
		file_hash,
		file_size,
		nar_hash_type,
		nar_hash,
		nar_size,
		deriver,
		ca
	FROM narinfos
	WHERE name = ?`, hash)

	var name, store_path, url, compression, file_hash_type, file_hash, nar_hash_type, nar_hash, deriver, ca string
	var file_size, nar_size int64
	err := res.Scan(&name, &store_path, &url, &compression, &file_hash_type, &file_hash, &file_size, &nar_hash_type, &nar_hash, &nar_size, &deriver, &ca)
	if err == sql.ErrNoRows {
		w.Header().Add("Content-Type", "text/html")
		w.WriteHeader(404)
		return
	}
	if internalServerError(w, err) {
		proxy.log.Error("Failed to get narinfo", zap.Error(err))
		return
	}

	_, err = proxy.narIndex.GetIndex(nar_hash)
	if err != nil {
		if err = proxy.deleteNarinfos(map[string]struct{}{hash: {}}); err != nil {
			proxy.log.Error("removing missing db entry", zap.Error(err))
		}

		w.Header().Add("Content-Type", "text/plain")
		w.WriteHeader(404)
		return
	}

	info := NarInfo{
		Name:        name,
		StorePath:   store_path,
		URL:         url,
		Compression: compression,
		FileHash:    file_hash_type + ":" + file_hash,
		FileSize:    file_size,
		NarHash:     nar_hash_type + ":" + nar_hash,
		NarSize:     nar_size,
		Deriver:     deriver,
		CA:          ca,
	}

	rows, err := proxy.db.Query(`SELECT signature FROM signatures WHERE name = ?`, hash)
	if internalServerError(w, err) {
		proxy.log.Error("Failed getting narinfo signature", zap.Error(err))
		return
	}
	for rows.Next() {
		var signature string
		if internalServerError(w, rows.Scan(&signature)) {
			proxy.log.Error("Failed scanning narinfo signature", zap.Error(err))
			return
		}
		info.Sig = append(info.Sig, signature)
	}
	rows.Close()

	rows, err = proxy.db.Query(`SELECT child FROM refs WHERE parent = ?`, hash)
	if internalServerError(w, err) {
		proxy.log.Error("Failed getting narinfo refs", zap.Error(err))
		return
	}
	for rows.Next() {
		var ref string
		if internalServerError(w, rows.Scan(&ref)) {
			proxy.log.Error("Failed scanning narinfo refs", zap.Error(err))
			return
		}
		info.References = append(info.References, ref)
	}
	rows.Close()

	w.Header().Add("Content-Type", "text/x-nix-narinfo")
	w.WriteHeader(200)
	err = info.Marshal(w)
	if internalServerError(w, err) {
		proxy.log.Error("Failed sending narinfo", zap.Error(err))
		return
	}
}

func (proxy *Proxy) narinfoPutV2(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	hash := vars["hash"]

	info := NarInfo{}
	err := info.Unmarshal(r.Body)
	if badRequest(w, errors.WithMessagef(err, "Parsing narinfo %q", hash)) {
		proxy.log.Error("Failed parsing narinfo", zap.String("hash", hash), zap.Error(err))
		return
	}

	if len(info.Sig) == 0 {
		for name, key := range proxy.secretKeys {
			if internalServerError(w, info.Sign(name, key)) {
				return
			}
		}
	} else if err = info.Verify(proxy.trustedKeys); err != nil {
		badRequest(w, errors.WithMessagef(err, "%s signatures are untrusted", info.StorePath))
		return
	}

	_, err = proxy.db.Exec(`
    INSERT INTO narinfos (
      name,
    	store_path,
    	url,
    	compression,
    	file_hash_type,
    	file_hash,
    	file_size,
    	nar_hash_type,
    	nar_hash,
    	nar_size,
    	deriver,
    	ca,
			created_at,
			accessed_at
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
    `,
		info.Name,
		info.StorePath,
		info.URL,
		info.Compression,
		info.FileHashType(),
		info.FileHashValue(),
		info.FileSize,
		info.NarHashType(),
		info.NarHashValue(),
		info.NarSize,
		info.Deriver,
		info.CA,
		time.Now(),
		time.Now())
	if internalServerError(w, errors.WithMessagef(err, "Inserting narinfo %q", hash)) {
		return
	}

	for _, reference := range info.References {
		_, err = proxy.db.Exec(`
      INSERT INTO refs (parent, child) VALUES (?, ?)
      `, info.Name, reference)
		if internalServerError(w, err) {
			return
		}
	}

	for _, signature := range info.Sig {
		_, err = proxy.db.Exec(`
      INSERT INTO signatures (name, signature) VALUES (?, ?)
      `, info.Name, signature)
		if internalServerError(w, err) {
			return
		}
	}

	w.WriteHeader(200)
}
