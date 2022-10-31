package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"
	"time"

	"crawshaw.io/sqlite"
	"github.com/folbricht/desync"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/pascaldekloe/metrics"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func nixCacheInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set(headerContentType, mimeNixCacheInfo)
	w.WriteHeader(200)
	_, _ = w.Write([]byte(
		`StoreDir: /nix/store
WantMassQuery: 1
Priority: 10`))
}

type Router struct {
	log *zap.Logger
	r   *mux.Router
	c   chan writeFunc
}

func NewRouter(log *zap.Logger) Router {
	r := mux.NewRouter()

	router := Router{log: log, r: r}
	ch := startDatabaseWriter(log)
	router.c = ch

	r.NotFoundHandler = notFound{}
	r.MethodNotAllowedHandler = notAllowed{}
	r.Use(
		withHTTPLogging(log.Named("http")),
		handlers.RecoveryHandler(handlers.PrintRecoveryStack(true)),
	)

	// nar := "/{url:nar/[0-9a-df-np-sv-z]{52}\\.nar}"
	// narinfo := "/{hash:[0-9a-df-np-sv-z]{32}}.narinfo"

	r.HandleFunc("/metrics", metrics.ServeHTTP)
	namespace := r.Name("namespace").PathPrefix("/{namespace:[a-z]{1,32}}/{url:.+}").Subrouter()
	namespace.Methods("HEAD").HandlerFunc(router.commonHead)
	namespace.Methods("GET").HandlerFunc(router.commonGet)
	namespace.Methods("PUT").HandlerFunc(router.commonPut)

	return router
}

func (router Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	router.r.ServeHTTP(w, r)
}

func experiment() {
	log, err := zap.NewDevelopment()
	if err != nil {
		panic(err)
	}
	defer log.Sync()

	migrate()

	f, err := os.Create("narinfoGet.prof")
	if err != nil {
		panic(err)
	}
	pprof.StartCPUProfile(f)

	router := NewRouter(log)

	const timeout = 15 * time.Minute
	const listen = ":7777"
	srv := &http.Server{
		Handler:      router,
		Addr:         listen,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	}

	sc := make(chan os.Signal, 1)
	signal.Notify(
		sc,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
	)

	go func() {
		log.Info("Server starting", zap.String("listen", listen))
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			// Only log an error if it's not due to shutdown or close
			log.Fatal("error bringing up listener", zap.Error(err))
		}
	}()

	<-sc
	signal.Stop(sc)

	// Shutdown timeout should be max request timeout (with 1s buffer).
	ctxShutDown, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := srv.Shutdown(ctxShutDown); err != nil {
		log.Fatal("server shutdown failed", zap.Error(err))
	}

	pprof.StopCPUProfile()
	log.Info("server shutdown gracefully")
}

