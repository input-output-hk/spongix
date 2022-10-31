package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/pkg/errors"
)

type Narinfo struct {
	Name        string     `json:"name"`
	StorePath   string     `json:"store_path" db:"store_path"`
	URL         string     `json:"url"`
	Compression string     `json:"compression"`
	FileHash    string     `json:"file_hash" db:"file_hash"`
	FileSize    int64      `json:"file_size" db:"file_size"`
	NarHash     string     `json:"nar_hash" db:"nar_hash"`
	NarSize     int64      `json:"nar_size" db:"nar_size"`
	References  References `json:"references" db:"-"`
	Deriver     string     `json:"deriver"`
	Sig         []string   `json:"sig" db:"-"`
	CA          string     `json:"ca"`
	ID          int64
	Namespace   string
	CTime       time.Time `db:"ctime"`
	ATime       time.Time `db:"atime"`
}

type Reference string
type References []Reference

func (r References) String() string {
	return r.join(" ")
}

func (r References) sigFormat() string {
	return r.join(",")
}

func (r References) join(sep string) string {
	rs := make([]string, len(r))
	for i, v := range r {
		rs[i] = string(v)
	}
	return strings.Join(rs, sep)
}

/*
func (proxy *Proxy) validateNarinfo(dir, path string, remove bool) error {
	info := &Narinfo{}
	f, err := os.Open(path)
	if err != nil {
		proxy.log.Error("Failed to open narinfo", zap.String("path", path), zap.Error(err))
		return nil
	}

	if err := info.Unmarshal(f); err != nil {
		proxy.log.Error("Failed to unmarshal narinfo", zap.String("path", path), zap.Error(err))
		if remove {
			os.Remove(path)
		}
		return nil
	}

	narPath := filepath.Join(dir, info.URL)
	stat, err := os.Stat(narPath)
	if err != nil {
		proxy.log.Error("Failed to find NAR", zap.String("nar_path", narPath), zap.String("path", path), zap.Error(err))
		if remove {
			os.Remove(path)
		}
		return nil
	}

	ssize := stat.Size()

	if ssize != info.FileSize {
		log.Printf("%q should be size %d but has %d", narPath, info.FileSize, ssize)
		proxy.log.Error("NAR has wrong size", zap.String("nar_path", narPath), zap.String("path", path), zap.Int64("expected", info.FileSize), zap.Int64("actual", ssize))
		if remove {
			os.Remove(path)
			os.Remove(narPath)
		}
		return nil
	}

	return nil
}
*/

func (info *Narinfo) PrepareForStorage(
	trustedKeys map[string]ed25519.PublicKey,
	secretKeys map[string]ed25519.PrivateKey,
) (io.Reader, error) {
	info.SanitizeNar()
	info.SanitizeSignatures(trustedKeys)
	if len(info.Sig) == 0 {
		for name, key := range secretKeys {
			info.Sign(name, key)
		}
	}
	return info.ToReader()
}

func (info *Narinfo) ToReader() (io.Reader, error) {
	buf := &bytes.Buffer{}
	err := info.Marshal(buf)
	return buf, err
}

func (info *Narinfo) Marshal(output io.Writer) error {
	out := bufio.NewWriter(output)

	write := func(format string, arg interface{}) error {
		_, err := out.WriteString(fmt.Sprintf(format, arg))
		return err
	}

	if err := write("StorePath: %s\n", info.StorePath); err != nil {
		return err
	} else if err := write("URL: %s\n", info.URL); err != nil {
		return err
	} else if err := write("Compression: %s\n", info.Compression); err != nil {
		return err
	} else if err := write("FileHash: %s\n", info.FileHash); err != nil {
		return err
	} else if err := write("FileSize: %d\n", info.FileSize); err != nil {
		return err
	} else if err := write("NarHash: %s\n", info.NarHash); err != nil {
		return err
	} else if err := write("NarSize: %d\n", info.NarSize); err != nil {
		return err
	}

	if len(info.References) > 0 {
		if err := write("References: %s\n", info.References.String()); err != nil {
			return err
		}
	}

	if len(info.Deriver) > 0 {
		if err := write("Deriver: %s\n", info.Deriver); err != nil {
			return err
		}
	}

	for _, sig := range info.Sig {
		if _, err := out.WriteString(fmt.Sprintf("Sig: %s\n", sig)); err != nil {
			return err
		}
	}

	return out.Flush()
}

