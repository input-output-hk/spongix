package main

import (
	"os"

	"github.com/j0holo/go-sqlite-lite/sqlite3"
)

// go run .  3.45s user 8.30s system 60% cpu 19.504 total

func experiment() {
	// _ = os.Remove("test.sqlite")

	// db, err := sqlx.Open("sqlite3", "file:test.sqlite?_journal_mode=WAL")
	db, err := sqlite3.Open("test.sqlite")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	if err := db.Exec(`
		PRAGMA cell_size_check = ON;
		PRAGMA foreign_keys = ON;
		PRAGMA recursive_triggers = ON;
		PRAGMA reverse_unordered_selects = ON;
	`); err != nil {
		panic(err)
	}

	if err := db.Exec(`
		CREATE TABLE IF NOT EXISTS indices
		( name TEXT PRIMARY KEY UNIQUE NOT NULL
		) STRICT;

		CREATE TABLE IF NOT EXISTS chunks
		( hash TEXT PRIMARY KEY UNIQUE NOT NULL
		, data BLOB NOT NULL
		) STRICT;

		CREATE TABLE IF NOT EXISTS indices_chunks
		( id INTEGER PRIMARY KEY NOT NULL
		, index_name TEXT NOT NULL
		, chunk_hash TEXT NOT NULL
		, offset INTEGER NOT NULL
		, FOREIGN KEY(index_name) REFERENCES indices(name)
		, FOREIGN KEY(chunk_hash) REFERENCES chunks(hash)
		) STRICT;
		CREATE INDEX IF NOT EXISTS indices_chunks_index_name ON indices_chunks(index_name);
		CREATE UNIQUE INDEX IF NOT EXISTS indices_chunks_combined ON indices_chunks(index_name, chunk_hash, offset);
	`); err != nil {
		panic(err)
	}

	// store(db, "ghc2.nar")
	load(db, "ghc.nar", "ghc2.nar")

	if err := db.Exec(`
		PRAGMA analysis_limit=1000;
		PRAGMA optimize;
	`); err != nil {
		panic(err)
	}

	db.Close()
	os.Exit(0)
}

func load(db *sqlite3.Conn, indexName, target string) {
	fd, err := os.Create(target)
	if err != nil {
		panic(err)
	}
	defer fd.Close()

	stmt, err := db.Prepare(`SELECT data FROM indices_chunks LEFT JOIN chunks ON hash IS chunk_hash WHERE index_name IS ? ORDER BY offset;`)
	if err != nil {
		panic(err)
	}
	defer stmt.Close()

	if err := stmt.Exec(indexName); err != nil {
		panic(err)
	} else if hasRow, err := stmt.Step(); err != nil {
		panic(err)
	} else {
		pp(hasRow)
	}

	// SELECT hash, data FROM chunks WHERE hash IS ?
	// rows, err := db.Queryx(`SELECT data FROM indices_chunks LEFT JOIN chunks ON hash IS chunk_hash WHERE index_name IS ? ORDER BY offset;`, indexName)
	// if err != nil {
	// 	panic(err)
	// }
	// defer rows.Close()

	// buf := bytes.Buffer{}

	// for rows.Next() {
	// 	var chunk []byte
	// 	if err := rows.Scan(&chunk); err != nil {
	// 		panic(err)
	// 	}
	// 	buf.Write(chunk)
	// }

	// os.WriteFile(target, buf.Bytes(), 0644)
}

// func store(db *sqlx.DB, indexName string) {
// 	fd, err := os.Open(indexName)
// 	if err != nil {
// 		panic(err)
// 	}
// 	defer fd.Close()
//
// 	chunker, err := desync.NewChunker(fd, chunkSizeMin(), chunkSizeAvg, chunkSizeMax())
// 	if err != nil {
// 		panic(err)
// 	}
//
// 	if _, err := db.Exec(`INSERT INTO indices (name) VALUES (?)`, indexName); err != nil {
// 		panic(err)
// 	}
//
// 	insertChunks, err := db.Prepare(`INSERT OR IGNORE INTO chunks (hash, data) VALUES (?, ?);`)
// 	defer insertChunks.Close()
// 	if err != nil {
// 		panic(err)
// 	}
// 	insertIndicesChunks, err := db.Prepare(`INSERT INTO indices_chunks (index_name, chunk_hash, offset) VALUES (?, ?, ?);`)
// 	defer insertChunks.Close()
//
// 	for {
// 		offset, chunk, err := chunker.Next()
// 		if err != nil {
// 			panic(err)
// 		}
//
// 		hash := desync.Digest.Sum(chunk)
// 		hashHex := fmt.Sprintf("%x", hash)
//
// 		if len(chunk) == 0 {
// 			break
// 		}
//
// 		if _, err := db.Exec(`INSERT OR IGNORE INTO chunks (hash, data) VALUES (?, ?);`, hashHex, chunk); err != nil {
// 			panic(err)
// 		} else if _, err := db.Exec(`INSERT INTO indices_chunks (index_name, chunk_hash, offset) VALUES (?, ?, ?);`, indexName, hashHex, offset); err != nil {
// 			panic(err)
// 		} else {
// 			// pp(hashHex, offset, indexName, indexChunkId)
// 		}
// 	}
// }
