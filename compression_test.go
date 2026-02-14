package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/steinfletcher/apitest"
	"github.com/ulikunitz/xz"
	"gotest.tools/v3/assert"
)

// Test helpers for compression fixtures.

func loadTestNar(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar")
	assert.NilError(t, err)
	return data
}

func compressWithXz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := xz.NewWriter(&buf)
	assert.NilError(t, err)
	_, err = w.Write(data)
	assert.NilError(t, err)
	assert.NilError(t, w.Close())
	return buf.Bytes()
}

func compressWithZstd(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	assert.NilError(t, err)
	_, err = w.Write(data)
	assert.NilError(t, err)
	assert.NilError(t, w.Close())
	return buf.Bytes()
}

// narinfo helpers

func makeNarinfoXz(narHash string, narSize int, fileHash string, fileSize int) string {
	return fmt.Sprintf(`StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/%s.nar.xz
Compression: xz
FileHash: sha256:%s
FileSize: %d
NarHash: sha256:%s
NarSize: %d
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: kappa:sn5s/RrqEI+YG6/PjwdbPjcAC7rcta7sJU4mFOawGvJBLsWkyLtBrT2EuFt/LJjWkTZ+ZWOI9NTtjo/woMdvAg==
`, narHash, fileHash, fileSize, narHash, narSize)
}

func makeNarinfoZstd(narHash string, narSize int, fileHash string, fileSize int) string {
	return fmt.Sprintf(`StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/%s.nar.zst
Compression: zstd
FileHash: sha256:%s
FileSize: %d
NarHash: sha256:%s
NarSize: %d
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: kappa:sn5s/RrqEI+YG6/PjwdbPjcAC7rcta7sJU4mFOawGvJBLsWkyLtBrT2EuFt/LJjWkTZ+ZWOI9NTtjo/woMdvAg==
`, narHash, fileHash, fileSize, narHash, narSize)
}

// parseNarinfoField extracts a single field value from a narinfo string.
func parseNarinfoField(narinfo, field string) string {
	for _, line := range strings.Split(narinfo, "\n") {
		if strings.HasPrefix(line, field+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, field+":"))
		}
	}
	return ""
}

// narinfoFieldEquals returns an apitest Assert function that checks a specific
// narinfo field has the expected value.
func narinfoFieldEquals(t *testing.T, field, expected string) func(*http.Response, *http.Request) error {
	return func(resp *http.Response, req *http.Request) error {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response body: %w", err)
		}
		// Reset body so subsequent assertions can also read it
		resp.Body = io.NopCloser(bytes.NewReader(body))

		actual := parseNarinfoField(string(body), field)
		if actual != expected {
			t.Errorf("narinfo field %s: got %q, want %q", field, actual, expected)
		}
		return nil
	}
}

// narinfoFieldAbsent returns an apitest Assert function that checks a specific
// narinfo field is not present in the response.
func narinfoFieldAbsent(t *testing.T, field string) func(*http.Response, *http.Request) error {
	return func(resp *http.Response, req *http.Request) error {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response body: %w", err)
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))

		actual := parseNarinfoField(string(body), field)
		if actual != "" {
			t.Errorf("narinfo field %s should be absent, but got %q", field, actual)
		}
		return nil
	}
}

// TestNarRejectsCompressedUpload verifies that compressed NAR uploads
// are rejected with 400, since spongix requires uncompressed NARs.
func TestNarRejectsCompressedUpload(t *testing.T) {
	narUncompressed := loadTestNar(t)

	t.Run("xz rejected", func(tt *testing.T) {
		proxy := testProxy(tt)
		narXz := compressWithXz(tt, narUncompressed)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarXz).
			Body(string(narXz)).
			Expect(tt).
			Status(http.StatusBadRequest).
			End()
	})

	t.Run("zstd rejected", func(tt *testing.T) {
		proxy := testProxy(tt)
		narZstd := compressWithZstd(tt, narUncompressed)

		nsNarZst := "/" + testNamespace + fNar + ".zst"

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarZst).
			Body(string(narZstd)).
			Expect(tt).
			Status(http.StatusBadRequest).
			End()
	})

	t.Run("uncompressed accepted", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNar).
			Body(string(narUncompressed)).
			Expect(tt).
			Status(http.StatusCreated).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNar).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNar).
			Body(string(narUncompressed)).
			End()
	})
}

