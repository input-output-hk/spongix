package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNixCopyRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("nix-store"); err != nil {
		t.Skip("nix-store not in PATH, skipping integration test")
	}

	proxy := testProxy(t)
	srv := httptest.NewServer(proxy.router())
	defer srv.Close()

	// Use a local chroot-style store: nix/store lives under root,
	// so store paths are /nix/store/... matching the binary cache StoreDir.
	root := t.TempDir()
	storeSpec := "local?root=" + root

	nixConf := filepath.Join(root, "etc", "nix")
	if err := os.MkdirAll(nixConf, 0o755); err != nil {
		t.Fatal(err)
	}

	confFile := filepath.Join(nixConf, "nix.conf")
	conf := "sandbox = false\nbuild-users-group =\nsubstituters =\nexperimental-features = nix-command\nnarinfo-cache-positive-ttl = 0\nnarinfo-cache-negative-ttl = 0\n"
	if err := os.WriteFile(confFile, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	// Use isolated XDG_CACHE_HOME dirs so nix doesn't reuse narinfos
	// cached by the push phase when fetching (narinfos are rewritten
	// on ingest to strip compression).
	pushCacheHome := t.TempDir()
	pullCacheHome := t.TempDir()
	activeCacheHome := &pushCacheHome

	runNix := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "NIX_CONF_DIR="+nixConf, "XDG_CACHE_HOME="+*activeCacheHome)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s %v failed:\n%s", name, args, out)
		}
		return string(out), err
	}

	// Create a test file and add it to the local store
	testContent := "hello from spongix integration test\n"
	testFile := filepath.Join(root, "test-file")
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatal(err)
	}

	addOut, err := runNix("nix-store", "--store", storeSpec, "--add", testFile)
	if err != nil {
		t.Fatal("nix-store --add:", err)
	}
	storePath := strings.TrimSpace(addOut)
	t.Logf("store path: %s", storePath)

	// Push to spongix (compression=none required -- spongix stores NARs uncompressed)
	cacheURL := srv.URL + "/" + testNamespace
	if _, err := runNix("nix", "copy", "--store", storeSpec, "--to", cacheURL+"?compression=none", storePath); err != nil {
		t.Fatal("nix copy --to:", err)
	}

	// Verify the narinfo was rewritten (URL should not have .xz)
	// storePath is like /nix/store/25hq...-name, extract the hash before the first dash
	hash := strings.TrimPrefix(storePath, "/nix/store/")
	if dashIdx := strings.IndexByte(hash, '-'); dashIdx > 0 {
		hash = hash[:dashIdx]
	}
	narinfoURL := cacheURL + "/" + hash + ".narinfo"
	t.Logf("fetching narinfo at: %s", narinfoURL)
	resp, err := http.Get(narinfoURL)
	if err != nil {
		t.Fatal("fetching narinfo:", err)
	}
	defer func() { _ = resp.Body.Close() }()
	narinfoBody, _ := io.ReadAll(resp.Body)
	t.Logf("narinfo status=%d body:\n%s", resp.StatusCode, narinfoBody)

	// Use a second clean local store and separate nix cache for fetch
	root2 := t.TempDir()
	storeSpec2 := "local?root=" + root2
	activeCacheHome = &pullCacheHome

	// Fetch back from spongix
	if _, err := runNix("nix", "copy", "--store", storeSpec2, "--from", cacheURL, "--no-check-sigs", storePath); err != nil {
		t.Fatal("nix copy --from:", err)
	}

	// Verify the file exists and has correct content.
	// With local?root=<dir>, the store path /nix/store/xxx lives at <dir>/nix/store/xxx.
	restoredFile := filepath.Join(root2, storePath)
	content, err := os.ReadFile(restoredFile)
	if err != nil {
		t.Fatal("reading restored file:", err)
	}
	if string(content) != testContent {
		t.Errorf("content mismatch:\n  got:  %q\n  want: %q", string(content), testContent)
	}
}

