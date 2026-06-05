package narinfox

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"

	"github.com/input-output-hk/spongix/pkg/store"
)

const (
	validNarHash32  = "1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp"
	upstreamSigName = "cache.nixos.org-1"
	upstreamSigB64  = "JccDYkaQjN7ywE9VGJ6/RAzCt7XJoqWsmjTRsdAdM8DF40ebDDu3XWaasuJkaezbhVxjaRLJm3VWDEk6EmRpCw=="
)

// fixtureXz is the canonical "upstream xz" narinfo: Compression=xz,
// FileHash != NarHash, URL has .nar.xz extension. Open must normalize all of
// these to the Spongix wire form.
const fixtureXz = `StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/0rjb49w7dldq79d3ax30gw279bnbn48w9q925g3lr0rhrl0ycxvs.nar.xz
Compression: xz
FileHash: sha256:0rjb49w7dldq79d3ax30gw279bnbn48w9q925g3lr0rhrl0ycxvs
FileSize: 224
NarHash: sha256:` + validNarHash32 + `
NarSize: 512
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: ` + upstreamSigName + `:` + upstreamSigB64 + `
`

// fixtureCanonical is the same narinfo already in Spongix wire form.
const fixtureCanonical = `StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/` + validNarHash32 + `.nar
Compression: none
FileHash: sha256:` + validNarHash32 + `
FileSize: 512
NarHash: sha256:` + validNarHash32 + `
NarSize: 512
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: ` + upstreamSigName + `:` + upstreamSigB64 + `
`

// newTestSigner returns a real ed25519 signer with a stable name. We use a
// real key (not a stub) so the resulting Signature parses back through
// narinfo.Parse — required by the round-trip tests.
func newTestSigner(t *testing.T, name string) (Signer, signature.SecretKey) {
	t.Helper()
	sk, _, err := signature.GenerateKeypair(name, nil)
	if err != nil {
		t.Fatalf("GenerateKeypair: %v", err)
	}
	return NewSigner(&sk), sk
}

