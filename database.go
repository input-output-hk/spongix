package main

import (
	"crawshaw.io/sqlite"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func (p *Proxy) migrate() {
	db, err := sqlite.OpenConn(
		p.dbFile(),
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
			, content_type TEXT NOT NULL
			, data BLOB NOT NULL
			, ctime INTEGER NOT NULL
			, atime INTEGER NOT NULL
			) STRICT`,
		`CREATE UNIQUE INDEX IF NOT EXISTS files_url ON files(url)`,
		`CREATE INDEX IF NOT EXISTS files_combined ON files(url, namespace)`,
	} {
		if _, err := db.Prep(sql).Step(); err != nil {
			panic(err)
		}
	}
}

func (p *Proxy) withDbReadOnly(f func(*sqlite.Conn) error) error {
	db, err := sqlite.OpenConn(
		p.dbFile(),
		sqlite.SQLITE_OPEN_READONLY|
			sqlite.SQLITE_OPEN_SHAREDCACHE|
			sqlite.SQLITE_OPEN_WAL|
			sqlite.SQLITE_OPEN_URI|
			sqlite.SQLITE_OPEN_NOMUTEX,
	)
	if err != nil {
		return errors.WithMessagef(err, "while opening database: %q", p.dbFile())
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
				p.log.Error("while closing database", zap.Error(err))
			}
		}()
	}()

	if ferr := f(db); ferr != nil {
		return errors.WithMessage(ferr, "from withDB callback")
	}

	return nil
}

func (p *Proxy) withDbReadWrite(f func(*sqlite.Conn) error) error {
	db, err := sqlite.OpenConn(
		p.dbFile(),
		sqlite.SQLITE_OPEN_READWRITE|
			sqlite.SQLITE_OPEN_PRIVATECACHE|
			sqlite.SQLITE_OPEN_WAL|
			sqlite.SQLITE_OPEN_URI|
			sqlite.SQLITE_OPEN_NOMUTEX,
	)
	if err != nil {
		return errors.WithMessagef(err, "while opening database: %q", p.dbFile())
	}
	defer func() {
		if _, err := db.Prep(`PRAGMA optimize;`).Step(); err != nil {
			p.log.Error("while running PRAGMA optimize", zap.Error(err))
		}
		if err := db.Close(); err != nil {
			p.log.Error("while closing database", zap.Error(err))
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
