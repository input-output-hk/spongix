package main

import (
	"bufio"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/kr/pretty"
	"github.com/pkg/errors"
)

type NarInfo struct {
	StorePath   string
	URL         string
	Compression string
	FileHash    string
	FileSize    uint64
	NarHash     string
	NarSize     uint64
	References  []string
	Deriver     string
	Sig         []string
	CA          string
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
		pretty.Println(line)
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
			info.StorePath = value
		case "URL":
			info.URL = value
		case "Compression":
			info.Compression = value
		case "FileHash":
			info.FileHash = value
		case "FileSize":
			if fileSize, err := strconv.ParseUint(value, 10, 64); err != nil {
				return err
			} else {
				info.FileSize = fileSize
			}
		case "NarHash":
			info.NarHash = value
		case "NarSize":
			if narSize, err := strconv.ParseUint(value, 10, 64); err != nil {
				return err
			} else {
				info.NarSize = narSize
			}
		case "References":
			info.References = append(info.References, strings.Split(value, " ")...)
		case "Deriver":
			info.Deriver = value
		case "Sig":
			info.Sig = append(info.Sig, value)
		case "CA":
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
	validCompression  = regexp.MustCompile(`\A(|none|xz|bzip2|br)\z`)
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
	}

	return errors.New("No matching signature found")
}

func (info *NarInfo) signMsg() string {
	refs := []string{}
	for _, ref := range info.References {
		refs = append(refs, "/nix/store/"+ref)
	}

	return fmt.Sprintf("1;%s;%s;%s;%s",
		info.StorePath,
		info.NarHash,
		strconv.FormatUint(info.NarSize, 10),
		strings.Join(refs, ","))
}

func (info *NarInfo) Sign(nixPrivateKey *NixPrivateKey) error {
	signature := info.Signature(nixPrivateKey)
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

func (info *NarInfo) Signature(nixPrivateKey *NixPrivateKey) string {
	signature := ed25519.Sign(nixPrivateKey.key, []byte(info.signMsg()))
	return nixPrivateKey.name + ":" + base64.StdEncoding.EncodeToString(signature)
}