func migrate() {
	db, err := sqlite.OpenConn(
		dbFile,
		sqlite.SQLITE_OPEN_READWRITE|
			sqlite.SQLITE_OPEN_CREATE|
			sqlite.SQLITE_OPEN_WAL|
			sqlite.SQLITE_OPEN_URI|
			sqlite.SQLITE_OPEN_NOMUTEX,
	)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	for _, sql := range []string{
		`PRAGMA cell_size_check = ON`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA recursive_triggers = ON`,
		`PRAGMA reverse_unordered_selects = ON`,
		`PRAGMA analysis_limit=400`,

		`CREATE TABLE IF NOT EXISTS indices
			( url TEXT PRIMARY KEY NOT NULL
			, namespace TEXT NOT NULL
			, content_type TEXT NOT NULL
			, size INTEGER NOT NULL
			, ctime INTEGER NOT NULL
			, atime INTEGER NOT NULL
			) STRICT`,
		`CREATE INDEX IF NOT EXISTS indices_url ON indices(url)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS indices_combined ON indices(url, namespace)`,

		`CREATE TABLE IF NOT EXISTS chunks
			( id INTEGER PRIMARY KEY NOT NULL
			, hash TEXT NOT NULL
			, data BLOB NOT NULL
			, ctime INTEGER NOT NULL
			, atime INTEGER NOT NULL
			) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS chunks_hash ON chunks(hash)`,

		`CREATE TABLE IF NOT EXISTS indices_chunks
			( id INTEGER PRIMARY KEY NOT NULL
			, index_url TEXT NOT NULL
			, chunk_hash TEXT NOT NULL
			, offset INTEGER NOT NULL
			, FOREIGN KEY(index_url) REFERENCES indices(url)
			, FOREIGN KEY(chunk_hash) REFERENCES chunks(hash)
			) STRICT`,
		`CREATE INDEX IF NOT EXISTS indices_chunks_index_url ON indices_chunks(index_url)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS indices_chunks_combined ON indices_chunks(index_url, chunk_hash, offset)`,

		`CREATE TABLE IF NOT EXISTS files
			( id INTEGER PRIMARY KEY NOT NULL
			, url TEXT NOT NULL
			, namespace TEXT NOT NULL
			, data BLOB NOT NULL
			, ctime INTEGER NOT NULL
			, atime INTEGER NOT NULL
			) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS files_url ON files(url)`,
		`CREATE INDEX IF NOT EXISTS files_combined ON files(url, namespace)`,

		`CREATE TABLE IF NOT EXISTS narinfos
			( id INTEGER PRIMARY KEY NOT NULL
			, name TEXT NOT NULL
			, store_path TEXT NOT NULL
			, url TEXT NOT NULL
			, compression TEXT NOT NULL
			, file_hash TEXT NOT NULL
			, file_size INTEGER NOT NULL
			, nar_hash TEXT NOT NULL
			, nar_size INTEGER NOT NULL
			, deriver TEXT NOT NULL
			, ca TEXT
			, data BLOB NOT NULL
			, namespace TEXT NOT NULL
			, ctime INTEGER NOT NULL
			, atime INTEGER NOT NULL
			) STRICT`,
		`CREATE INDEX IF NOT EXISTS narinfos_name ON narinfos(name)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS narinfos_combined ON narinfos(name, namespace)`,

		`CREATE TABLE IF NOT EXISTS narinfo_refs
			( narinfo_id INTEGER NOT NULL
			, ref TEXT NOT NULL
			, FOREIGN KEY(narinfo_id) REFERENCES narinfos(id)
			) STRICT`,
		`CREATE INDEX IF NOT EXISTS narinfos_refs_narinfo_id ON narinfo_refs(narinfo_id)`,

		`CREATE TABLE IF NOT EXISTS narinfo_sigs
			( narinfo_id TEXT NOT NULL
			, sig TEXT NOT NULL
			, FOREIGN KEY(narinfo_id) REFERENCES narinfos(id)
			) STRICT`,
		`CREATE INDEX IF NOT EXISTS narinfos_sigs_narinfo_id ON narinfo_sigs(narinfo_id)`,

		`CREATE TABLE IF NOT EXISTS realisations
			( id INTEGER PRIMARY KEY NOT NULL
			, rid TEXT NOT NULL
			, namespace TEXT NOT NULL
			, data BLOB NOT NULL
			) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS realisations_combined ON realisations(rid, namespace)`,
	} {
		if _, err := db.Prep(sql).Step(); err != nil {
			panic(err)
		}
	}
}

type writeFunc func(db *sqlite.Conn) error

func startDatabaseWriter(log *zap.Logger) chan writeFunc {
	ch := make(chan writeFunc, 1000)
	go databaseWriter(log, ch)
	return ch
}

func databaseWriter(log *zap.Logger, ch chan writeFunc) {
	for {
		log.Debug("Starting database writer")

		if err := withDB(log, func(db *sqlite.Conn) error {
			log.Debug("range start")
			for fun := range ch {
				if _, err := db.Prep(`BEGIN IMMEDIATE`).Step(); err != nil {
					return errors.WithMessage(err, "while executing BEGIN IMMEDIATE")
				}

				if funErr := fun(db); funErr != nil {
					if _, err := db.Prep(`ROLLBACK`).Step(); err != nil {
						return errors.WithMessage(err, "while executing ROLLBACK")
					}
					return errors.WithMessage(funErr, "from withDB callback")
				} else if _, err := db.Prep(`COMMIT`).Step(); err != nil {
					return errors.WithMessage(err, "while executing COMMIT")
				}
			}

			return nil
		}); err != nil {
			log.Error("while handling write", zap.Error(err))
		}

		time.Sleep(1 * time.Second)
	}
}

func (router Router) withDBWrite(f writeFunc) error {
	done := make(chan error, 1)
	router.c <- func(db *sqlite.Conn) error {
		var err error
		defer func() { done <- err }()
		err = f(db)
		return err
	}
	return <-done
}

func (router Router) commonHead(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]

	if err := withDBFast(router.log, func(db *sqlite.Conn) error {
		selectFiles := db.Prep(`
			SELECT 1 FROM files WHERE namespace IS :namespace AND url IS :url LIMIT 1
		`)
		if err := selectFiles.Reset(); err != nil {
			return err
		}
		defer selectFiles.Step()
		if hasRow, err := selectFiles.Step(); err != nil {
			return errors.WithMessage(err, "while selecting files")
		} else if hasRow {
			w.WriteHeader(200)
			return nil
		}

		selectIndices := db.Prep(`
			SELECT 1 FROM indices WHERE namespace IS :namespace AND url IS :url LIMIT 1
		`)
		if err := selectIndices.Reset(); err != nil {
			return err
		}
		defer selectIndices.Step()
		selectIndices.SetText(":namespace", namespace)
		selectIndices.SetText(":url", url)
		if hasRow, err := selectIndices.Step(); err != nil {
			return errors.WithMessage(err, "while selecting indices")
		} else if !hasRow {
			w.WriteHeader(404)
		} else {
			w.WriteHeader(200)
		}
		return nil
	}); err != nil {
		router.log.Error("on HEAD", zap.Error(err), zap.String("url", r.URL.String()))
		w.WriteHeader(500)
	}
}

func (router Router) commonGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]

	any := false
	if err := withDBFast(router.log, func(db *sqlite.Conn) error {
		selectFiles := db.Prep(`
			SELECT id FROM files WHERE namespace IS :namespace AND url IS :url LIMIT 1
		`)
		if err := selectFiles.Reset(); err != nil {
			return err
		}
		selectFiles.SetText(":namespace", namespace)
		selectFiles.SetText(":url", url)
		defer selectFiles.Step()

		if hasRow, err := selectFiles.Step(); err != nil {
			return errors.WithMessage(err, "while selecting files")
		} else if !hasRow {
		} else if id := selectFiles.ColumnInt64(0); err != nil {
			return errors.WithMessage(err, "on reading the chunk id")
		} else if blob, err := db.OpenBlob("", "files", "data", id, false); err != nil {
			return errors.WithMessage(err, "on opening a file")
		} else {
			defer blob.Close()
			if _, err := io.Copy(w, blob); err != nil {
				return errors.WithMessage(err, "on copying a file")
			} else {
				any = true
				return nil
			}
		}

		selectChunks := db.Prep(`
			SELECT chunks.id FROM indices
			LEFT JOIN indices_chunks ON indices_chunks.index_url IS indices.url
			LEFT JOIN chunks ON chunks.hash IS indices_chunks.chunk_hash
			WHERE indices.url IS :url AND indices.namespace IS :namespace
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
			} else if id := selectChunks.ColumnInt64(0); err != nil {
				return errors.WithMessage(err, "on reading the chunk id")
			} else if blob, err := db.OpenBlob("", "chunks", "data", id, false); err != nil {
				return errors.WithMessage(err, "on opening a chunk")
			} else if _, err := io.Copy(w, blob); err != nil {
				return errors.WithMessage(err, "on copying a chunk")
			} else if err := blob.Close(); err != nil {
				return errors.WithMessage(err, "on closing a chunk")
			} else {
				any = true
			}
		}

		return nil
	}); err != nil {
		router.log.Error("on GET", zap.Error(err), zap.String("url", r.URL.String()))
		w.WriteHeader(500)
	} else if !any {
		w.WriteHeader(404)
	}
}

