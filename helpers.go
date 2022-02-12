package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/pkg/errors"
)

func LoadNixPublicKeys(rawKeys []string) (map[string]ed25519.PublicKey, error) {
	keys := map[string]ed25519.PublicKey{}
	for _, rawKey := range rawKeys {
		name, value, err := parseNixPair(rawKey)
		if err != nil {
			return nil, errors.WithMessage(err, "While loading public keys")
		}
		keys[name] = ed25519.PublicKey(value)
	}

	return keys, nil
}

func LoadNixPrivateKeys(paths []string) (map[string]ed25519.PrivateKey, error) {
	pairs, err := readNixPairs(paths)
	if err != nil {
		return nil, errors.WithMessage(err, "While loading private keys")
	}

	keys := map[string]ed25519.PrivateKey{}
	for name, key := range pairs {
		keys[name] = ed25519.PrivateKey(key)
	}

	return keys, nil
}

func readNixPairs(paths []string) (map[string][]byte, error) {
	keys := map[string][]byte{}

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.WithMessagef(err, "Trying to read %q", path)
		}

		name, key, err := parseNixPair(string(raw))
		if err != nil {
			return nil, errors.WithMessagef(err, "Key parsing failed for %q", raw)
		}

		keys[name] = key
	}

	return keys, nil
}

func parseNixPair(input string) (string, []byte, error) {
	i := strings.IndexRune(input, ':')
	if i < 1 {
		return "", nil, errors.Errorf("Key has no name part in %q", input)
	}
	name := input[0:i]
	encoded := input[i+1:]
	value, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", nil, errors.Errorf("Key decoding failed for %q", encoded)
	}

	return name, value, nil
}

func internalServerError(w http.ResponseWriter, err error) bool {
	return respondError(w, err, http.StatusInternalServerError)
}

func badRequest(w http.ResponseWriter, err error) bool {
	return respondError(w, err, http.StatusBadRequest)
}

func respondError(w http.ResponseWriter, err error, status int) bool {
	if err == nil {
		return false
	}

	http.Error(w, err.Error(), status)
	return true
}

type notFound struct{}

func (n notFound) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	notFoundResponse(w, r)
}

func notFoundResponse(w http.ResponseWriter, r *http.Request) {
	log.Println(r.Method, r.URL.Path)
	w.WriteHeader(404)
	_, _ = w.Write([]byte(r.RequestURI + " not found"))
}

func fatal(err error) {
	if err != nil {
		log.Panic(err)
	}
}

func ByteCountSI(b int64) string {
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}
