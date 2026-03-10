package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/alitto/pond"
	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/config"
	"github.com/input-output-hk/spongix/pkg/store"
	"github.com/steinfletcher/apitest"
	"go.uber.org/zap"
)

const testToken = "test-secret-token"

func testRegistryProxy(t *testing.T) *Proxy {
	t.Helper()
	db, err := store.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	proxy := &Proxy{
		config: &config.Config{
			Chunks: &config.Chunks{
				StoreType: "pebble",
				PebbleDir: t.TempDir(),
			},
			Namespaces: map[string]*config.Namespace{
				"cache": {
					Substituters: []string{},
					StoreType:    "pebble",
					Registry:     &config.Registry{Enable: true},
				},
			},
			PinAPI: &config.PinAPI{Enable: true, BearerToken: testToken},
		},
		log:            zap.NewNop(),
		headPool:       pond.New(1, 10),
		cachePool:      pond.New(1, 10),
		chunkStore:        newFakeStore(),
		indexStores:      map[string]desync.IndexWriteStore{"cache": newFakeIndex()},
		pebbleDB:       db,
		httpHeadClient: &http.Client{},
		httpGetClient:  &http.Client{},
		symlinkBinData: []byte("fake-symlink-binary"),
	}

	if err := proxy.config.Prepare(); err != nil {
		t.Fatal(err)
	}

	return proxy
}

func authHeader(token string) string {
	return "Bearer " + token
}

func TestRegistryPutAndGet(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-claude-code-nix-2.1.45","toPath":"/nix/store/xyz00000000000000000000000000002-claude-code-nix-2.1.45","mainProgram":"claude","system":"x86_64-linux"}`

	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/claude-code-nix/2.1.45").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusCreated).
		End()

	// GET metadata
	apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/claude-code-nix/2.1.45").
		Expect(t).
		Status(http.StatusOK).
		End()
}

func TestRegistryPutNoAuth(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`

	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusUnauthorized).
		End()
}

func TestRegistryPutNoPinAPI(t *testing.T) {
	// When PinAPI is not configured, PUT should succeed without auth.
	db, err := store.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	proxy := &Proxy{
		config: &config.Config{
			Chunks: &config.Chunks{
				StoreType: "pebble",
				PebbleDir: t.TempDir(),
			},
			Namespaces: map[string]*config.Namespace{
				"cache": {
					Substituters: []string{},
					StoreType:    "pebble",
					Registry:     &config.Registry{Enable: true},
				},
			},
			// No PinAPI configured
		},
		log:            zap.NewNop(),
		headPool:       pond.New(1, 10),
		cachePool:      pond.New(1, 10),
		chunkStore:     newFakeStore(),
		indexStores:    map[string]desync.IndexWriteStore{"cache": newFakeIndex()},
		pebbleDB:       db,
		httpHeadClient: &http.Client{},
		httpGetClient:  &http.Client{},
		symlinkBinData: []byte("fake-symlink-binary"),
	}
	if err := proxy.config.Prepare(); err != nil {
		t.Fatal(err)
	}

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`

	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusCreated).
		End()
}

func TestRegistryPutWrongToken(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`

	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader("wrong-token")).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusForbidden).
		End()
}

func TestRegistryPutInvalidStorePath(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"not-a-store-path","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`

	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusBadRequest).
		End()
}

func TestRegistryPutMissingSystem(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0"}`

	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusBadRequest).
		End()
}

func TestRegistryDelete(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`

	// Register
	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusCreated).
		End()

	// Delete
	apitest.New().
		Handler(proxy.router()).
		Delete("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Expect(t).
		Status(http.StatusNoContent).
		End()

	// Verify gone
	apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test/1.0.0").
		Expect(t).
		Status(http.StatusNotFound).
		End()
}

func TestRegistryListVersions(t *testing.T) {
	proxy := testRegistryProxy(t)

	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-` + v + `","toPath":"/nix/store/xyz00000000000000000000000000002-test-` + v + `","system":"x86_64-linux"}`
		apitest.New().
			Handler(proxy.router()).
			Put("/cache/f/test/"+v).
			Header("Authorization", authHeader(testToken)).
			Header("Content-Type", "application/json").
			Body(body).
			Expect(t).
			Status(http.StatusCreated).
			End()
	}

	result := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test").
		Expect(t).
		Status(http.StatusOK).
		End()

	var versions []string
	_ = json.NewDecoder(result.Response.Body).Decode(&versions)
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions, got %d: %v", len(versions), versions)
	}
}