func TestOpen_NormalizesCompression(t *testing.T) {
	doc, err := Open(strings.NewReader(fixtureXz), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := narinfo.Parse(bytes.NewReader(doc.Bytes()))
	if err != nil {
		t.Fatalf("re-parsing Bytes(): %v", err)
	}
	if info.Compression != "none" {
		t.Errorf("Compression = %q, want \"none\"", info.Compression)
	}
	if info.FileHash.String() != info.NarHash.String() {
		t.Errorf("FileHash %q != NarHash %q", info.FileHash, info.NarHash)
	}
	if info.FileSize != info.NarSize {
		t.Errorf("FileSize %d != NarSize %d", info.FileSize, info.NarSize)
	}
}

func TestOpen_NormalizesURL(t *testing.T) {
	doc, err := Open(strings.NewReader(fixtureXz), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := narinfo.Parse(bytes.NewReader(doc.Bytes()))
	if err != nil {
		t.Fatalf("re-parsing Bytes(): %v", err)
	}
	wantURL := "nar/" + validNarHash32 + ".nar"
	if info.URL != wantURL {
		t.Errorf("URL = %q, want %q", info.URL, wantURL)
	}
}

func TestOpen_Idempotent(t *testing.T) {
	signer, _ := newTestSigner(t, "test-1")
	first, err := Open(strings.NewReader(fixtureXz), signer)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := Open(bytes.NewReader(first.Bytes()), signer)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("Open is not idempotent\nfirst:\n%s\nsecond:\n%s", first.Bytes(), second.Bytes())
	}
}

// TestOpen_CanonicalFieldOrder documents that Bytes() is reordered to
// go-nix's canonical key order regardless of input order. Tests must rely on
// re-parsing or idempotency, not byte-for-byte equality with arbitrary input.
func TestOpen_CanonicalFieldOrder(t *testing.T) {
	shuffled := "NarSize: 512\n" +
		"References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test\n" +
		"NarHash: sha256:" + validNarHash32 + "\n" +
		"Compression: none\n" +
		"URL: nar/" + validNarHash32 + ".nar\n" +
		"StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\n"
	doc, err := Open(strings.NewReader(shuffled), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := string(doc.Bytes())
	if !strings.HasPrefix(got, "StorePath: ") {
		t.Errorf("output does not start with StorePath:\n%s", got)
	}
	storePathIdx := strings.Index(got, "StorePath:")
	urlIdx := strings.Index(got, "URL:")
	narHashIdx := strings.Index(got, "NarHash:")
	if storePathIdx >= urlIdx || urlIdx >= narHashIdx {
		t.Errorf("expected canonical order StorePath < URL < NarHash, got:\n%s", got)
	}
}

func TestOpen_NilSigner(t *testing.T) {
	doc, err := Open(strings.NewReader(fixtureCanonical), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := narinfo.Parse(bytes.NewReader(doc.Bytes()))
	if err != nil {
		t.Fatalf("re-parsing Bytes(): %v", err)
	}
	// Output retains only the original upstream signature.
	if len(info.Signatures) != 1 || info.Signatures[0].Name != upstreamSigName {
		t.Errorf("expected only upstream sig %q, got %v", upstreamSigName, info.Signatures)
	}
}

func TestOpen_SignsWhenAbsent(t *testing.T) {
	signer, sk := newTestSigner(t, "proxy-1")
	doc, err := Open(strings.NewReader(fixtureCanonical), signer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := narinfo.Parse(bytes.NewReader(doc.Bytes()))
	if err != nil {
		t.Fatalf("re-parsing Bytes(): %v", err)
	}
	pub := sk.ToPublicKey()
	if !signature.VerifyFirst(info.Fingerprint(), info.Signatures, []signature.PublicKey{pub}) {
		t.Errorf("signature did not verify against signer's public key")
	}
}

// TestOpen_SkipsWhenAlreadySigned asserts that calling Open twice with the
// same signer produces byte-identical output — re-signing is a no-op.
func TestOpen_SkipsWhenAlreadySigned(t *testing.T) {
	signer, _ := newTestSigner(t, "proxy-1")
	first, err := Open(strings.NewReader(fixtureCanonical), signer)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	second, err := Open(bytes.NewReader(first.Bytes()), signer)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatalf("re-Open with same signer changed bytes (should be no-op)\nfirst:\n%s\nsecond:\n%s",
			first.Bytes(), second.Bytes())
	}
}

func TestOpen_PreservesUpstreamSigs(t *testing.T) {
	signer, _ := newTestSigner(t, "proxy-1")
	doc, err := Open(strings.NewReader(fixtureCanonical), signer)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := narinfo.Parse(bytes.NewReader(doc.Bytes()))
	if err != nil {
		t.Fatalf("re-parsing Bytes(): %v", err)
	}
	var sawUpstream, sawProxy bool
	for _, s := range info.Signatures {
		if s.Name == upstreamSigName {
			sawUpstream = true
		}
		if s.Name == "proxy-1" {
			sawProxy = true
		}
	}
	if !sawUpstream {
		t.Errorf("upstream signature %q was stripped", upstreamSigName)
	}
	if !sawProxy {
		t.Errorf("proxy signature %q was not appended", "proxy-1")
	}
}

func TestOpen_MissingNarHash(t *testing.T) {
	input := "StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\n" +
		"URL: nar/h.nar\n" +
		"Compression: none\n" +
		"NarSize: 512\n"
	_, err := Open(strings.NewReader(input), nil)
	if !errors.Is(err, ErrMissingNarHash) {
		t.Fatalf("expected ErrMissingNarHash, got %v", err)
	}
}

func TestOpen_ParseError(t *testing.T) {
	// Garbage input: no "key: value" structure on the first line. The
	// upstream parser rejects this with "unable to find separator".
	_, err := Open(strings.NewReader("not a narinfo\n"), nil)
	if err == nil {
		t.Fatal("expected error on garbage input, got nil")
	}
	if errors.Is(err, ErrMissingNarHash) {
		t.Errorf("garbage input should not surface as ErrMissingNarHash: %v", err)
	}
}

func TestMetadata_References(t *testing.T) {
	type tc struct {
		name string
		body string
		want []store.StorePathHash32
	}
	cases := []tc{
		{
			name: "multiple",
			body: "StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\n" +
				"URL: nar/x.nar\nCompression: none\n" +
				"NarHash: sha256:" + validNarHash32 + "\nNarSize: 512\n" +
				"References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test abc123def456ghij789klmnopqr012345-other\n",
			want: []store.StorePathHash32{"5b4cprjhjw35wyzvgmgvqay4hjf59h7x", "abc123def456ghij789klmnopqr012345"},
		},
		{
			name: "single",
			body: "StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\n" +
				"URL: nar/x.nar\nCompression: none\n" +
				"NarHash: sha256:" + validNarHash32 + "\nNarSize: 512\n" +
				"References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test\n",
			want: []store.StorePathHash32{"5b4cprjhjw35wyzvgmgvqay4hjf59h7x"},
		},
		{
			name: "empty",
			body: "StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\n" +
				"URL: nar/x.nar\nCompression: none\n" +
				"NarHash: sha256:" + validNarHash32 + "\nNarSize: 512\n" +
				"References: \n",
			want: nil,
		},
		{
			name: "missing",
			body: "StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\n" +
				"URL: nar/x.nar\nCompression: none\n" +
				"NarHash: sha256:" + validNarHash32 + "\nNarSize: 512\n",
			want: nil,
		},
		{
			name: "no-dash entries silently skipped",
			body: "StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\n" +
				"URL: nar/x.nar\nCompression: none\n" +
				"NarHash: sha256:" + validNarHash32 + "\nNarSize: 512\n" +
				"References: nodashhere validhash-withname anothernodash\n",
			want: []store.StorePathHash32{"validhash"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := Open(strings.NewReader(c.body), nil)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			got := doc.Metadata().References
			if !equalRefs(got, c.want) {
				t.Errorf("References = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMetadata_NarHash52(t *testing.T) {
	doc, err := Open(strings.NewReader(fixtureCanonical), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := doc.Metadata().NarHash52
	want := store.NarHash52(validNarHash32)
	if got != want {
		t.Errorf("NarHash52 = %q, want %q", got, want)
	}
}

func TestMetadata_StorePathHash(t *testing.T) {
	doc, err := Open(strings.NewReader(fixtureCanonical), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := doc.Metadata().StorePathHash
	want := store.StorePathHash32("hyhrnrnpsz9fw5p9dk85a58y31ink18c")
	if got != want {
		t.Errorf("StorePathHash = %q, want %q", got, want)
	}
}

func TestMetadata_FromSingleParse(t *testing.T) {
	// Asserts Metadata is derived from the in-memory NarInfo, not from a
	// fresh scan over Bytes(). The property we check: re-parsing Bytes()
	// produces an info whose NarHash matches Metadata().NarHash52.
	doc, err := Open(strings.NewReader(fixtureXz), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := narinfo.Parse(bytes.NewReader(doc.Bytes()))
	if err != nil {
		t.Fatalf("re-parsing: %v", err)
	}
	want := store.NarHash52(strings.TrimPrefix(info.NarHash.String(), "sha256:"))
	if doc.Metadata().NarHash52 != want {
		t.Errorf("Metadata().NarHash52 = %q, want %q", doc.Metadata().NarHash52, want)
	}
}

func equalRefs(a, b []store.StorePathHash32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
