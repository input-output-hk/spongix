package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/input-output-hk/spongix/pkg/store"
)

// RegistryPutRequest is the JSON body for PUT /{ns}/f/{owner}/{name}/{version}.
type RegistryPutRequest struct {
	FromPath    string `json:"fromPath"`
	ToPath      string `json:"toPath"`
	FromStore   string `json:"fromStore"`
	MainProgram string `json:"mainProgram"`
	System      string `json:"system"`
}

var storePathRe = regexp.MustCompile(`^/nix/store/([0-9a-df-np-sv-z]{32})-(.+)$`)

// ValidateStorePath checks that a string looks like a valid Nix store path
// and returns the 32-char hash.
func ValidateStorePath(p string) (string, error) {
	m := storePathRe.FindStringSubmatch(p)
	if m == nil {
		return "", fmt.Errorf("invalid store path: %q", p)
	}
	return m[1], nil
}

// StorePathHash extracts the 32-char hash from a Nix store path.
func StorePathHash(p string) string {
	m := storePathRe.FindStringSubmatch(p)
	if m == nil {
		return ""
	}
	return m[1]
}

// ValidateRegistryPut validates a PUT request and returns a RegistryEntry.
func ValidateRegistryPut(name, version string, req RegistryPutRequest) (*store.RegistryEntry, error) {
	if req.System == "" {
		return nil, fmt.Errorf("system is required")
	}
	if _, err := ValidateStorePath(req.FromPath); err != nil {
		return nil, fmt.Errorf("fromPath: %w", err)
	}
	if _, err := ValidateStorePath(req.ToPath); err != nil {
		return nil, fmt.Errorf("toPath: %w", err)
	}
	return &store.RegistryEntry{
		Name:        name,
		Version:     version,
		System:      req.System,
		FromPath:    req.FromPath,
		ToPath:      req.ToPath,
		FromStore:   req.FromStore,
		MainProgram: req.MainProgram,
		CreatedAt:   time.Now().Unix(),
	}, nil
}

// CompareSemver compares two semver-ish version strings.
// Returns -1, 0, or 1. Non-numeric parts are compared lexicographically.
func CompareSemver(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")

	maxLen := max(len(partsA), len(partsB))

	for i := range maxLen {
		var pa, pb string
		if i < len(partsA) {
			pa = partsA[i]
		}
		if i < len(partsB) {
			pb = partsB[i]
		}

		na, errA := strconv.Atoi(pa)
		nb, errB := strconv.Atoi(pb)

		if errA == nil && errB == nil {
			if na < nb {
				return -1
			}
			if na > nb {
				return 1
			}
		} else {
			if pa < pb {
				return -1
			}
			if pa > pb {
				return 1
			}
		}
	}
	return 0
}