// TODO: replace with a validating parser
func (info *Narinfo) Unmarshal(input io.Reader) error {
	if input == nil {
		return errors.New("can't unmarshal nil reader")
	}

	if info.Namespace == "" {
		return errors.New("Namespace must be set before Unmarshal")
	}

	scanner := bufio.NewScanner(input)
	capacity := 1024 * 1024
	buf := make([]byte, 0, capacity)
	scanner.Buffer(buf, capacity)

	for scanner.Scan() {
		line := scanner.Text()

		parts := strings.SplitN(line, ": ", 2)
		if len(parts) != 2 {
			return errors.Errorf("Failed to parse line: %q", line)
		}
		key := parts[0]
		value := parts[1]
		if value == "" {
			continue
		}

		switch key {
		case "StorePath":
			if err := info.SetStorePath(value); err != nil {
				return errors.WithMessage(err, "parsing StorePath")
			}
		case "URL":
			if err := info.SetURL(value); err != nil {
				return errors.WithMessage(err, "parsing URL")
			}
		case "Compression":
			if err := info.SetCompression(value); err != nil {
				return errors.WithMessage(err, "parsing Compression")
			}
		case "FileHash":
			if err := info.SetFileHash(value); err != nil {
				return errors.WithMessage(err, "parsing FileHash")
			}
		case "FileSize":
			if err := info.SetFileSize(value); err != nil {
				return errors.WithMessage(err, "parsing FileSize")
			}
		case "NarHash":
			if err := info.SetNarHash(value); err != nil {
				return errors.WithMessage(err, "parsing NarHash")
			}
		case "NarSize":
			if narSize, err := strconv.ParseInt(value, 10, 64); err != nil {
				return errors.WithMessage(err, "parsing NarSize")
			} else if err := info.SetNarSize(narSize); err != nil {
				return errors.WithMessage(err, "parsing NarSize")
			}
		case "References":
			values := strings.Split(value, " ")
			if err := info.SetReferences(values); err != nil {
				return errors.WithMessage(err, "parsing References")
			}
		case "Deriver":
			if err := info.SetDeriver(value); err != nil {
				return errors.WithMessage(err, "parsing Deriver")
			}
		case "Sig":
			if err := info.AddSig(value); err != nil {
				return errors.WithMessage(err, "parsing Sig")
			}
		case "CA":
			if err := info.SetCA(value); err != nil {
				return errors.WithMessage(err, "parsing CA")
			}
		default:
			return errors.Errorf("Unknown narinfo key: %q: %v", key, value)
		}
	}

	if err := scanner.Err(); err != nil {
		return errors.WithMessage(err, "Parsing narinfo")
	}

	if info.Compression == "" {
		info.Compression = "bzip2"
	}

	if err := info.Validate(); err != nil {
		return errors.WithMessage(err, "Validating narinfo")
	}

	return nil
}

func (info *Narinfo) AddSig(sig string) error {
	return info.AddSigs([]string{sig})
}

func (info *Narinfo) AddSigs(values []string) error {
	all := map[string]struct{}{}

	for _, sig := range info.Sig {
		all[sig] = struct{}{}
	}

	for _, value := range values {
		all[value] = struct{}{}
	}

	keys := []string{}

	for sig, _ := range all {
		keys = append(keys, sig)
	}

	sort.Strings(keys)
	info.Sig = keys

	return nil
}

func (info *Narinfo) SetCA(ca string) error {
	if info.CA != "" {
		return errors.Errorf("Duplicate CA")
	}
	info.CA = ca
	return nil
}

func (info *Narinfo) SetCompression(value string) error {
	if info.Compression != "" {
		return errors.Errorf("Duplicate Compression")
	}
	info.Compression = value
	return nil
}

func (info *Narinfo) SetDeriver(deriver string) error {
	if info.Deriver != "" {
		return errors.Errorf("Duplicate Deriver")
	}
	info.Deriver = deriver
	return nil
}

func (info *Narinfo) SetFileHash(fileHash string) error {
	if info.FileHash != "" {
		return errors.Errorf("Duplicate FileHash")
	}
	info.FileHash = fileHash
	return nil
}

func (info *Narinfo) SetFileSize(fileSize string) error {
	if info.FileSize != 0 {
		return errors.Errorf("Duplicate FileSize")
	} else if fileSize, err := strconv.ParseInt(fileSize, 10, 64); err != nil {
		return errors.WithMessage(err, "parsing FileSize")
	} else {
		info.FileSize = fileSize
	}

	return nil
}

func (info *Narinfo) SetNarHash(narHash string) error {
	if info.NarHash != "" {
		return errors.Errorf("Duplicate NarHash")
	}
	info.NarHash = narHash
	return nil
}