// TestNixFetchCompressedUpstream verifies that Spongix can serve a NAR
// whose upstream only provides compressed content. This is the real-world
// scenario: cache.nixos.org serves .nar.xz, Spongix rewrites the narinfo
// to Compression: none, then proxies + decompresses the NAR on the fly.
func TestNixFetchCompressedUpstream(t *testing.T) {
	if _, err := exec.LookPath("nix-store"); err != nil {
		t.Skip("nix-store not in PATH, skipping integration test")
	}
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz not in PATH, skipping integration test")
	}

	// --- Phase 1: Create a real store path and extract its NAR + narinfo ---

	root := t.TempDir()
	storeSpec := "local?root=" + root
	nixConf := filepath.Join(root, "etc", "nix")
	if err := os.MkdirAll(nixConf, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := "sandbox = false\nbuild-users-group =\nsubstituters =\nexperimental-features = nix-command\nnarinfo-cache-positive-ttl = 0\nnarinfo-cache-negative-ttl = 0\n"
	if err := os.WriteFile(filepath.Join(nixConf, "nix.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	cacheHome := t.TempDir()
	runNix := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "NIX_CONF_DIR="+nixConf, "XDG_CACHE_HOME="+cacheHome)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s %v failed:\n%s", name, args, out)
		}
		return string(out), err
	}

	testContent := "hello from compressed upstream test\n"
	testFile := filepath.Join(root, "test-file")
	if err := os.WriteFile(testFile, []byte(testContent), 0o644); err != nil {
		t.Fatal(err)
	}

	addOut, err := runNix("nix-store", "--store", storeSpec, "--add", testFile)
	if err != nil {
		t.Fatal("nix-store --add:", err)
	}
	storePath := strings.TrimSpace(addOut)
	t.Logf("store path: %s", storePath)

	// Dump the NAR (--dump takes a filesystem path, not a store path)
	fsPath := filepath.Join(root, storePath)
	dumpCmd := exec.Command("nix-store", "--dump", fsPath)
	narBytes, err := dumpCmd.Output()
	if err != nil {
		t.Fatal("nix-store --dump:", err)
	}
	t.Logf("NAR size: %d bytes", len(narBytes))

	// Compute NAR hash (sha256, nix-base32)
	narSha := sha256.Sum256(narBytes)
	narHashHex := fmt.Sprintf("%x", narSha)
	narHashNix32, err := runNix("nix", "hash", "convert", "--hash-algo", "sha256", "--to", "nix32", narHashHex)
	if err != nil {
		t.Fatal("nix hash convert:", err)
	}
	narHashNix32 = strings.TrimSpace(narHashNix32)

	// Compress the NAR with xz
	xzCmd := exec.Command("xz", "--compress", "--stdout")
	xzCmd.Stdin = bytes.NewReader(narBytes)
	xzOut, err := xzCmd.Output()
	if err != nil {
		t.Fatal("xz compress:", err)
	}
	narXzBytes := xzOut
	t.Logf("NAR.xz size: %d bytes", len(narXzBytes))

	// Compute file hash for the xz-compressed NAR
	fileSha := sha256.Sum256(narXzBytes)
	fileHashHex := fmt.Sprintf("%x", fileSha)
	fileHashNix32, err := runNix("nix", "hash", "convert", "--hash-algo", "sha256", "--to", "nix32", fileHashHex)
	if err != nil {
		t.Fatal("nix hash convert (file):", err)
	}
	fileHashNix32 = strings.TrimSpace(fileHashNix32)

	// Build the narinfo as an upstream would serve it (compressed)
	narURL := fmt.Sprintf("nar/%s.nar.xz", narHashNix32)
	narinfoContent := fmt.Sprintf("StorePath: %s\nURL: %s\nCompression: xz\nFileHash: sha256:%s\nFileSize: %d\nNarHash: sha256:%s\nNarSize: %d\nReferences: \n",
		storePath, narURL, fileHashNix32, len(narXzBytes), narHashNix32, len(narBytes))

	t.Logf("narinfo:\n%s", narinfoContent)

	// --- Phase 2: Start fake upstream serving compressed content ---

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("upstream: %s %s", r.Method, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, ".narinfo"):
			w.Header().Set("Content-Type", mimeNarinfo)
			_, _ = w.Write([]byte(narinfoContent))
		case strings.HasSuffix(r.URL.Path, ".nar.xz"):
			w.Header().Set("Content-Type", "application/x-xz")
			_, _ = w.Write(narXzBytes)
		case strings.HasSuffix(r.URL.Path, ".nar"):
			// Upstream does NOT have the uncompressed NAR.
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "nix-cache-info"):
			w.Header().Set("Content-Type", mimeNixCacheInfo)
			_, _ = w.Write([]byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	// --- Phase 3: Start Spongix with the fake upstream ---

	proxy := testProxy(t)
	proxy.config.Namespaces[testNamespace].Substituters = []string{upstream.URL}
	spongix := httptest.NewServer(proxy.router())
	defer spongix.Close()

	cacheURL := spongix.URL + "/" + testNamespace

	// --- Phase 4: Fetch through Spongix using nix copy ---

	fetchRoot := t.TempDir()
	fetchStore := "local?root=" + fetchRoot
	fetchCache := t.TempDir()

	fetchNix := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "NIX_CONF_DIR="+nixConf, "XDG_CACHE_HOME="+fetchCache)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s %v failed:\n%s", name, args, out)
		}
		return string(out), err
	}

	if _, err := fetchNix("nix", "copy", "--store", fetchStore, "--from", cacheURL, "--no-check-sigs", storePath); err != nil {
		t.Fatal("nix copy --from (through spongix):", err)
	}

	// Verify the file exists and has correct content
	restoredFile := filepath.Join(fetchRoot, storePath)
	content, err := os.ReadFile(restoredFile)
	if err != nil {
		t.Fatal("reading restored file:", err)
	}
	if string(content) != testContent {
		t.Errorf("content mismatch:\n  got:  %q\n  want: %q", string(content), testContent)
	}

	// --- Phase 5: Fetch again -- should come from Spongix's local cache ---

	fetchRoot2 := t.TempDir()
	fetchStore2 := "local?root=" + fetchRoot2
	fetchCache2 := t.TempDir()

	// Shut down the upstream to prove the second fetch uses local cache.
	upstream.Close()

	fetchNix2 := func(name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "NIX_CONF_DIR="+nixConf, "XDG_CACHE_HOME="+fetchCache2)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%s %v failed:\n%s", name, args, out)
		}
		return string(out), err
	}

	if _, err := fetchNix2("nix", "copy", "--store", fetchStore2, "--from", cacheURL, "--no-check-sigs", storePath); err != nil {
		t.Fatal("nix copy --from (cached, upstream down):", err)
	}

	content2, err := os.ReadFile(filepath.Join(fetchRoot2, storePath))
	if err != nil {
		t.Fatal("reading restored file (cached):", err)
	}
	if string(content2) != testContent {
		t.Errorf("content mismatch (cached):\n  got:  %q\n  want: %q", string(content2), testContent)
	}
}

