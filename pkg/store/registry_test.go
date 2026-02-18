package store

import (
	"sort"
	"testing"
)

func TestRegistryPutGetRoundTrip(t *testing.T) {
	db := tempDB(t)

	entry := &RegistryEntry{
		Owner:       "manveru",
		Name:        "claude-code-nix",
		Version:     "2.1.45",
		System:      "x86_64-linux",
		FromPath:    "/nix/store/abc00000000000000000000000000001-claude-code-nix-2.1.45",
		ToPath:      "/nix/store/xyz00000000000000000000000000002-claude-code-nix-2.1.45",
		FromStore:   "http://localhost:7745/cache",
		MainProgram: "claude",
		CreatedAt:   1000000,
	}

	if err := db.PutRegistryEntry("cache", entry); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetRegistryEntry("cache", "manveru", "claude-code-nix", "2.1.45", "x86_64-linux")
	if err != nil {
		t.Fatal(err)
	}
	if got.Owner != "manveru" || got.Name != "claude-code-nix" || got.Version != "2.1.45" {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.System != "x86_64-linux" {
		t.Fatalf("unexpected system: %s", got.System)
	}
	if got.MainProgram != "claude" {
		t.Fatalf("unexpected mainProgram: %s", got.MainProgram)
	}
}

func TestRegistryGetNotFound(t *testing.T) {
	db := tempDB(t)

	_, err := db.GetRegistryEntry("cache", "nobody", "nothing", "0.0.0", "x86_64-linux")
	if err != ErrRegistryEntryNotFound {
		t.Fatalf("expected ErrRegistryEntryNotFound, got %v", err)
	}
}

func TestRegistryGetEntries(t *testing.T) {
	db := tempDB(t)

	for _, sys := range []string{"x86_64-linux", "aarch64-linux"} {
		entry := &RegistryEntry{
			Owner:    "manveru",
			Name:     "test-pkg",
			Version:  "1.0.0",
			System:   sys,
			FromPath: "/nix/store/abc00000000000000000000000000001-test-pkg-1.0.0",
			ToPath:   "/nix/store/xyz00000000000000000000000000002-test-pkg-1.0.0",
		}
		if err := db.PutRegistryEntry("cache", entry); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := db.GetRegistryEntries("cache", "manveru", "test-pkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestRegistryDeleteVersion(t *testing.T) {
	db := tempDB(t)

	for _, sys := range []string{"x86_64-linux", "aarch64-linux"} {
		entry := &RegistryEntry{
			Owner:    "manveru",
			Name:     "test-pkg",
			Version:  "1.0.0",
			System:   sys,
			FromPath: "/nix/store/abc00000000000000000000000000001-test-pkg-1.0.0",
			ToPath:   "/nix/store/xyz00000000000000000000000000002-test-pkg-1.0.0",
		}
		if err := db.PutRegistryEntry("cache", entry); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.DeleteRegistryVersion("cache", "manveru", "test-pkg", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	entries, err := db.GetRegistryEntries("cache", "manveru", "test-pkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", len(entries))
	}
}

func TestRegistryListVersions(t *testing.T) {
	db := tempDB(t)

	for _, v := range []string{"1.0.0", "1.1.0", "2.0.0"} {
		entry := &RegistryEntry{
			Owner:    "manveru",
			Name:     "test-pkg",
			Version:  v,
			System:   "x86_64-linux",
			FromPath: "/nix/store/abc00000000000000000000000000001-test-pkg-" + v,
			ToPath:   "/nix/store/xyz00000000000000000000000000002-test-pkg-" + v,
		}
		if err := db.PutRegistryEntry("cache", entry); err != nil {
			t.Fatal(err)
		}
	}

	versions, err := db.ListRegistryVersions("cache", "manveru", "test-pkg")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(versions)
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(versions))
	}
	if versions[0] != "1.0.0" || versions[1] != "1.1.0" || versions[2] != "2.0.0" {
		t.Fatalf("unexpected versions: %v", versions)
	}
}

func TestRegistryListPackages(t *testing.T) {
	db := tempDB(t)

	for _, name := range []string{"pkg-a", "pkg-b"} {
		entry := &RegistryEntry{
			Owner:    "manveru",
			Name:     name,
			Version:  "1.0.0",
			System:   "x86_64-linux",
			FromPath: "/nix/store/abc00000000000000000000000000001-" + name,
			ToPath:   "/nix/store/xyz00000000000000000000000000002-" + name,
		}
		if err := db.PutRegistryEntry("cache", entry); err != nil {
			t.Fatal(err)
		}
	}

	packages, err := db.ListRegistryPackages("cache", "manveru")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(packages)
	if len(packages) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(packages))
	}
	if packages[0] != "pkg-a" || packages[1] != "pkg-b" {
		t.Fatalf("unexpected packages: %v", packages)
	}
}

func TestRegistryContentHash(t *testing.T) {
	db := tempDB(t)

	entry := &RegistryEntry{
		Owner:    "manveru",
		Name:     "test-pkg",
		Version:  "1.0.0",
		System:   "x86_64-linux",
		FromPath: "/nix/store/abc00000000000000000000000000001-test-pkg-1.0.0",
		ToPath:   "/nix/store/xyz00000000000000000000000000002-test-pkg-1.0.0",
	}
	if err := db.PutRegistryEntry("cache", entry); err != nil {
		t.Fatal(err)
	}

	hash1, err := db.RegistryContentHash("cache", "manveru", "test-pkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if hash1 == "" {
		t.Fatal("expected non-empty hash")
	}

	// Same content should produce the same hash.
	hash2, err := db.RegistryContentHash("cache", "manveru", "test-pkg", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if hash1 != hash2 {
		t.Fatalf("hash mismatch: %s != %s", hash1, hash2)
	}
}

func TestRegistryNamespaceIsolation(t *testing.T) {
	db := tempDB(t)

	entry := &RegistryEntry{
		Owner:    "manveru",
		Name:     "test-pkg",
		Version:  "1.0.0",
		System:   "x86_64-linux",
		FromPath: "/nix/store/abc00000000000000000000000000001-test-pkg-1.0.0",
		ToPath:   "/nix/store/xyz00000000000000000000000000002-test-pkg-1.0.0",
	}
	if err := db.PutRegistryEntry("ns1", entry); err != nil {
		t.Fatal(err)
	}

	// Different namespace should not see it.
	_, err := db.GetRegistryEntry("ns2", "manveru", "test-pkg", "1.0.0", "x86_64-linux")
	if err != ErrRegistryEntryNotFound {
		t.Fatalf("expected not found in different namespace, got %v", err)
	}
}