func (info *Narinfo) SetNarSize(narSize int64) error {
	if info.NarSize != 0 {
		return errors.Errorf("Duplicate NarSize")
	} else {
		info.NarSize = narSize
	}
	return nil
}

func (info *Narinfo) SetReferences(references []string) error {
	refs := make([]Reference, len(references))
	for i, r := range references {
		refs[i] = Reference(r)
	}
	info.References = append(info.References, refs...)
	return nil
}

func (info *Narinfo) SetStorePath(path string) error {
	if info.StorePath != "" {
		return errors.Errorf("Duplicate StorePath")
	}
	info.StorePath = path
	parts := strings.SplitN(filepath.Base(path), "-", 2)
	info.Name = parts[0]
	return nil
}

func (info *Narinfo) SetURL(url string) error {
	if info.URL != "" {
		return errors.Errorf("Duplicate URL")
	}
	info.URL = url
	return nil
}

var (
	nixHash           = `[0-9a-df-np-sv-z]`
	validNixStorePath = regexp.MustCompile(`\A/nix/store/` + nixHash + `{32}-.+\z`)
	validStorePath    = regexp.MustCompile(`\A` + nixHash + `{32}-.+\z`)
	validURL          = regexp.MustCompile(`\Anar/` + nixHash + `{52}(\.drv|\.nar(\.(xz|bz2|zst|lzip|lz4|br))?)\z`)
	validCompression  = regexp.MustCompile(`\A(|none|xz|bzip2|br|zst)\z`)
	validHash         = regexp.MustCompile(`\Asha256:` + nixHash + `{52}\z`)
	validDeriver      = regexp.MustCompile(`\A` + nixHash + `{32}-.+\.drv\z`)
)

func (info *Narinfo) Validate() error {
	if info.Namespace == "" {
		return errors.New("Empty Namespace")
	}

	if !validNixStorePath.MatchString(info.StorePath) {
		return errors.Errorf("Invalid StorePath: %q", info.StorePath)
	}

	if !validURL.MatchString(info.URL) {
		return errors.Errorf("Invalid URL: %q", info.URL)
	}

	if !validCompression.MatchString(info.Compression) {
		return errors.Errorf("Invalid Compression: %q", info.Compression)
	}

	if !validHash.MatchString(info.FileHash) {
		return errors.Errorf("Invalid FileHash: %q", info.FileHash)
	}

	if info.FileSize == 0 {
		return errors.Errorf("Invalid FileSize: %d", info.FileSize)
	}

	if !validHash.MatchString(info.NarHash) {
		return errors.Errorf("Invalid NarHash: %q", info.NarHash)
	}

	if info.NarSize == 0 {
		return errors.Errorf("Invalid NarSize: %d", info.NarSize)
	}

	for _, ref := range info.References {
		if !validStorePath.MatchString(string(ref)) {
			return errors.Errorf("Invalid Reference: %q", ref)
		}
	}

	if info.Deriver != "" && !validDeriver.MatchString(info.Deriver) {
		return errors.Errorf("Invalid Deriver: %q", info.Deriver)
	}

	return nil
}

// modifies the Narinfo to point to an uncompressed NAR file.
// This doesn't affect validity of the signature.
func (info *Narinfo) SanitizeNar() {
	if info.Compression == "none" {
		return
	}

	info.FileHash = info.NarHash
	info.FileSize = info.NarSize
	info.Compression = "none"

	ext := filepath.Ext(info.URL)
	info.URL = info.URL[0 : len(info.URL)-len(ext)]
}

// ensures only valid sigantures are kept in the Narinfo
func (info *Narinfo) SanitizeSignatures(publicKeys map[string]ed25519.PublicKey) {
	valid, _ := info.ValidInvalidSignatures(publicKeys)
	info.Sig = valid
}

// Returns valid and invalid signatures
func (info *Narinfo) ValidInvalidSignatures(publicKeys map[string]ed25519.PublicKey) ([]string, []string) {
	if len(info.Sig) == 0 {
		return nil, nil
	}

	signMsg := info.signMsg()
	valid := []string{}
	invalid := []string{}

	// finally we need at leaat one matching signature
	for _, sig := range info.Sig {
		i := strings.IndexRune(string(sig), ':')
		name := string(sig[0:i])
		sigStr := string(sig[i+1:])
		signature, err := base64.StdEncoding.DecodeString(sigStr)
		if err != nil {
			invalid = append(invalid, sig)
		} else if key, ok := publicKeys[name]; ok {
			if ed25519.Verify(key, []byte(signMsg), signature) {
				valid = append(valid, sig)
			} else {
				invalid = append(invalid, sig)
			}
		}
	}

	return valid, invalid
}