func TestRegistryListPackages(t *testing.T) {
	proxy := testRegistryProxy(t)

	for _, name := range []string{"pkg-a", "pkg-b"} {
		body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-` + name + `","toPath":"/nix/store/xyz00000000000000000000000000002-` + name + `","system":"x86_64-linux"}`
		apitest.New().
			Handler(proxy.router()).
			Put("/cache/f/"+name+"/1.0.0").
			Header("Authorization", authHeader(testToken)).
			Header("Content-Type", "application/json").
			Body(body).
			Expect(t).
			Status(http.StatusCreated).
			End()
	}

	result := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f").
		Expect(t).
		Status(http.StatusOK).
		End()

	var packages []string
	_ = json.NewDecoder(result.Response.Body).Decode(&packages)
	if len(packages) != 2 {
		t.Fatalf("expected 2 packages, got %d: %v", len(packages), packages)
	}
}

func TestRegistryFlakeTarball(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","mainProgram":"test-bin","system":"x86_64-linux"}`
	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusCreated).
		End()

	result := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test/1.0.0.tar.gz").
		Expect(t).
		Status(http.StatusOK).
		Header("x-nix-is-immutable", "1").
		End()

	// Verify Link header
	linkHeader := result.Response.Header.Get("Link")
	if linkHeader == "" {
		t.Fatal("expected Link header")
	}

	// Verify ETag header
	etag := result.Response.Header.Get("ETag")
	if etag == "" {
		t.Fatal("expected ETag header")
	}

	// Verify tarball is valid gzip and contains source/flake.nix + source/bin/symlink
	tarData, _ := io.ReadAll(result.Response.Body)
	gr, err := gzip.NewReader(bytes.NewReader(tarData))
	if err != nil {
		t.Fatalf("tarball is not valid gzip: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	foundFlake := false
	foundSymlink := false
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		switch hdr.Name {
		case "source/flake.nix":
			foundFlake = true
			content, _ := io.ReadAll(tr)
			if !bytes.Contains(content, []byte("fetchClosure")) {
				t.Fatal("flake.nix missing fetchClosure")
			}
			if !bytes.Contains(content, []byte("test-bin")) {
				t.Fatal("flake.nix missing mainProgram")
			}
			if !bytes.Contains(content, []byte(`${self}/bin/symlink`)) {
				t.Fatal("flake.nix missing self-referencing symlink builder")
			}
		case "source/bin/symlink":
			foundSymlink = true
			if hdr.Mode != 0o755 {
				t.Fatalf("symlink binary should be executable, got mode %o", hdr.Mode)
			}
		}
	}
	if !foundFlake {
		t.Fatal("tarball missing source/flake.nix")
	}
	if !foundSymlink {
		t.Fatal("tarball missing source/bin/symlink")
	}
}

