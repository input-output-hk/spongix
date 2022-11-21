package narinfo

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

type Narinfo struct {
	Name        string   `json:"name"`
	StorePath   string   `json:"store_path"`
	URL         string   `json:"url"`
	Compression string   `json:"compression"`
	FileHash    string   `json:"file_hash"`
	FileSize    int64    `json:"file_size"`
	NarHash     string   `json:"nar_hash"`
	NarSize     int64    `json:"nar_size"`
	References  []string `json:"references"`
	Deriver     string   `json:"deriver"`
	Sig         []string `json:"sig"`
	CA          string   `json:"ca"`
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
func (info *Narinfo) Unmarshal(input io.Reader) error {
	if input == nil {
		return errors.New("can't unmarshal nil reader")
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
			if fileSize, err := strconv.ParseInt(value, 10, 64); err == nil {
				info.FileSize = fileSize
			} else {
				return err
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
			if narSize, err := strconv.ParseInt(value, 10, 64); err == nil {
				info.NarSize = narSize
			} else {
				return err
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

	if info.Compression == "" {
		info.Compression = "bzip2"
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

func (info *Narinfo) Validate() error {
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
		i := strings.IndexRune(sig, ':')
		name := sig[0:i]
		sigStr := sig[i+1:]
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

func (info *Narinfo) Sign(name string, key ed25519.PrivateKey) {
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
