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
	Name        string `json:"name"`
	Version     string `json:"version"`
	System      string `json:"system"`
	FromPath    string `json:"fromPath"`
	ToPath      string `json:"toPath"`
	FromStore   string `json:"fromStore"`
	MainProgram string `json:"mainProgram"`
	CreatedAt   int64  `json:"createdAt"`
}

// registryKey returns: reg/{namespace}/{name}/{version}/{system}
func registryKey(namespace, name, version, system string) []byte {
	k := "reg/" + namespace + "/" + name + "/" + version + "/" + system
	return []byte(k)
}

// registryVersionPrefix returns: reg/{namespace}/{name}/{version}/
func registryVersionPrefix(namespace, name, version string) []byte {
	return []byte("reg/" + namespace + "/" + name + "/" + version + "/")
}

// registryPackagePrefix returns: reg/{namespace}/{name}/
func registryPackagePrefix(namespace, name string) []byte {
	return []byte("reg/" + namespace + "/" + name + "/")
}

// registryNamespacePrefix returns: reg/{namespace}/
func registryNamespacePrefix(namespace string) []byte {
	return []byte("reg/" + namespace + "/")
}

// PutRegistryEntry stores a registry entry.
func (d *DB) PutRegistryEntry(namespace string, entry *RegistryEntry) error {
	key := registryKey(namespace, entry.Name, entry.Version, entry.System)
	val, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling registry entry: %w", err)
	}
	return d.db.Set(key, val, pebble.Sync)
}

// GetRegistryEntry retrieves a single registry entry by exact key.
func (d *DB) GetRegistryEntry(namespace, name, version, system string) (*RegistryEntry, error) {
	key := registryKey(namespace, name, version, system)
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
func (d *DB) GetRegistryEntries(namespace, name, version string) ([]*RegistryEntry, error) {
	prefix := registryVersionPrefix(namespace, name, version)
	return d.iterRegistryEntries(prefix)
}

// DeleteRegistryEntry deletes a single registry entry.
func (d *DB) DeleteRegistryEntry(namespace, name, version, system string) error {
	key := registryKey(namespace, name, version, system)
	return d.db.Delete(key, pebble.Sync)
}

// DeleteRegistryVersion deletes all system entries for a package version.
func (d *DB) DeleteRegistryVersion(namespace, name, version string) error {
	prefix := registryVersionPrefix(namespace, name, version)
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
func (d *DB) ListRegistryVersions(namespace, name string) ([]string, error) {
	prefix := registryPackagePrefix(namespace, name)
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
		// Key: reg/{ns}/{name}/{version}/{system}
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

// ListRegistryPackages returns all unique package names for a namespace.
func (d *DB) ListRegistryPackages(namespace string) ([]string, error) {
	prefix := registryNamespacePrefix(namespace)
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
		// Key: reg/{ns}/{name}/{version}/{system}
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
func (d *DB) RegistryContentHash(namespace, name, version string) (string, error) {
	entries, err := d.GetRegistryEntries(namespace, name, version)
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

// MigrateRegistryDropOwner rewrites old 6-segment registry keys
// (reg/{namespace}/{owner}/{name}/{version}/{system}) to the new 5-segment
// format (reg/{namespace}/{name}/{version}/{system}). Idempotent: new-format
// keys are left untouched. Called once at startup.
func (d *DB) MigrateRegistryDropOwner() error {
	prefix := []byte("reg/")
	iter, err := d.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return fmt.Errorf("creating migration iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	batch := d.db.NewBatch()
	migrated := 0

	for iter.First(); iter.Valid(); iter.Next() {
		key := string(iter.Key())
		parts := strings.Split(key, "/")
		// Old format: reg/{namespace}/{owner}/{name}/{version}/{system} = 6 parts
		// New format: reg/{namespace}/{name}/{version}/{system} = 5 parts
		if len(parts) != 6 {
			continue
		}

		// parts: [reg, namespace, owner, name, version, system]
		namespace := parts[1]
		// owner is parts[2] -- dropped
		name := parts[3]
		version := parts[4]
		system := parts[5]

		// Read the value and strip the owner field from the JSON.
		val := iter.Value()
		data := make([]byte, len(val))
		copy(data, val)

		var entry RegistryEntry
		if err := json.Unmarshal(data, &entry); err != nil {
			return fmt.Errorf("unmarshaling old entry %s: %w", key, err)
		}

		newKey := registryKey(namespace, name, version, system)
		newVal, err := json.Marshal(&entry)
		if err != nil {
			return fmt.Errorf("marshaling migrated entry: %w", err)
		}

		_ = batch.Set(newKey, newVal, nil)
		_ = batch.Delete([]byte(key), nil)
		migrated++
	}
	if err := iter.Error(); err != nil {
		_ = batch.Close()
		return err
	}

	if migrated > 0 {
		if err := batch.Commit(pebble.Sync); err != nil {
			return fmt.Errorf("committing migration batch: %w", err)
		}
	} else {
		_ = batch.Close()
	}

	return nil
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