func TestRegistryFlakeLatestRedirect(t *testing.T) {
	proxy := testRegistryProxy(t)

	for _, v := range []string{"1.0.0", "2.0.0", "1.5.0"} {
		body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-` + v + `","toPath":"/nix/store/xyz00000000000000000000000000002-test-` + v + `","system":"x86_64-linux"}`
		apitest.New().
			Handler(proxy.router()).
			Put("/cache/f/test/"+v).
			Header("Authorization", authHeader(testToken)).
			Header("Content-Type", "application/json").
			Body(body).
			Expect(t).
			Status(http.StatusCreated).
			End()
	}

	result := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test/*.tar.gz").
		Expect(t).
		Status(http.StatusTemporaryRedirect).
		End()

	loc := result.Response.Header.Get("Location")
	expected := "/cache/f/test/2.0.0.tar.gz"
	if loc != expected {
		t.Fatalf("expected redirect to %s, got %s", expected, loc)
	}
}

func TestRegistryFlakeTarballNotFound(t *testing.T) {
	proxy := testRegistryProxy(t)

	apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/nonexistent/1.0.0.tar.gz").
		Expect(t).
		Status(http.StatusNotFound).
		End()
}

func TestRegistryFlakeLatestNotFound(t *testing.T) {
	proxy := testRegistryProxy(t)

	apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/nonexistent/*.tar.gz").
		Expect(t).
		Status(http.StatusNotFound).
		End()
}

func TestRegistryETagConditional(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`
	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusCreated).
		End()

	// First GET to obtain ETag
	result := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test/1.0.0.tar.gz").
		Expect(t).
		Status(http.StatusOK).
		End()

	etag := result.Response.Header.Get("ETag")

	// Conditional GET with matching ETag
	apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test/1.0.0.tar.gz").
		Header("If-None-Match", etag).
		Expect(t).
		Status(http.StatusNotModified).
		End()
}

func TestRegistryMultiSystem(t *testing.T) {
	proxy := testRegistryProxy(t)

	for _, sys := range []string{"x86_64-linux", "aarch64-linux"} {
		body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"` + sys + `"}`
		apitest.New().
			Handler(proxy.router()).
			Put("/cache/f/test/1.0.0").
			Header("Authorization", authHeader(testToken)).
			Header("Content-Type", "application/json").
			Body(body).
			Expect(t).
			Status(http.StatusCreated).
			End()
	}

	// GET metadata should return 2 entries
	result := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test/1.0.0").
		Expect(t).
		Status(http.StatusOK).
		End()

	var entries []map[string]any
	_ = json.NewDecoder(result.Response.Body).Decode(&entries)
	if len(entries) != 2 {
		t.Fatalf("expected 2 system entries, got %d", len(entries))
	}

	// Tarball should contain both systems in flake.nix
	tarResult := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/test/1.0.0.tar.gz").
		Expect(t).
		Status(http.StatusOK).
		End()

	tarData, _ := io.ReadAll(tarResult.Response.Body)
	gr, err := gzip.NewReader(bytes.NewReader(tarData))
	if err != nil {
		t.Fatalf("tarball is not valid gzip: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == "source/flake.nix" {
			content, _ := io.ReadAll(tr)
			if !bytes.Contains(content, []byte("x86_64-linux")) {
				t.Fatal("flake.nix missing x86_64-linux")
			}
			if !bytes.Contains(content, []byte("aarch64-linux")) {
				t.Fatal("flake.nix missing aarch64-linux")
			}
		}
	}
}

func TestRegistryFlakeNamedOutput(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-helix-25.1.1","toPath":"/nix/store/xyz00000000000000000000000000002-helix-25.1.1","mainProgram":"hx","system":"x86_64-linux"}`
	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/helix/25.1.1").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusCreated).
		End()

	result := apitest.New().
		Handler(proxy.router()).
		Get("/cache/f/helix/25.1.1.tar.gz").
		Expect(t).
		Status(http.StatusOK).
		End()

	tarData, _ := io.ReadAll(result.Response.Body)
	gr, err := gzip.NewReader(bytes.NewReader(tarData))
	if err != nil {
		t.Fatalf("tarball is not valid gzip: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == "source/flake.nix" {
			content, _ := io.ReadAll(tr)
			if !bytes.Contains(content, []byte("packages.x86_64-linux.default")) {
				t.Fatal("flake.nix missing packages.x86_64-linux.default")
			}
			if !bytes.Contains(content, []byte("packages.x86_64-linux.helix")) {
				t.Fatal("flake.nix missing packages.x86_64-linux.helix named output")
			}
			if !bytes.Contains(content, []byte("packages.x86_64-linux.helix = self.packages.x86_64-linux.default")) {
				t.Fatal("flake.nix named output should alias default")
			}
			return
		}
	}
	t.Fatal("tarball missing source/flake.nix")
}

func TestRegistryAutoPin(t *testing.T) {
	proxy := testRegistryProxy(t)

	body := `{"fromPath":"/nix/store/abc00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`
	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(body).
		Expect(t).
		Status(http.StatusCreated).
		End()

	// Verify the fromPath hash was auto-pinned with label.
	pinned, err := proxy.pebbleDB.IsPinned(store.StorePathHash32("abc00000000000000000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Fatal("expected fromPath hash to be auto-pinned")
	}

	// Verify the pin has the expected label.
	pins, err := proxy.pebbleDB.ListPins()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(pins))
	}
	expectedLabel := "test@1.0.0"
	if pins[0].Label != expectedLabel {
		t.Fatalf("expected label %q, got %q", expectedLabel, pins[0].Label)
	}
}

func TestRegistryPutOverwriteUnpinsOld(t *testing.T) {
	proxy := testRegistryProxy(t)

	// Register first entry.
	bodyA := `{"fromPath":"/nix/store/aaa00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/xxx00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`
	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(bodyA).
		Expect(t).
		Status(http.StatusCreated).
		End()

	// Verify old hash is pinned.
	pinned, err := proxy.pebbleDB.IsPinned(store.StorePathHash32("aaa00000000000000000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Fatal("expected old fromPath hash to be pinned")
	}

	// Overwrite with different fromPath.
	bodyB := `{"fromPath":"/nix/store/bbb00000000000000000000000000001-test-1.0.0","toPath":"/nix/store/yyy00000000000000000000000000002-test-1.0.0","system":"x86_64-linux"}`
	apitest.New().
		Handler(proxy.router()).
		Put("/cache/f/test/1.0.0").
		Header("Authorization", authHeader(testToken)).
		Header("Content-Type", "application/json").
		Body(bodyB).
		Expect(t).
		Status(http.StatusCreated).
		End()

	// Old hash should be unpinned.
	pinned, err = proxy.pebbleDB.IsPinned(store.StorePathHash32("aaa00000000000000000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if pinned {
		t.Fatal("expected old fromPath hash to be unpinned after overwrite")
	}

	// New hash should be pinned.
	pinned, err = proxy.pebbleDB.IsPinned(store.StorePathHash32("bbb00000000000000000000000000001"))
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Fatal("expected new fromPath hash to be pinned")
	}

	// Only one pin should remain.
	pins, err := proxy.pebbleDB.ListPins()
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 {
		t.Fatalf("expected 1 pin, got %d", len(pins))
	}
}