// TestRegistryTarballNixFetch verifies that Nix can actually fetch and parse a
// registry tarball via the Lockable HTTP Tarball Protocol. This catches bugs
// like serving uncompressed tar with a .tar.gz URL (Nix will try to gunzip and fail).
func TestRegistryTarballNixFetch(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH, skipping integration test")
	}

	proxy := testRegistryProxy(t)
	srv := httptest.NewServer(proxy.router())
	defer srv.Close()

	// Register a package with fake store paths.
	regBody := `{"fromPath":"/nix/store/abc00000000000000000000000000001-hello-1.0.0","toPath":"/nix/store/xyz00000000000000000000000000002-hello-1.0.0","mainProgram":"hello","system":"x86_64-linux"}`
	req, _ := http.NewRequest("PUT", srv.URL+"/cache/f/hello/1.0.0",
		strings.NewReader(regBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("registering package:", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT returned %d, want 201", resp.StatusCode)
	}

	// Verify the tarball is valid gzip at the HTTP level.
	tarballURL := srv.URL + "/cache/f/hello/1.0.0.tar.gz"
	tarResp, err := http.Get(tarballURL)
	if err != nil {
		t.Fatal("fetching tarball:", err)
	}
	defer tarResp.Body.Close()
	if tarResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tarResp.Body)
		t.Fatalf("tarball returned %d: %s", tarResp.StatusCode, body)
	}

	// Check immutable headers are present.
	if tarResp.Header.Get("x-nix-is-immutable") != "1" {
		t.Error("missing x-nix-is-immutable header")
	}
	if tarResp.Header.Get("Link") == "" {
		t.Error("missing Link header")
	}
	if tarResp.Header.Get("ETag") == "" {
		t.Error("missing ETag header")
	}

	// Use nix flake metadata to validate the full tarball protocol:
	// HTTP fetch -> gzip decompress -> tar extract -> find flake.nix -> parse.
	// This is the exact code path a real `nix flake lock` uses.
	nixCache := t.TempDir()
	cmd := exec.Command("nix", "flake", "metadata", "--json",
		"tarball+"+tarballURL)
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+nixCache)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("nix flake metadata failed (tarball protocol broken):\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}
	out := stdout.Bytes()

	// Parse the JSON output to verify Nix saw the flake.
	var metadata struct {
		Locked struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"locked"`
	}
	if err := json.Unmarshal(out, &metadata); err != nil {
		t.Fatalf("parsing nix flake metadata output: %v\nraw: %s", err, out)
	}
	t.Logf("nix flake metadata: locked type=%s url=%s", metadata.Locked.Type, metadata.Locked.URL)

	if metadata.Locked.Type != "tarball" {
		t.Errorf("expected locked type 'tarball', got %q", metadata.Locked.Type)
	}

	// Test the * redirect: GET *.tar.gz should 307 to versioned URL.
	latestURL := srv.URL + "/cache/f/hello/*.tar.gz"
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse // don't follow redirects
	}}
	latestResp, err := client.Get(latestURL)
	if err != nil {
		t.Fatal("fetching latest:", err)
	}
	latestResp.Body.Close()
	if latestResp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("*.tar.gz returned %d, want 307", latestResp.StatusCode)
	}
	loc := latestResp.Header.Get("Location")
	if !strings.HasSuffix(loc, "/cache/f/hello/1.0.0.tar.gz") {
		t.Errorf("redirect location %q doesn't point to versioned tarball", loc)
	}
}

// TestRegistryFlakeAttributes verifies that nix eval on a generated registry
// tarball produces a derivation with version, passthru.shellPath, and
// meta.mainProgram — the attributes NixOS types.shellPackage requires.
func TestRegistryFlakeAttributes(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH, skipping integration test")
	}

	proxy := testRegistryProxy(t)
	srv := httptest.NewServer(proxy.router())
	defer srv.Close()

	// Register a package with mainProgram set.
	regBody := `{"fromPath":"/nix/store/abc00000000000000000000000000001-nushell-0.110.1","toPath":"/nix/store/xyz00000000000000000000000000002-nushell-0.110.1","mainProgram":"nu","system":"x86_64-linux"}`
	req, _ := http.NewRequest("PUT", srv.URL+"/cache/f/nushell/0.110.1",
		strings.NewReader(regBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("registering package:", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT returned %d, want 201", resp.StatusCode)
	}

	// Fetch the tarball and extract to a temp dir.
	tarballURL := srv.URL + "/cache/f/nushell/0.110.1.tar.gz"
	tarResp, err := http.Get(tarballURL)
	if err != nil {
		t.Fatal("fetching tarball:", err)
	}
	defer tarResp.Body.Close()
	tarData, _ := io.ReadAll(tarResp.Body)

	flakeDir := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(flakeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(tarData))
	if err != nil {
		t.Fatalf("invalid gzip: %v", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		// Strip the "source/" prefix from tar entries.
		name := strings.TrimPrefix(hdr.Name, "source/")
		if name == "" || name == "source" {
			continue
		}
		target := filepath.Join(flakeDir, name)
		if hdr.Typeflag == tar.TypeDir {
			os.MkdirAll(target, 0o755)
			continue
		}
		os.MkdirAll(filepath.Dir(target), 0o755)
		data, _ := io.ReadAll(tr)
		os.WriteFile(target, data, os.FileMode(hdr.Mode))
	}

	// Initialize git repo (required for nix flake eval).
	gitEnv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	for _, args := range [][]string{
		{"init"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-m", "test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = flakeDir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}

	// Evaluate the flake and check attribute presence.
	nixCache := t.TempDir()
	cmd := exec.Command("nix", "eval", "--json",
		"path:"+flakeDir+"#packages.x86_64-linux.default",
		"--apply", `pkg: {
			hasPassthru = pkg ? passthru;
			hasShellPath = (pkg.passthru or {}) ? shellPath;
			shellPath = (pkg.passthru or {}).shellPath or "MISSING";
			hasTopLevelShellPath = pkg ? shellPath;
			topLevelShellPath = pkg.shellPath or "MISSING";
			hasVersion = pkg ? version;
			version = pkg.version or "MISSING";
			hasMainProgram = pkg ? mainProgram;
			mainProgram = pkg.mainProgram or "MISSING";
			hasMeta = pkg ? meta;
			type = pkg.type or "MISSING";
			attrNames = builtins.attrNames pkg;
		}`)
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+nixCache)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("nix eval failed:\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}

	var result struct {
		HasPassthru          bool     `json:"hasPassthru"`
		HasShellPath         bool     `json:"hasShellPath"`
		ShellPath            string   `json:"shellPath"`
		HasTopLevelShellPath bool     `json:"hasTopLevelShellPath"`
		TopLevelShellPath    string   `json:"topLevelShellPath"`
		HasVersion           bool     `json:"hasVersion"`
		Version              string   `json:"version"`
		HasMainProgram       bool     `json:"hasMainProgram"`
		MainProgram          string   `json:"mainProgram"`
		HasMeta              bool     `json:"hasMeta"`
		Type                 string   `json:"type"`
		AttrNames            []string `json:"attrNames"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parsing nix eval output: %v\nraw: %s", err, stdout.String())
	}

	if result.Type != "derivation" {
		t.Errorf("expected type 'derivation', got %q", result.Type)
	}
	if !result.HasVersion {
		t.Errorf("missing version attribute; attrNames: %v", result.AttrNames)
	} else if result.Version != "0.110.1" {
		t.Errorf("version = %q, want %q", result.Version, "0.110.1")
	}
	if !result.HasPassthru {
		t.Errorf("missing passthru attribute; attrNames: %v", result.AttrNames)
	}
	if !result.HasShellPath {
		t.Errorf("missing passthru.shellPath; attrNames: %v", result.AttrNames)
	} else if result.ShellPath != "/bin/nu" {
		t.Errorf("passthru.shellPath = %q, want %q", result.ShellPath, "/bin/nu")
	}
	// types.shellPackage.check requires shellPath as a top-level derivation attribute.
	if !result.HasTopLevelShellPath {
		t.Errorf("missing top-level shellPath (required by types.shellPackage.check); attrNames: %v", result.AttrNames)
	} else if result.TopLevelShellPath != "/bin/nu" {
		t.Errorf("top-level shellPath = %q, want %q", result.TopLevelShellPath, "/bin/nu")
	}
	if !result.HasMainProgram {
		t.Errorf("missing mainProgram attribute")
	} else if result.MainProgram != "nu" {
		t.Errorf("mainProgram = %q, want %q", result.MainProgram, "nu")
	}
	if !result.HasMeta {
		t.Errorf("missing meta attribute")
	}
}

