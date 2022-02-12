package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/numtide/go-nix/nixbase32"
	"github.com/pkg/errors"
)

type NarInfo struct {
	Name        string
	StorePath   string
	URL         string
	Compression string
	FileHash    string
	FileSize    int64
	NarHash     string
	NarSize     int64
	References  []string
	Deriver     string
	Sig         []string
	CA          string
}

func (proxy *Proxy) validateStore() {
	err := filepath.Walk(proxy.Dir, func(path string, fsInfo fs.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if fsInfo.IsDir() {
			return nil
		}

		switch filepath.Ext(path) {
		case ".narinfo":
			return validateNarinfo(proxy.Dir, path, true)
		case ".xz", ".bzip2", "br", "zst", ".nar":
			return validateNar(proxy.Dir, path)
		}
		return nil
	})

	if err != nil {
		log.Panicln(err)
	}

	log.Println("Cache validated")
}

func validateNar(dir, path string) error {
	r := regexp.MustCompile(`([^/.]+)\..+`)
	match := r.FindStringSubmatch(path)

	dec, err := nixbase32.DecodeString(match[1])
	if err != nil {
		return err
	}

	fd, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fd.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, fd); err != nil {
		return err
	}

	realSum := fmt.Sprintf("%x", hash.Sum(nil))
	needSum := fmt.Sprintf("%x", dec)

	if realSum != needSum {
		fmt.Printf("hash was %s but expected %s\n", realSum, needSum)
	}

	return nil
}

func validateNarinfo(dir, path string, remove bool) error {
	info := &NarInfo{}
	f, err := os.Open(path)
	if err != nil {
		log.Printf("%q couldn't be opened: %s", path, err.Error())
		return nil
	}

	if err := info.Unmarshal(f); err != nil {
		log.Printf("%q is not a valid narinfo: %s", path, err.Error())
		if remove {
			os.Remove(path)
		}
		return nil
	}

	narPath := filepath.Join(dir, info.URL)
	stat, err := os.Stat(narPath)
	if err != nil {
		log.Printf("%q for %q not found, removing narinfo", narPath, path)
		if remove {
			os.Remove(path)
		}
		return nil
	}

	ssize := stat.Size()

	if ssize != info.FileSize {
		log.Printf("%q should be size %d but has %d", narPath, info.FileSize, ssize)
		if remove {
			os.Remove(path)
			os.Remove(narPath)
		}
		return nil
	}

	return nil
}

