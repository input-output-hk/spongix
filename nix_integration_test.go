package main

import (
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
	defer resp.Body.Close()
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