// TestRegistryFlakeAttributesViaTarball is the same as TestRegistryFlakeAttributes
// but evaluates via the tarball URL (the production code path) rather than extracting
// to a local directory. This catches issues with Nix's tarball fetcher/caching.
func TestRegistryFlakeAttributesViaTarball(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not in PATH, skipping integration test")
	}

	proxy := testRegistryProxy(t)
	srv := httptest.NewServer(proxy.router())
	defer srv.Close()

	regBody := `{"fromPath":"/nix/store/abc00000000000000000000000000001-nushell-0.110.1","toPath":"/nix/store/xyz00000000000000000000000000002-nushell-0.110.1","mainProgram":"nu","system":"x86_64-linux"}`
	req, _ := http.NewRequest("PUT", srv.URL+"/cache/f/nushell/0.110.1",
		strings.NewReader(regBody))
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("registering package:", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT returned %d, want 201", resp.StatusCode)
	}

	// Evaluate via the tarball URL — this is how nix flake lock works.
	tarballURL := srv.URL + "/cache/f/nushell/0.110.1.tar.gz"
	nixCache := t.TempDir()
	cmd := exec.Command("nix", "eval", "--json", "--impure",
		"--expr", fmt.Sprintf(`
			let
				flake = builtins.getFlake "tarball+%s";
				pkg = flake.packages.x86_64-linux.default;
			in {
				hasPassthru = pkg ? passthru;
				hasShellPath = (pkg.passthru or {}) ? shellPath;
				shellPath = (pkg.passthru or {}).shellPath or "MISSING";
				hasVersion = pkg ? version;
				version = pkg.version or "MISSING";
				mainProgram = pkg.mainProgram or "MISSING";
				type = pkg.type or "MISSING";
				attrNames = builtins.attrNames pkg;
			}
		`, tarballURL))
	cmd.Env = append(os.Environ(), "XDG_CACHE_HOME="+nixCache)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("nix eval failed:\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
	}

	var result struct {
		HasPassthru  bool     `json:"hasPassthru"`
		HasShellPath bool     `json:"hasShellPath"`
		ShellPath    string   `json:"shellPath"`
		HasVersion   bool     `json:"hasVersion"`
		Version      string   `json:"version"`
		MainProgram  string   `json:"mainProgram"`
		Type         string   `json:"type"`
		AttrNames    []string `json:"attrNames"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parsing nix eval output: %v\nraw: %s", err, stdout.String())
	}

	if result.Type != "derivation" {
		t.Errorf("expected type 'derivation', got %q", result.Type)
	}
	if !result.HasVersion {
		t.Errorf("missing version attribute; attrNames: %v", result.AttrNames)
	} else if result.Version != "0.110.1" {
		t.Errorf("version = %q, want %q", result.Version, "0.110.1")
	}
	if !result.HasPassthru {
		t.Errorf("missing passthru attribute; attrNames: %v", result.AttrNames)
	}
	if !result.HasShellPath {
		t.Errorf("missing passthru.shellPath; attrNames: %v", result.AttrNames)
	} else if result.ShellPath != "/bin/nu" {
		t.Errorf("passthru.shellPath = %q, want %q", result.ShellPath, "/bin/nu")
	}
}

func TestCapushPushIntegration(t *testing.T) {
	if _, err := exec.LookPath("nu"); err != nil {
		t.Skip("nu not in PATH, skipping capush integration test")
	}
	if _, err := exec.LookPath("nix-store"); err != nil {
		t.Skip("nix-store not in PATH, skipping integration test")
	}

	proxy := testRegistryProxy(t)
	srv := httptest.NewServer(proxy.router())
	defer srv.Close()

	// Use a local chroot store so the test is fully isolated from the
	// real /nix/store. The nix.conf `store` setting redirects all nix
	// commands (build, store make-content-addressed, copy) to the chroot.
	root := t.TempDir()
	storeSpec := "local?root=" + root
	nixConf := filepath.Join(root, "etc", "nix")
	if err := os.MkdirAll(nixConf, 0o755); err != nil {
		t.Fatal(err)
	}
	conf := fmt.Sprintf("store = %s\nsandbox = false\nbuild-users-group =\nsubstituters =\nexperimental-features = nix-command\n", storeSpec)
	if err := os.WriteFile(filepath.Join(nixConf, "nix.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add a test file to the chroot store
	testFile := filepath.Join(root, "test-file")
	if err := os.WriteFile(testFile, []byte("capush test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	addCmd := exec.Command("nix-store", "--store", storeSpec, "--add", testFile)
	addCmd.Env = append(os.Environ(), "NIX_CONF_DIR="+nixConf)
	addOut, err := addCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("nix-store --add: %s", addOut)
	}
	storePath := strings.TrimSpace(string(addOut))
	t.Logf("store path: %s", storePath)

	// Run capush push — NIX_CONF_DIR makes all nix commands use the chroot
	cacheURL := srv.URL + "/cache"
	cmd := exec.Command("./scripts/capush.nu", "push", storePath,
		"--store", cacheURL,
		"--name", "test-pkg",
		"--version", "1.0.0",
		"--main-program", "test-bin",
		"--token", testToken)
	cmd.Env = append(os.Environ(), "NIX_CONF_DIR="+nixConf)
	out, err := cmd.CombinedOutput()
	t.Logf("capush output:\n%s", out)
	if err != nil {
		t.Fatalf("capush push failed: %v", err)
	}

	// Verify registry entry was created
	resp, err := http.Get(cacheURL + "/f/test-pkg/1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	// Verify tarball is served
	tarResp, err := http.Get(cacheURL + "/f/test-pkg/1.0.0.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer tarResp.Body.Close()
	if tarResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(tarResp.Body)
		t.Fatalf("tarball: expected 200, got %d: %s", tarResp.StatusCode, body)
	}
	if tarResp.Header.Get("x-nix-is-immutable") != "1" {
		t.Error("tarball missing x-nix-is-immutable header")
	}
}