func (router Router) commonPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]

	now := time.Now().UnixNano()

	if err := router.withDBWrite(func(db *sqlite.Conn) error {
		if r.ContentLength < int64(chunkSizeMin()) {
			insertFiles := db.Prep(`
				INSERT OR IGNORE INTO files
				( url,  namespace,  data,  ctime,  atime) VALUES
				(:url, :namespace, :data, :ctime, :atime)
			`)
			if err := insertFiles.Reset(); err != nil {
				return err
			}
			defer insertFiles.Step()

			insertFiles.SetText(":url", url)
			insertFiles.SetText(":namespace", namespace)
			insertFiles.SetInt64(":ctime", now)
			insertFiles.SetInt64(":atime", now)

			buf := bytes.Buffer{}
			if _, err := io.Copy(&buf, r.Body); err != nil {
				return err
			} else {
				insertFiles.SetBytes(":data", buf.Bytes())
			}

			if _, err := insertFiles.Step(); err != nil {
				return err
			} else {
				return nil
			}
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
		router.log.Error("on PUT", zap.Error(err), zap.String("url", r.URL.String()))
		w.WriteHeader(500)
	} else {
		w.WriteHeader(200)
	}
}

const dbFile = "/big/cache/test.sqlite"

func withDBFast(log *zap.Logger, f func(*sqlite.Conn) error) error {
	db, err := sqlite.OpenConn(
		dbFile,
		sqlite.SQLITE_OPEN_READONLY|
			sqlite.SQLITE_OPEN_SHAREDCACHE|
			sqlite.SQLITE_OPEN_WAL|
			sqlite.SQLITE_OPEN_URI|
			sqlite.SQLITE_OPEN_NOMUTEX,
	)
	if err != nil {
		return errors.WithMessagef(err, "while opening database: %q", dbFile)
	}
	defer func() {
		go func() {
			for _, sql := range []string{
				`PRAGMA analysis_limit=1000`,
				`PRAGMA optimize`,
			} {
				_, _ = db.Prep(sql).Step()
			}

			if err := db.Close(); err != nil {
				log.Error("while closing database", zap.Error(err))
			}
		}()
	}()

	if ferr := f(db); ferr != nil {
		return errors.WithMessage(ferr, "from withDB callback")
	}

	return nil
}

func withDB(log *zap.Logger, f func(*sqlite.Conn) error) error {
	db, err := sqlite.OpenConn(
		dbFile,
		sqlite.SQLITE_OPEN_READWRITE|
			sqlite.SQLITE_OPEN_PRIVATECACHE|
			sqlite.SQLITE_OPEN_WAL|
			sqlite.SQLITE_OPEN_URI|
			sqlite.SQLITE_OPEN_NOMUTEX,
	)
	if err != nil {
		return errors.WithMessagef(err, "while opening database: %q", dbFile)
	}
	defer func() {
		if _, err := db.Prep(`PRAGMA optimize;`).Step(); err != nil {
			log.Error("while running PRAGMA optimize", zap.Error(err))
		}
		if err := db.Close(); err != nil {
			log.Error("while closing database", zap.Error(err))
		}
	}()

	for _, sql := range []string{
		`PRAGMA cell_size_check = ON;`,
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA recursive_triggers = ON;`,
		`PRAGMA reverse_unordered_selects = ON;`,
		`PRAGMA analysis_limit=1000;`,
	} {
		if _, err := db.Prep(sql).Step(); err != nil {
			return errors.WithMessagef(err, "while running: %q", sql)
		}
	}

	return f(db)
}

func (router Router) realisationsGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	hash := vars["hash"]

	if err := withDB(router.log, func(db *sqlite.Conn) error {
		selectRealisations := db.Prep(`
			SELECT id FROM realisations WHERE rid IS :rid AND namespace IS :namespace
		`)

		if err := selectRealisations.Reset(); err != nil {
			return err
		}
		selectRealisations.SetText(":rid", hash)
		selectRealisations.SetText(":namespace", namespace)

		if hasRow, err := selectRealisations.Step(); err != nil {
			return err
		} else if hasRow {
			id := selectRealisations.GetInt64("id")
			_, _ = selectRealisations.Step()
			blob, err := db.OpenBlob("", "realisations", "data", id, false)
			defer blob.Close()
			if err != nil {
				return errors.WithMessage(err, "while opening realisation data")
			} else if _, err := io.Copy(w, blob); err != nil {
				return errors.WithMessage(err, "while copying realisation data")
			}
		} else {
			w.WriteHeader(404)
		}

		return nil
	}); err != nil {
		router.log.Error("during realisationsPut", zap.Error(err))
		w.WriteHeader(500)
	}
}

func (router Router) realisationsPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	hash := vars["hash"]

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(400)
		fmt.Fprintf(w, "failed to read body: %s", err)
		return
	}
	realisation := map[string]interface{}{}
	if err := json.Unmarshal(body, &realisation); err != nil {
		w.WriteHeader(400)
		fmt.Fprintf(w, "failed to unmarshal body: %s", err)
		return
	}

	var id string
	if idI, ok := realisation["id"]; !ok {
		w.WriteHeader(400)
		w.Write([]byte(`id attribute is missing`))
		return
	} else if id, ok = idI.(string); !ok {
		w.WriteHeader(400)
		w.Write([]byte(`id attribute should be a string`))
		return
	} else if id != hash {
		w.WriteHeader(400)
		w.Write([]byte(`id attribute and url don't match`))
		return
	}

	if err := withDB(router.log, func(db *sqlite.Conn) error {
		insertRealisations := db.Prep(`
			INSERT OR REPLACE INTO realisations
			( rid
			, namespace
			, data
			)
			VALUES
			( :rid
			, :namespace
			, :data
			)
		`)

		if err := insertRealisations.Reset(); err != nil {
			return err
		}
		insertRealisations.SetText(":rid", id)
		insertRealisations.SetText(":namespace", namespace)
		insertRealisations.SetBytes(":data", body)

		if _, err := insertRealisations.Step(); err != nil {
			return err
		}

		return nil
	}); err != nil {
		router.log.Error("during realisationsPut", zap.Error(err))
		w.WriteHeader(500)
	} else {
		w.WriteHeader(200)
	}
}

func (router Router) narinfoHead(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	hash := vars["hash"]

	if err := withDBFast(router.log, func(db *sqlite.Conn) error {
		selectNarinfos := db.Prep(`
			SELECT id FROM narinfos WHERE name IS :name AND namespace IS :namespace LIMIT 1
		`)
		if err := selectNarinfos.Reset(); err != nil {
			return errors.WithMessage(err, "while resetting selectNarinfos")
		}
		selectNarinfos.SetText(":name", hash)
		selectNarinfos.SetText(":namespace", namespace)
		defer selectNarinfos.Step()

		if hasRow, err := selectNarinfos.Step(); err != nil {
			return errors.WithMessage(err, "while selecting narinfos")
		} else if hasRow {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}

		return nil
	}); err != nil {
		router.log.Error("during narinfoHead", zap.Error(err))
		w.WriteHeader(500)
	}

	//	if err := withDB(router.log, func(db *sqlite.Conn) error {
	//		selectNarinfos := db.Prep(`
	//			UPDATE narinfos SET atime = :atime WHERE name IS :name AND namespace IS :namespace
	//		`)
	//		if err := selectNarinfos.Reset(); err != nil {
	//			return err
	//		}
	//		selectNarinfos.SetInt64(":atime", time.Now().UnixMicro())
	//		selectNarinfos.SetText(":name", hash)
	//		selectNarinfos.SetText(":namespace", namespace)
	//
	//		if _, err := selectNarinfos.Step(); err != nil {
	//			return err
	//		} else if db.Changes() > 0 {
	//			w.WriteHeader(200)
	//		} else {
	//			w.WriteHeader(404)
	//		}
	//
	//		return nil
	//	}); err != nil {
	//		router.log.Error("during narinfoHead", zap.Error(err))
	//		w.WriteHeader(500)
	//	}
}

func (router Router) narinfoGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	hash := vars["hash"]

	if err := withDBFast(router.log, func(db *sqlite.Conn) error {
		selectNarinfos := db.Prep(`
			SELECT id FROM narinfos WHERE name IS :name AND namespace IS :namespace LIMIT 1
		`)
		if err := selectNarinfos.Reset(); err != nil {
			return err
		}
		selectNarinfos.SetText(":name", hash)
		selectNarinfos.SetText(":namespace", namespace)

		if hasRow, err := selectNarinfos.Step(); err != nil {
			return err
		} else if !hasRow {
			w.WriteHeader(404)
		} else {
			id := selectNarinfos.GetInt64("id")
			if blob, err := db.OpenBlob("", "narinfos", "data", id, false); err != nil {
				return err
			} else {
				defer blob.Close()
				io.Copy(w, blob)
			}
		}

		return nil
	}); err != nil {
		w.WriteHeader(500)
	}
}

