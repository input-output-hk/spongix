// Package narinfox is the wire-serving gateway for Nix .narinfo documents.
//
// Every narinfo that Spongix stores or serves passes through Open, which
// returns a Document whose Bytes always satisfy the Spongix wire invariant:
//
//	Compression: none
//	URL:         nar/<NarHash52>.nar
//	FileHash:    == NarHash
//	FileSize:    == NarSize
//	Signatures:  upstream signatures preserved + the configured Signer's
//	             signature appended when not already present
//
// Holding a *Document is a static proof that Bytes() satisfies the invariant.
// Callers cannot construct a Document directly, cannot mutate it, and cannot
// serialize a non-normalized narinfo.
//
// Open is deliberately lossy about the upstream URL: it rewrites URL to the
// canonical nar/<NarHash52>.nar form. Call sites that need the *original*
// upstream URL (main.go doCache and router.go tryFetchNarFromSubstituter, to
// drive the upstream NAR fetch) therefore call narinfo.Parse directly — Open
// would destroy the field they exist to read.
package narinfox

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/nixhash"

	"github.com/input-output-hk/spongix/pkg/store"
)

// ErrMissingNarHash is returned when the input narinfo lacks a NarHash field.
// A malformed NarHash value (wrong base32, missing algo prefix) is caught by
// narinfo.Parse itself and surfaces as a wrapped parse error.
var ErrMissingNarHash = errors.New("narinfox: narinfo is missing NarHash")

// Document is a parsed, normalized, optionally re-signed narinfo. The zero
// value is invalid; calling Bytes or Metadata on a zero or nil *Document
// panics with a nil-pointer dereference. Obtain one only via Open.
type Document struct {
	normalized []byte
	meta       Metadata
}

// Metadata projects the three indexing keys Spongix derives from a narinfo.
// Populated during Open from the same parse that produced Bytes; never
// computed from a separate scan over the normalized bytes.
type Metadata struct {
	NarHash52     store.NarHash52
	StorePathHash store.StorePathHash32
	References    []store.StorePathHash32
}

// Open parses, normalizes, and (if signer is non-nil) appends the signer's
// signature when not already present.
//
// It is the only constructor of Document and the single source of truth for
// the Spongix wire invariant.
//
// Returns an error for malformed input (parse failure, missing NarHash).
// Callers in router.go convert errors into either a 500 (proxyNarinfo, insert)
// or a cache-miss fallback to upstream (largeHeadAndGet).
func Open(r io.Reader, signer Signer) (*Document, error) {
	info, err := narinfo.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("narinfox: parsing narinfo: %w", err)
	}
	if info.NarHash == nil {
		return nil, ErrMissingNarHash
	}

	// Normalize compression. The go-nix parser maps an empty Compression
	// field to "bzip2", so only literal "none" means "already uncompressed".
	if info.Compression != "none" {
		info.Compression = "none"
		info.FileHash = info.NarHash
		info.FileSize = info.NarSize
	}

	// Canonical URL: nar/<NarHash52>.nar, regardless of upstream form.
	// Format(NixBase32, false) returns the 52-char base32 digest without the
	// "sha256:" prefix, sidestepping the split-on-":" the legacy code did.
	narHash52 := info.NarHash.Format(nixhash.NixBase32, false)
	info.URL = "nar/" + narHash52 + ".nar"

	// Append signature if requested and not already present from this signer.
	if signer != nil {
		name := signer.Name()
		alreadySigned := false
		for _, s := range info.Signatures {
			if s.Name == name {
				alreadySigned = true
				break
			}
		}
		if !alreadySigned {
			sig, err := signer.Sign(info.Fingerprint())
			if err != nil {
				return nil, fmt.Errorf("narinfox: signing: %w", err)
			}
			info.Signatures = append(info.Signatures, sig)
		}
	}

	doc := &Document{
		normalized: []byte(info.String()),
		meta: Metadata{
			NarHash52:     store.NarHash52(narHash52),
			StorePathHash: extractStorePathHash(info.StorePath),
			References:    extractReferenceHashes(info.References),
		},
	}
	return doc, nil
}

// Bytes returns the canonical wire-format serialization. Always normalized.
// The returned slice is owned by the Document; do not mutate.
func (d *Document) Bytes() []byte {
	return d.normalized
}

// Metadata returns the storage-relevant projection populated during Open.
// Callers MUST use this rather than re-parsing Bytes() — the latter defeats
// the single-parse property this package exists to provide.
func (d *Document) Metadata() Metadata {
	return d.meta
}

// extractStorePathHash returns the 32-char hash from a store path like
// "/nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test". Returns "" when the
// path is malformed; callers (gc, NarHash mapping) treat "" as "skip".
func extractStorePathHash(storePath string) store.StorePathHash32 {
	trimmed := strings.TrimPrefix(storePath, "/nix/store/")
	if idx := strings.IndexByte(trimmed, '-'); idx >= 32 {
		return store.StorePathHash32(trimmed[:32])
	}
	return ""
}

// extractReferenceHashes returns the 32-char hash portion of each reference
// entry. Entries without a dash are silently skipped — preserves the
// established behavior of the legacy parseNarinfoReferences helper.
func extractReferenceHashes(refs []string) []store.StorePathHash32 {
	if len(refs) == 0 {
		return nil
	}
	out := make([]store.StorePathHash32, 0, len(refs))
	for _, r := range refs {
		if idx := strings.IndexByte(r, '-'); idx > 0 {
			out = append(out, store.StorePathHash32(r[:idx]))
		}
	}
	return out
}
