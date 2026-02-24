package store

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/cockroachdb/pebble"
)

// pinKey returns the Pebble key for a pin: p/<hash>
func pinKey(hash string) []byte {
	key := make([]byte, 2+len(hash))
	key[0] = 'p'
	key[1] = '/'
	copy(key[2:], hash)
	return key
}

// PinEntry represents a pinned store path hash with an optional label.
type PinEntry struct {
	Hash  string `json:"hash"`
	Label string `json:"label,omitempty"`
}

// PinHash adds a pin for the given store path hash with an optional label.
func (d *DB) PinHash(hash, label string) error {
	return d.db.Set(pinKey(hash), []byte(label), pebble.Sync)
}

// UnpinHash removes a pin for the given store path hash.
func (d *DB) UnpinHash(hash string) error {
	return d.db.Delete(pinKey(hash), pebble.Sync)
}

// ListPins returns all pinned store path hashes with their labels.
func (d *DB) ListPins() ([]PinEntry, error) {
	prefix := []byte("p/")
	iter, err := d.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: incrementKey(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("creating pin iterator: %w", err)
	}
	defer func() { _ = iter.Close() }()

	var pins []PinEntry
	for iter.First(); iter.Valid(); iter.Next() {
		hash := strings.TrimPrefix(string(iter.Key()), "p/")
		label := string(iter.Value())
		pins = append(pins, PinEntry{Hash: hash, Label: label})
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	return pins, nil
}

// IsPinned returns whether the given store path hash is pinned.
func (d *DB) IsPinned(hash string) (bool, error) {
	_, closer, err := d.db.Get(pinKey(hash))
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking pin %s: %w", hash, err)
	}
	_ = closer.Close()
	return true, nil
}

// LoadPinnedHashes reads a file containing one store path hash per line.
// Blank lines and lines starting with # are skipped.
// Returns an empty map if path is empty or the file doesn't exist.
func LoadPinnedHashes(path string) (map[string]struct{}, error) {
	if path == "" {
		return map[string]struct{}{}, nil
	}

	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]struct{}{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening pinned hashes file: %w", err)
	}
	defer func() { _ = f.Close() }()

	pins := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pins[line] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading pinned hashes file: %w", err)
	}
	return pins, nil
}