func (router Router) narinfoPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	// hash := vars["hash"]

	narinfo := Narinfo{Namespace: namespace}
	if err := narinfo.Unmarshal(r.Body); err != nil {
		router.log.Error("unmarshaling narinfo", zap.Error(err))
		w.WriteHeader(400)
	} else if err := withDB(router.log, func(db *sqlite.Conn) error {
		insertNarinfos := db.Prep(`
				INSERT INTO narinfos
				( name
				, store_path
				, url
				, compression
				, file_hash
				, file_size
				, nar_hash
				, nar_size
				, deriver
				, ca
				, data
				, namespace
				, ctime
				, atime
				)
				VALUES
				( :name
				, :store_path
				, :url
				, :compression
				, :file_hash
				, :file_size
				, :nar_hash
				, :nar_size
				, :deriver
				, :ca
			  , :data
				, :namespace
				, :ctime
				, :atime
				)
			`)
		if err := insertNarinfos.Reset(); err != nil {
			return err
		}
		buf := bytes.Buffer{}
		if err := narinfo.Marshal(&buf); err != nil {
			return err
		}

		insertNarinfos.SetText(":name", narinfo.Name)
		insertNarinfos.SetText(":store_path", narinfo.StorePath)
		insertNarinfos.SetText(":url", narinfo.URL)
		insertNarinfos.SetText(":compression", narinfo.Compression)
		insertNarinfos.SetText(":file_hash", narinfo.FileHash)
		insertNarinfos.SetInt64(":file_size", narinfo.FileSize)
		insertNarinfos.SetText(":nar_hash", narinfo.NarHash)
		insertNarinfos.SetInt64(":nar_size", narinfo.NarSize)
		insertNarinfos.SetText(":deriver", narinfo.Deriver)
		insertNarinfos.SetText(":ca", narinfo.CA)
		insertNarinfos.SetBytes(":data", buf.Bytes())
		insertNarinfos.SetText(":namespace", narinfo.Namespace)
		now := time.Now().UnixNano()
		insertNarinfos.SetInt64(":ctime", now)
		insertNarinfos.SetInt64(":atime", now)
		if _, err := insertNarinfos.Step(); err != nil {
			return err
		}
		id := db.LastInsertRowID()

		insertNarinfoRefs := db.Prep(`INSERT INTO narinfo_refs (narinfo_id, ref) VALUES (:narinfo_id, :ref)`)
		for _, ref := range narinfo.References {
			if err := insertNarinfoRefs.Reset(); err != nil {
				return err
			}
			insertNarinfoRefs.SetInt64(":narinfo_id", id)
			insertNarinfoRefs.SetText(":ref", string(ref))
			if _, err := insertNarinfoRefs.Step(); err != nil {
				return err
			}
		}

		insertNarinfoSigs := db.Prep(`INSERT INTO narinfo_sigs (narinfo_id, sig) VALUES (:narinfo_id, :sig)`)
		for _, sig := range narinfo.Sig {
			if err := insertNarinfoSigs.Reset(); err != nil {
				return err
			}
			insertNarinfoSigs.SetInt64(":narinfo_id", id)
			insertNarinfoSigs.SetText(":sig", string(sig))
			if _, err := insertNarinfoSigs.Step(); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		router.log.Error("executing narinfoPut", zap.Error(err))
		w.WriteHeader(500)
	} else {
		w.WriteHeader(200)
	}
}

func (router Router) narGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]

	any := false
	if err := withDBFast(router.log, func(db *sqlite.Conn) error {
		selectChunks := db.Prep(`
			SELECT chunks.id, indices.content_type FROM indices
			LEFT JOIN indices_chunks ON indices_chunks.index_url IS indices.url
			LEFT JOIN chunks ON chunks.hash IS indices_chunks.chunk_hash
			WHERE indices.url IS :url AND indices.namespace IS :namespace
			ORDER BY indices_chunks.offset
		`)
		if err := selectChunks.Reset(); err != nil {
			return errors.WithMessage(err, "while resetting selectChunks")
		}
		selectChunks.SetText(":url", url)
		selectChunks.SetText(":namespace", namespace)

		for {
			if hasRow, err := selectChunks.Step(); err != nil {
				return errors.WithMessage(err, "on selectChunks.Step")
			} else if hasRow {
				id := selectChunks.ColumnInt64(0)
				contentType := selectChunks.ColumnText(1)
				blob, err := db.OpenBlob("", "chunks", "data", id, false)
				if err != nil {
					return errors.WithMessage(err, "on opening a chunk")
				}
				defer blob.Close()
				w.Header().Set("Content-Type", contentType)

				if _, err := io.Copy(w, blob); err != nil {
					return errors.WithMessage(err, "on copying a chunk")
				} else if err := blob.Close(); err != nil {
					return errors.WithMessage(err, "on closing a chunk")
				} else {
					any = true
				}
			} else {
				break
			}
		}

		return nil
	}); err != nil {
		router.log.Error("during narGet", zap.Error(err))
		w.WriteHeader(500)
	} else if !any {
		w.WriteHeader(404)
	}
}

