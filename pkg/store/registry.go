package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cockroachdb/pebble"
)

// ErrRegistryEntryNotFound is returned when a requested registry entry does not exist.
var ErrRegistryEntryNotFound = errors.New("registry entry not found")

// RegistryEntry mirrors the main package's RegistryEntry for storage.
type RegistryEntry struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	System      string `json:"system"`
	FromPath    string `json:"fromPath"`
	ToPath      string `json:"toPath"`
	FromStore   string `json:"fromStore"`
	MainProgram string `json:"mainProgram"`
	CreatedAt   int64  `json:"createdAt"`
}

// registryKey returns: reg/{namespace}/{owner}/{name}/{version}/{system}
func registryKey(namespace, owner, name, version, system string) []byte {
	k := "reg/" + namespace + "/" + owner + "/" + name + "/" + version + "/" + system
	return []byte(k)
}

// registryVersionPrefix returns: reg/{namespace}/{owner}/{name}/{version}/
func registryVersionPrefix(namespace, owner, name, version string) []byte {
	return []byte("reg/" + namespace + "/" + owner + "/" + name + "/" + version + "/")
}

// registryPackagePrefix returns: reg/{namespace}/{owner}/{name}/
func registryPackagePrefix(namespace, owner, name string) []byte {
	return []byte("reg/" + namespace + "/" + owner + "/" + name + "/")
}

// registryOwnerPrefix returns: reg/{namespace}/{owner}/
func registryOwnerPrefix(namespace, owner string) []byte {
	return []byte("reg/" + namespace + "/" + owner + "/")
}

// PutRegistryEntry stores a registry entry.
func (d *DB) PutRegistryEntry(namespace string, entry *RegistryEntry) error {
	key := registryKey(namespace, entry.Owner, entry.Name, entry.Version, entry.System)
	val, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling registry entry: %w", err)
	}
	return d.db.Set(key, val, pebble.Sync)
}

// GetRegistryEntry retrieves a single registry entry by exact key.
func (d *DB) GetRegistryEntry(namespace, owner, name, version, system string) (*RegistryEntry, error) {
	key := registryKey(namespace, owner, name, version, system)
	val, closer, err := d.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, ErrRegistryEntryNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting registry entry: %w", err)
	}
	data := make([]byte, len(val))
	copy(data, val)
	_ = closer.Close()

	var entry RegistryEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("unmarshaling registry entry: %w", err)
	}
	return &entry, nil
}

// GetRegistryEntries returns all system entries for a given package version.
func (d *DB) GetRegistryEntries(namespace, owner, name, version string) ([]*RegistryEntry, error) {
	prefix := registryVersionPrefix(namespace, owner, name, version)
	return d.iterRegistryEntries(prefix)
}

// DeleteRegistryEntry deletes a single registry entry.
func (d *DB) DeleteRegistryEntry(namespace, owner, name, version, system string) error {
	key := registryKey(namespace, owner, name, version, system)
	return d.db.Delete(key, pebble.Sync)
}

// DeleteRegistryVersion deletes all system entries for a package version.
func (d *DB) DeleteRegistryVersion(namespace, owner, name, version string) error {
	prefix := registryVersionPrefix(namespace, owner, name, version)
	iter, err := d.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return fmt.Errorf("creating registry iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	batch := d.db.NewBatch()
	for iter.First(); iter.Valid(); iter.Next() {
		_ = batch.Delete(append([]byte{}, iter.Key()...), nil)
	}
	if err := iter.Error(); err != nil {
		_ = batch.Close()
		return err
	}
	return batch.Commit(pebble.Sync)
}

// ListRegistryVersions returns all unique versions for a package.
func (d *DB) ListRegistryVersions(namespace, owner, name string) ([]string, error) {
	prefix := registryPackagePrefix(namespace, owner, name)
	iter, err := d.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("creating registry iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	seen := map[string]struct{}{}
	var versions []string
	for iter.First(); iter.Valid(); iter.Next() {
		// Key: reg/{ns}/{owner}/{name}/{version}/{system}
		key := string(iter.Key())
		rest := strings.TrimPrefix(key, string(prefix))
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) >= 1 {
			v := parts[0]
			if _, ok := seen[v]; !ok {
				seen[v] = struct{}{}
				versions = append(versions, v)
			}
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return versions, nil
}

// ListRegistryPackages returns all unique package names for an owner.
func (d *DB) ListRegistryPackages(namespace, owner string) ([]string, error) {
	prefix := registryOwnerPrefix(namespace, owner)
	iter, err := d.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("creating registry iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	seen := map[string]struct{}{}
	var packages []string
	for iter.First(); iter.Valid(); iter.Next() {
		// Key: reg/{ns}/{owner}/{name}/{version}/{system}
		key := string(iter.Key())
		rest := strings.TrimPrefix(key, string(prefix))
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) >= 1 {
			pkg := parts[0]
			if _, ok := seen[pkg]; !ok {
				seen[pkg] = struct{}{}
				packages = append(packages, pkg)
			}
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return packages, nil
}

// RegistryContentHash computes a stable hash over all entries for a package version.
// Used for ETag headers.
func (d *DB) RegistryContentHash(namespace, owner, name, version string) (string, error) {
	entries, err := d.GetRegistryEntries(namespace, owner, name, version)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, e := range entries {
		data, err := json.Marshal(e)
		if err != nil {
			return "", err
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// iterRegistryEntries iterates over a prefix and returns all registry entries.
func (d *DB) iterRegistryEntries(prefix []byte) ([]*RegistryEntry, error) {
	iter, err := d.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("creating registry iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	var entries []*RegistryEntry
	for iter.First(); iter.Valid(); iter.Next() {
		val := iter.Value()
		data := make([]byte, len(val))
		copy(data, val)
		var entry RegistryEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return nil, fmt.Errorf("unmarshaling registry entry: %w", err)
		}
		entries = append(entries, &entry)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return entries, nil
}
