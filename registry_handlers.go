package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"go.uber.org/zap"
)

// registryPut handles PUT /{ns}/f/{owner}/{name}/{version}.
func (p *Proxy) registryPut(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	owner := vars["owner"]
	pkg := vars["pkg"]
	version := vars["version"]

	var req RegistryPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		answer(w, http.StatusBadRequest, mimeText, "invalid JSON: "+err.Error())
		return
	}

	// Default fromStore to this Spongix instance's URL.
	if req.FromStore == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		req.FromStore = scheme + "://" + r.Host + "/" + namespace
	}

	entry, err := ValidateRegistryPut(owner, pkg, version, req)
	if err != nil {
		answer(w, http.StatusBadRequest, mimeText, err.Error())
		return
	}

	if err := p.pebbleDB.PutRegistryEntry(namespace, entry); err != nil {
		p.log.Error("storing registry entry", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}

	// Auto-pin the fromPath hash to prevent GC.
	fromHash := StorePathHash(entry.FromPath)
	if fromHash != "" {
		if err := p.pebbleDB.PinHash(fromHash); err != nil {
			p.log.Error("auto-pinning registry entry", zap.String("hash", fromHash), zap.Error(err))
		}
	}

	metricRegistryOps(namespace, "put").Add(1)
	p.log.Info("registered package",
		zap.String("namespace", namespace),
		zap.String("owner", owner),
		zap.String("pkg", pkg),
		zap.String("version", version),
		zap.String("system", entry.System),
	)
	w.WriteHeader(http.StatusCreated)
}

// registryGet handles GET /{ns}/f/{owner}/{name}/{version}.
func (p *Proxy) registryGet(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	owner := vars["owner"]
	pkg := vars["pkg"]
	version := vars["version"]

	entries, err := p.pebbleDB.GetRegistryEntries(namespace, owner, pkg, version)
	if err != nil {
		p.log.Error("getting registry entries", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	if len(entries) == 0 {
		serveNotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", mimeJson)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(entries)
}

// registryDelete handles DELETE /{ns}/f/{owner}/{name}/{version}.
func (p *Proxy) registryDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	owner := vars["owner"]
	pkg := vars["pkg"]
	version := vars["version"]

	// Get entries before deleting to unpin.
	entries, _ := p.pebbleDB.GetRegistryEntries(namespace, owner, pkg, version)

	if err := p.pebbleDB.DeleteRegistryVersion(namespace, owner, pkg, version); err != nil {
		p.log.Error("deleting registry version", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}

	// Unpin fromPath hashes if this was the last version referencing them.
	for _, entry := range entries {
		fromHash := StorePathHash(entry.FromPath)
		if fromHash != "" {
			_ = p.pebbleDB.UnpinHash(fromHash)
		}
	}

	metricRegistryOps(namespace, "delete").Add(1)
	w.WriteHeader(http.StatusNoContent)
}

// registryListVersions handles GET /{ns}/f/{owner}/{name}.
func (p *Proxy) registryListVersions(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	owner := vars["owner"]
	pkg := vars["pkg"]

	versions, err := p.pebbleDB.ListRegistryVersions(namespace, owner, pkg)
	if err != nil {
		p.log.Error("listing registry versions", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	if versions == nil {
		versions = []string{}
	}

	w.Header().Set("Content-Type", mimeJson)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(versions)
}

// registryListPackages handles GET /{ns}/f/{owner}.
func (p *Proxy) registryListPackages(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	owner := vars["owner"]

	packages, err := p.pebbleDB.ListRegistryPackages(namespace, owner)
	if err != nil {
		p.log.Error("listing registry packages", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	if packages == nil {
		packages = []string{}
	}

	w.Header().Set("Content-Type", mimeJson)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(packages)
}

// registryFlakeTarball handles GET /{ns}/f/{owner}/{name}/{version}.tar.gz.
// Serves an immutable tarball containing source/flake.nix.
func (p *Proxy) registryFlakeTarball(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	owner := vars["owner"]
	pkg := vars["pkg"]
	version := vars["version"]

	if p.symlinkBin == nil {
		answer(w, http.StatusServiceUnavailable, mimeText, "registry not ready: symlink binary not yet bootstrapped")
		return
	}

	entries, err := p.pebbleDB.GetRegistryEntries(namespace, owner, pkg, version)
	if err != nil {
		p.log.Error("getting registry entries for tarball", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	if len(entries) == 0 {
		serveNotFound(w, r)
		return
	}

	// Set FromStore for the symlink binary to match the request's namespace.
	sb := *p.symlinkBin
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	sb.FromStore = scheme + "://" + r.Host + "/" + namespace

	flakeNix, err := renderFlakeNix(pkg, version, entries, sb)
	if err != nil {
		p.log.Error("rendering flake.nix", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}

	tarball, err := buildFlakeTarball(flakeNix)
	if err != nil {
		p.log.Error("building tarball", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}

	contentHash, err := p.pebbleDB.RegistryContentHash(namespace, owner, pkg, version)
	if err != nil {
		p.log.Error("computing content hash", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	etag := `"` + contentHash + `"`

	// Handle conditional request.
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	immutableURL := fmt.Sprintf("%s://%s/%s/f/%s/%s/%s.tar.gz", scheme, r.Host, namespace, owner, pkg, version)

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("x-nix-is-immutable", "1")
	w.Header().Set("Link", "<"+immutableURL+">; rel=\"immutable\"")
	w.Header().Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(tarball)
}

// registryFlakeLatest handles GET /{ns}/f/{owner}/{name}/*.tar.gz.
// Resolves * to the latest version and redirects with 307.
func (p *Proxy) registryFlakeLatest(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	namespace := vars["namespace"]
	owner := vars["owner"]
	pkg := vars["pkg"]

	versions, err := p.pebbleDB.ListRegistryVersions(namespace, owner, pkg)
	if err != nil {
		p.log.Error("listing versions for latest", zap.Error(err))
		answer(w, http.StatusInternalServerError, mimeText, err.Error())
		return
	}
	if len(versions) == 0 {
		serveNotFound(w, r)
		return
	}

	latest := versions[0]
	for _, v := range versions[1:] {
		if CompareSemver(v, latest) > 0 {
			latest = v
		}
	}

	target := fmt.Sprintf("/%s/f/%s/%s/%s.tar.gz", namespace, owner, pkg, latest)
	http.Redirect(w, r, target, http.StatusTemporaryRedirect)
}

// buildFlakeTarball creates a gzip-compressed tar archive with source/flake.nix.
// Nix expects a top-level directory named "source" inside the tarball.
func buildFlakeTarball(flakeNix []byte) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Directory entry.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "source/",
		Mode:     0o755,
	}); err != nil {
		return nil, fmt.Errorf("writing tar dir header: %w", err)
	}

	// flake.nix file.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "source/flake.nix",
		Size:     int64(len(flakeNix)),
		Mode:     0o644,
	}); err != nil {
		return nil, fmt.Errorf("writing tar file header: %w", err)
	}
	if _, err := tw.Write(flakeNix); err != nil {
		return nil, fmt.Errorf("writing tar file body: %w", err)
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar writer: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, fmt.Errorf("closing gzip writer: %w", err)
	}

	return buf.Bytes(), nil
}
