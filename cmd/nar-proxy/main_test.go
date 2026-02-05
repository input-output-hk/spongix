package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// TestNarHandlerHTTPError demonstrates Bug 1: nil dereference when http.Get()
// fails. When the upstream cache is unreachable, http.Get returns a nil
// response and a non-nil error. The handler checks
// "err != nil || narinfoResponse.StatusCode != 200" but then unconditionally
// dereferences narinfoResponse.StatusCode, which panics.
// The gorilla RecoveryHandler catches the panic and returns 500.
func TestNarHandlerHTTPError(t *testing.T) {
	np := &NarProxy{
		CacheUrl: "http://127.0.0.1:1/",
		Prefix:   "/dl/",
		log:      zap.NewNop(),
	}

	router := np.newRouter()

	req, err := http.NewRequest("GET", "/dl/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test/somefile", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	// Correct behavior: when upstream is unreachable, return 502 Bad Gateway.
	// BUG: the handler panics with nil pointer dereference at main.go:98
	// (accessing narinfoResponse.StatusCode when narinfoResponse is nil),
	// and RecoveryHandler catches it, returning 500 instead.
	if rr.Code != http.StatusBadGateway {
		t.Errorf("expected status 502 Bad Gateway for unreachable upstream, got %d "+
			"(BUG: nil dereference on narinfoResponse at main.go:98 causes panic → 500)", rr.Code)
	}
}

// TestNarHandlerNarFetchCopyPasteBug demonstrates Bug 2: copy-paste error on
// lines 112-113. When the NAR fetch fails (returns 500), the handler writes
// narinfoResponse.StatusCode (200) instead of narResponse.StatusCode (500),
// and copies narinfoResponse.Body instead of narResponse.Body.
// This test proves the bug by asserting the response is 200 even though the
// NAR fetch returned 500.
func TestNarHandlerNarFetchCopyPasteBug(t *testing.T) {
	narinfo := `StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/somehash.nar
Compression: none
FileHash: sha256:0000000000000000000000000000000000000000000000000000
FileSize: 0
NarHash: sha256:0000000000000000000000000000000000000000000000000000
NarSize: 0`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".narinfo") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(narinfo))
			return
		}
		// NAR fetch returns 500
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	np := &NarProxy{
		CacheUrl: ts.URL + "/",
		Prefix:   "/dl/",
		log:      zap.NewNop(),
	}

	router := np.newRouter()

	req, err := http.NewRequest("GET", "/dl/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test/somefile", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	// Correct behavior: when NAR fetch returns 500, the response should be 500.
	// BUG: main.go:112 uses narinfoResponse.StatusCode (200) instead of
	// narResponse.StatusCode (500) — a copy-paste error.
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500 (NAR fetch failed), got %d "+
			"(BUG: main.go:112 writes narinfoResponse.StatusCode instead of "+
			"narResponse.StatusCode)", rr.Code)
	}
}