func (info *NarInfo) Marshal(output io.Writer) error {
	out := bufio.NewWriter(output)

	write := func(format string, arg interface{}) error {
		_, err := out.WriteString(fmt.Sprintf(format, arg))
		return err
	}

	if err := write("StorePath: %s\n", info.StorePath); err != nil {
		return err
	}

	if err := write("URL: %s\n", info.URL); err != nil {
		return err
	}

	if err := write("Compression: %s\n", info.Compression); err != nil {
		return err
	}

	if err := write("FileHash: %s\n", info.FileHash); err != nil {
		return err
	}

	if err := write("FileSize: %d\n", info.FileSize); err != nil {
		return err
	}

	if err := write("NarHash: %s\n", info.NarHash); err != nil {
		return err
	}

	if err := write("NarSize: %d\n", info.NarSize); err != nil {
		return err
	}

	if len(info.References) > 0 {
		if err := write("References: %s\n", strings.Join(info.References, " ")); err != nil {
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
func (info *NarInfo) Unmarshal(input io.Reader) error {
	scanner := bufio.NewScanner(input)
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
			if info.StorePath != "" {
				return errors.Errorf("Duplicate StorePath")
			}
			info.StorePath = value
			parts := strings.SplitN(filepath.Base(value), "-", 2)
			info.Name = parts[0]
		case "URL":
			if info.URL != "" {
				return errors.Errorf("Duplicate URL")
			}
			info.URL = value
		case "Compression":
			if info.Compression != "" {
				return errors.Errorf("Duplicate Compression")
			}
			info.Compression = value
		case "FileHash":
			if info.FileHash != "" {
				return errors.Errorf("Duplicate FileHash")
			}
			info.FileHash = value
		case "FileSize":
			if info.FileSize != 0 {
				return errors.Errorf("Duplicate FileSize")
			}
			if fileSize, err := strconv.ParseInt(value, 10, 64); err != nil {
				return err
			} else {
				info.FileSize = fileSize
			}
		case "NarHash":
			if info.NarHash != "" {
				return errors.Errorf("Duplicate NarHash")
			}
			info.NarHash = value
		case "NarSize":
			if info.NarSize != 0 {
				return errors.Errorf("Duplicate NarSize")
			}
			if narSize, err := strconv.ParseInt(value, 10, 64); err != nil {
				return err
			} else {
				info.NarSize = narSize
			}
		case "References":
			info.References = append(info.References, strings.Split(value, " ")...)
		case "Deriver":
			if info.Deriver != "" {
				return errors.Errorf("Duplicate Deriver")
			}
			info.Deriver = value
		case "Sig":
			info.Sig = append(info.Sig, value)
		case "CA":
			if info.CA != "" {
				return errors.Errorf("Duplicate CA")
			}
			info.CA = value
		default:
			return errors.Errorf("Unknown narinfo key: %q: %v", key, value)
		}
	}

	if err := scanner.Err(); err != nil {
		return errors.WithMessage(err, "Parsing narinfo")
	}

	if err := info.Validate(); err != nil {
		return errors.WithMessage(err, "Validating narinfo")
	}

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

func (info *NarInfo) Validate() error {
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
		if !validStorePath.MatchString(ref) {
			return errors.Errorf("Invalid Reference: %q", ref)
		}
	}

	if info.Deriver != "" && !validDeriver.MatchString(info.Deriver) {
		return errors.Errorf("Invalid Deriver: %q", info.Deriver)
	}

	return nil
}

func (info *NarInfo) Verify(publicKeys map[string]ed25519.PublicKey) error {
	signMsg := info.signMsg()

	signatures := []string{}

	// finally we need at leaat one matching signature
	for _, sig := range info.Sig {
		i := strings.IndexRune(sig, ':')
		name := sig[0:i]
		sigStr := sig[i+1:]
		signature, err := base64.StdEncoding.DecodeString(sigStr)
		if err != nil {
			return errors.Errorf("Signature decoding failed for %q", sigStr)
		}

		if key, ok := publicKeys[name]; ok {
			if ed25519.Verify(key, []byte(signMsg), signature) {
				return nil
			} else {
				return errors.Errorf("Signed by %q but signature doesn't match narinfo", name)
			}
		}

		signatures = append(signatures, name)
	}

	return errors.Errorf("No matching signature found in %q", signatures)
}

func (info *NarInfo) signMsg() string {
	refs := []string{}
	for _, ref := range info.References {
		refs = append(refs, "/nix/store/"+ref)
	}

	return fmt.Sprintf("1;%s;%s;%s;%s",
		info.StorePath,
		info.NarHash,
		strconv.FormatInt(info.NarSize, 10),
		strings.Join(refs, ","))
}

func (info *NarInfo) Sign(name string, key ed25519.PrivateKey) error {
	signature := info.Signature(name, key)
	missing := true

	for _, sig := range info.Sig {
		if sig == signature {
			missing = false
		}
	}

	if missing {
		info.Sig = append(info.Sig, signature)
	}

	return nil
}

func (info *NarInfo) Signature(name string, key ed25519.PrivateKey) string {
	signature := ed25519.Sign(key, []byte(info.signMsg()))
	return name + ":" + base64.StdEncoding.EncodeToString(signature)
}

func (info *NarInfo) NarHashType() string {
	return strings.SplitN(info.NarHash, ":", 2)[0]
}

func (info *NarInfo) NarHashValue() string {
	return strings.SplitN(info.NarHash, ":", 2)[1]
}

func (info *NarInfo) FileHashType() string {
	return strings.SplitN(info.FileHash, ":", 2)[0]
}

func (info *NarInfo) FileHashValue() string {
	return strings.SplitN(info.FileHash, ":", 2)[1]
}
