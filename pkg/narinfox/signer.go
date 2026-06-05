package narinfox

import (
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
)

// Signer signs a narinfo fingerprint. Used by a single Proxy instance; not
// required to be reentrant beyond what the configured key type guarantees.
type Signer interface {
	// Name returns the public-key name (e.g. "cache.iog.io-1") used to
	// detect whether this key has already signed a narinfo.
	Name() string

	// Sign returns a detached signature over fingerprint, which is the
	// string returned by *narinfo.NarInfo.Fingerprint().
	Sign(fingerprint string) (signature.Signature, error)
}

// NewSigner wraps a go-nix SecretKey. Passing nil returns nil; callers may
// pass NewSigner(maybeKey) (or wire the field only when a key file is
// configured) without writing their own nil guard — Open accepts a nil Signer.
func NewSigner(key *signature.SecretKey) Signer {
	if key == nil {
		return nil
	}
	return &secretKeySigner{
		key:  *key,
		name: key.ToPublicKey().Name,
	}
}

type secretKeySigner struct {
	key  signature.SecretKey
	name string
}

func (s *secretKeySigner) Name() string { return s.name }

func (s *secretKeySigner) Sign(fingerprint string) (signature.Signature, error) {
	return s.key.Sign(nil, fingerprint)
}
