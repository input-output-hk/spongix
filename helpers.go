package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kr/pretty"
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

	pretty.Println(err)
	http.Error(w, err.Error(), status)
	return true
}

type notFound struct{}

func (n notFound) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	notFoundResponse(w, r)
}

func notFoundResponse(w http.ResponseWriter, r *http.Request) {
	log.Println(r.Method, r.URL.Path)

	parts := strings.Split(r.URL.Path, "/")
	l := len(parts)

	var bucket, key string

	if l == 0 {
		w.WriteHeader(404)
		return
	}
	if l > 0 {
		bucket = parts[0]
	}
	if l > 1 {
		key = parts[l-1]
	}

	w.WriteHeader(404)
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>NoSuchKey</Code>
  <Message>The specified key does not exist.</Message>
  <Key>` + key + `</Key>
  <BucketName>` + bucket + `</BucketName>
  <Resource>` + r.RequestURI + `</Resource>
  <RequestId>16B81914FBB8345F</RequestId>
  <HostId>672a09d6-39bb-41a6-bcf3-b0375d351cfe</HostId>
</Error>`))
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		now := time.Now()
		next.ServeHTTP(w, r)
		log.Println(r.Method, r.URL.String(), time.Now().Sub(now).String())
	})
}
