package compress

import (
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func newTestHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	})
}

func TestCompressHandlerGzip(t *testing.T) {
	handler := CompressHandler(newTestHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected Content-Encoding %q, got %q", "gzip", got)
	}

	reader, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("failed to create gzip reader: %v", err)
	}
	defer func() { _ = reader.Close() }()

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read gzip body: %v", err)
	}

	if string(body) != "hello world" {
		t.Fatalf("expected body %q, got %q", "hello world", string(body))
	}
}

func TestCompressHandlerZstd(t *testing.T) {
	handler := CompressHandler(newTestHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "zstd")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("expected Content-Encoding %q, got %q", "zstd", got)
	}

	reader, err := zstd.NewReader(resp.Body)
	if err != nil {
		t.Fatalf("failed to create zstd reader: %v", err)
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read zstd body: %v", err)
	}

	if string(body) != "hello world" {
		t.Fatalf("expected body %q, got %q", "hello world", string(body))
	}
}

func TestCompressHandlerDeflate(t *testing.T) {
	handler := CompressHandler(newTestHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept-Encoding", "deflate")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Encoding"); got != "deflate" {
		t.Fatalf("expected Content-Encoding %q, got %q", "deflate", got)
	}

	reader := flate.NewReader(resp.Body)
	defer func() { _ = reader.Close() }()

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("failed to read deflate body: %v", err)
	}

	if string(body) != "hello world" {
		t.Fatalf("expected body %q, got %q", "hello world", string(body))
	}
}

func TestCompressHandlerNoEncoding(t *testing.T) {
	handler := CompressHandler(newTestHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("expected no Content-Encoding header, got %q", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read body: %v", err)
	}

	if string(body) != "hello world" {
		t.Fatalf("expected body %q, got %q", "hello world", string(body))
	}
}