func (router Router) narHead(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]

	if err := withDB(router.log, func(db *sqlite.Conn) error {
		selectIndices := db.Prep(`
			UPDATE indices SET atime = :atime WHERE url IS :url AND namespace IS :namespace
		`)
		if err := selectIndices.Reset(); err != nil {
			return err
		}
		selectIndices.SetInt64(":atime", time.Now().UnixMicro())
		selectIndices.SetText(":url", url)
		selectIndices.SetText(":namespace", namespace)
		if _, err := selectIndices.Step(); err != nil {
			return err
		} else if db.Changes() > 0 {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(404)
		}
		return nil
	}); err != nil {
		router.log.Error("executing narHead", zap.Error(err))
		w.WriteHeader(500)
	}
}

func (router Router) narPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	url := vars["url"]

	now := time.Now().UnixNano()

	if err := withDB(router.log, func(db *sqlite.Conn) error {
		chunker, err := desync.NewChunker(r.Body, chunkSizeMin(), chunkSizeAvg, chunkSizeMax())
		if err != nil {
			return err
		}

		insertIndices := db.Prep(`
			INSERT INTO indices
			( url
			, namespace
			, ctime
			, atime
			)
			VALUES
			( :url
			, :namespace
			, :ctime
			, :atime
			)
			ON CONFLICT(url, namespace)
			DO UPDATE SET atime = :atime
			RETURNING ctime
		`)
		if err := insertIndices.Reset(); err != nil {
			return err
		}
		insertIndices.SetText(":url", url)
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
				pp("not storing chunk")
				return nil
			} else {
				insertIndices.Step()
			}
		}

		insertChunks := db.Prep(`
			INSERT INTO chunks
			( hash
			, data
			, ctime
			, atime
			)
			VALUES
			( :hash
			, :data
			, :ctime
			, :atime
			)
			ON CONFLICT(hash) DO UPDATE SET atime = :atime
		`)

		insertIndicesChunks := db.Prep(`
			INSERT OR IGNORE INTO indices_chunks
			( index_url
			, chunk_hash
			, offset
			)
			VALUES
			( :index_url
			, :chunk_hash
			, :offset
			)
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
		router.log.Error("executing narPut", zap.Error(err))
		w.WriteHeader(500)
	} else {
		w.WriteHeader(200)
	}
}