// TestCompressedNarGetReturns404 verifies that GET requests for compressed
// NAR URLs (.nar.xz, .nar.zst) return 404, since spongix only stores
// uncompressed NARs.
func TestCompressedNarGetReturns404(t *testing.T) {
	proxy := testProxy(t)
	narUncompressed := loadTestNar(t)

	// Upload uncompressed NAR
	apitest.New().
		Handler(proxy.router()).
		Method(http.MethodPut).
		URL(nsNar).
		Body(string(narUncompressed)).
		Expect(t).
		Status(http.StatusCreated).
		End()

	// GET via .nar.xz should 404
	apitest.New().
		Handler(proxy.router()).
		Method(http.MethodGet).
		URL(nsNarXz).
		Expect(t).
		Status(http.StatusNotFound).
		End()
}

// TestNarinfoRewriteCompression verifies that narinfos with non-none
// Compression are rewritten on PUT: Compression -> none,
// FileHash -> NarHash, FileSize -> NarSize, URL extension stripped.
func TestNarinfoRewriteCompression(t *testing.T) {
	narHash := "1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp"

	t.Run("xz", func(tt *testing.T) {
		proxy := testProxy(tt)

		inputNarinfo := makeNarinfoXz(narHash, 512, "0rjb49w7dldq79d3ax30gw279bnbn48w9q925g3lr0rhrl0ycxvs", 220)

		// Upload narinfo with Compression: xz
		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarinfo).
			Body(inputNarinfo).
			Expect(tt).
			Status(http.StatusCreated).
			End()

		// Retrieve narinfo and verify rewritten fields
		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			Assert(narinfoFieldEquals(tt, "Compression", "none")).
			Assert(narinfoFieldEquals(tt, "FileHash", "sha256:"+narHash)).
			Assert(narinfoFieldEquals(tt, "FileSize", "512")).
			Assert(narinfoFieldEquals(tt, "NarHash", "sha256:"+narHash)).
			Assert(narinfoFieldEquals(tt, "NarSize", "512")).
			Assert(narinfoFieldEquals(tt, "URL", "nar/"+narHash+".nar")).
			End()
	})

	t.Run("zstd", func(tt *testing.T) {
		proxy := testProxy(tt)

		inputNarinfo := makeNarinfoZstd(narHash, 512, "0mn3yn1dh5l065jjrj7b3gc8sn3kbji32w16dwgcs6yqgada09jv", 192)

		// Upload narinfo with Compression: zstd
		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarinfo).
			Body(inputNarinfo).
			Expect(tt).
			Status(http.StatusCreated).
			End()

		// Retrieve and verify rewritten fields
		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			Assert(narinfoFieldEquals(tt, "Compression", "none")).
			Assert(narinfoFieldEquals(tt, "FileHash", "sha256:"+narHash)).
			Assert(narinfoFieldEquals(tt, "FileSize", "512")).
			Assert(narinfoFieldEquals(tt, "URL", "nar/"+narHash+".nar")).
			End()
	})

	t.Run("none unchanged", func(tt *testing.T) {
		proxy := testProxy(tt)

		// Upload narinfo with Compression: none -- should be stored as-is
		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarinfo).
			Body(fixtureNarinfoNone).
			Expect(tt).
			Status(http.StatusCreated).
			End()

		// Should be byte-for-byte identical on retrieval
		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			Body(fixtureNarinfoNone).
			End()
	})
}

// TestNarinfoSignatureStrippedOnRewrite verifies that upstream signatures
// are removed when the narinfo is rewritten (since changing fields
// invalidates the original signature).
func TestNarinfoSignatureStrippedOnRewrite(t *testing.T) {
	proxy := testProxy(t)

	narHash := "1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp"
	inputNarinfo := makeNarinfoXz(narHash, 512, "0rjb49w7dldq79d3ax30gw279bnbn48w9q925g3lr0rhrl0ycxvs", 220)

	apitest.New().
		Handler(proxy.router()).
		Method(http.MethodPut).
		URL(nsNarinfo).
		Body(inputNarinfo).
		Expect(t).
		Status(http.StatusCreated).
		End()

	apitest.New().
		Handler(proxy.router()).
		Method(http.MethodGet).
		URL(nsNarinfo).
		Expect(t).
		Status(http.StatusOK).
		Assert(narinfoFieldAbsent(t, "Sig")).
		End()
}
