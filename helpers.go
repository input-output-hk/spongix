package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"strings"

	"github.com/kr/pretty"
	"github.com/pkg/errors"
)

func pp(v ...interface{}) {
	pretty.Println(v...)
}

func loadNixPublicKeys(rawKeys []string) (map[string]ed25519.PublicKey, error) {
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

func loadNixPrivateKeys(paths []string) (map[string]ed25519.PrivateKey, error) {
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
