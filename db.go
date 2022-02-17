package main

import (
	"database/sql"
	"net/url"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap"
)

func (proxy *Proxy) SetupDB() {
	dsn, err := url.Parse(proxy.DatabaseDSN)
	fatal(err)
	query := dsn.Query()
	// query.Add("cache", "shared")
	// query.Add("mode", "memory")
	query.Add("_auto_vacuum", "incremental")
	query.Add("_foreign_keys", "true")
	query.Add("_journal_mode", "wal")
	query.Add("_synchronous", "FULL")
	query.Add("_loc", "UTC")
	dsn.RawQuery = query.Encode()

	db, err := sql.Open("sqlite3", dsn.String())
	fatal(err)

	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS narinfos (
	name text PRIMARY KEY,
  store_path text,
	url text,
	compression text,
	file_hash_type text,
	file_hash text,
	file_size integer,
	nar_hash_type text,
	nar_hash text,
	nar_size integer,
	deriver text,
	ca text,
	created_at datetime,
	accessed_at datetime
);
`)
	fatal(err)

	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS refs (
  parent text,
	child text,
  FOREIGN KEY(parent) REFERENCES narinfos(name) DEFERRABLE INITIALLY DEFERRED
);
`)
	fatal(err)

	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS signatures (
  name text,
	signature text,
	FOREIGN KEY(name) REFERENCES narinfos(name) DEFERRABLE INITIALLY DEFERRED
);
`)
	fatal(err)

	proxy.db = db
}

func (proxy *Proxy) selectNarinfo(name string) (*Narinfo, error) {
	res := proxy.db.QueryRow(`
	SELECT
		store_path, url, compression,
		file_hash_type, file_hash, file_size,
		nar_hash_type, nar_hash, nar_size,
		deriver, ca
	FROM narinfos
	WHERE name = ?`, name)

	var (
		store_path, url, compression,
		file_hash_type, file_hash,
		nar_hash_type, nar_hash,
		deriver, ca string

		file_size, nar_size int64
	)

	err := res.Scan(
		&store_path, &url, &compression,
		&file_hash_type, &file_hash, &file_size,
		&nar_hash_type, &nar_hash, &nar_size,
		&deriver, &ca,
	)

	if err == sql.ErrNoRows {
		return nil, err
	} else if err != nil {
		return nil, err
	}

	info := &Narinfo{
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

	sigs, err := proxy.db.Query(`SELECT signature FROM signatures WHERE name = ?`, name)
	if err != nil {
		return info, errors.WithMessage(err, "Failed getting narinfo signatures")
	}
	defer sigs.Close()
	for sigs.Next() {
		var signature string
		err := sigs.Scan(&signature)
		if err != nil {
			return info, errors.WithMessage(err, "Failed scanning narinfo signature")
		}
		info.Sig = append(info.Sig, signature)
	}
	sigs.Close()

	refs, err := proxy.db.Query(`SELECT child FROM refs WHERE parent = ?`, name)
	if err != nil {
		return info, errors.WithMessage(err, "Failed getting narinfo refs")
	}
	defer refs.Close()
	for refs.Next() {
		var ref string
		err := refs.Scan(&ref)
		if err != nil {
			return info, errors.WithMessage(err, "Failed scanning narinfo refs")
		}
		info.References = append(info.References, ref)
	}
	refs.Close()

	return info, nil
}

func (proxy *Proxy) insertNarinfo(info *Narinfo) error {
	_, err := proxy.db.Exec(`
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

	if err != nil {
		return errors.WithMessage(err, "Inserting narinfo failed")
	}

	for _, reference := range info.References {
		_, err = proxy.db.Exec(`
      INSERT INTO refs (parent, child) VALUES (?, ?)
      `, info.Name, reference)
		if err != nil {
			return errors.WithMessage(err, "Failed to insert refs")
		}
	}

	for _, signature := range info.Sig {
		_, err = proxy.db.Exec(`
      INSERT INTO signatures (name, signature) VALUES (?, ?)
      `, info.Name, signature)
		if err != nil {
			return errors.WithMessage(err, "Failed to insert signatures")
		}
	}

	return nil
}

func (proxy *Proxy) selectNarHash(name string) string {
	var narHash string

	res := proxy.db.QueryRow(`SELECT nar_hash FROM narinfos WHERE name = ?`, name)
	if err := res.Scan(&narHash); err != nil {
		if err == sql.ErrNoRows {
			return ""
		}
		proxy.log.Error("getting nar_hash for name", zap.Error(err))
	}

	return narHash
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