func (info *Narinfo) signMsg() string {
	refs := make(References, len(info.References))
	for i, ref := range info.References {
		refs[i] = Reference("/nix/store/" + ref)
	}

	return fmt.Sprintf("1;%s;%s;%s;%s",
		info.StorePath,
		info.NarHash,
		strconv.FormatInt(info.NarSize, 10),
		refs.sigFormat())
}

func (info *Narinfo) Sign(name string, key ed25519.PrivateKey) {
	info.Sig = append(info.Sig, info.Signature(name, key))
}

func (info *Narinfo) Signature(name string, key ed25519.PrivateKey) string {
	signature := ed25519.Sign(key, []byte(info.signMsg()))
	return name + ":" + base64.StdEncoding.EncodeToString(signature)
}

func (info *Narinfo) NarHashType() string {
	return strings.SplitN(info.NarHash, ":", 2)[0]
}

func (info *Narinfo) NarHashValue() string {
	return strings.SplitN(info.NarHash, ":", 2)[1]
}

func (info *Narinfo) FileHashType() string {
	return strings.SplitN(info.FileHash, ":", 2)[0]
}

func (info *Narinfo) FileHashValue() string {
	return strings.SplitN(info.FileHash, ":", 2)[1]
}

func (info *Narinfo) dbInsert(db *sqlx.DB) error {
	if info.Namespace == "" {
		return errors.New("Cannot insert without namespace")
	}

	info.CTime = time.Now().UTC()
	info.ATime = time.Now().UTC()

	tx, err := db.Beginx()
	if err != nil {
		return err
	}

	res, err := tx.NamedExec(`
			INSERT OR REPLACE INTO narinfos
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
		  , :namespace
		  , :ctime
		  , :atime
			)
		`, info,
	)
	if err != nil {
		defer tx.Rollback()
		return err
	}

	id, err := res.LastInsertId()
	if err != nil {
		defer tx.Rollback()
		return err
	}
	info.ID = id

	for _, ref := range info.References {
		if _, err := tx.Exec(
			`INSERT INTO narinfo_refs (narinfo_id, ref) VALUES (?, ?)`,
			info.ID, ref,
		); err != nil {
			defer tx.Rollback()
			return err
		}
	}

	for _, sig := range info.Sig {
		if _, err := tx.Exec(
			`INSERT INTO narinfo_sigs (narinfo_id, sig) VALUES (?, ?)`,
			info.ID, sig,
		); err != nil {
			defer tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func findNarinfo(db *sqlx.DB, namespace, name string) (*Narinfo, error) {
	// use transaction in case of GC.
	tx, err := db.Beginx()
	if err != nil {
		tx.Rollback()
		return nil, errors.WithMessage(err, "while beginning transaction")
	}

	narinfoQuery := tx.QueryRowx(`SELECT * FROM narinfos WHERE name IS ? AND namespace IS ?;`, name, namespace)
	info := Narinfo{}
	if err := narinfoQuery.StructScan(&info); err != nil {
		defer tx.Rollback()
		return nil, errors.WithMessage(err, "while selecting narinfos")
	}

	refQuery, err := tx.Queryx(`SELECT ref FROM narinfo_refs WHERE narinfo_id IS ?`, info.ID)
	defer refQuery.Close()
	if err != nil {
		defer tx.Rollback()
		return nil, errors.WithMessage(err, "while selecting narinfo_refs")
	}

	for refQuery.Next() {
		var ref string
		if refQuery.Scan(&ref); err != nil {
			defer refQuery.Close()
			defer tx.Rollback()
			return nil, errors.WithMessage(err, "while scanning narinfo_refs")
		}
		info.References = append(info.References, Reference(ref))
	}

	sigQuery, err := tx.Queryx(`SELECT sig FROM narinfo_sigs WHERE narinfo_id IS ?`, info.ID)
	defer sigQuery.Close()
	if err != nil {
		defer tx.Rollback()
		return nil, errors.WithMessage(err, "while selecting narinfo_sigs")
	}

	for sigQuery.Next() {
		var sig string
		if sigQuery.Scan(&sig); err != nil {
			defer sigQuery.Close()
			defer tx.Rollback()
			return nil, errors.WithMessage(err, "while scanning narinfo_sigs")
		}
		info.Sig = append(info.Sig, sig)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if _, err := db.Exec(`UPDATE narinfos SET atime = ? WHERE id IS ?`, time.Now().UTC(), info.ID); err != nil {
		return nil, errors.WithMessage(err, "while updating atime")
	}

	return &info, nil
}
