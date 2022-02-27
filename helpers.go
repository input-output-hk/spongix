package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"go.uber.org/zap"
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
	w.WriteHeader(404)
	_, _ = w.Write([]byte(r.RequestURI + " not found"))
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

type parallelResponse struct {
	body io.ReadCloser
	url  string
	code int
}

func (proxy *Proxy) parallelRequest(ctx context.Context, method, path string) *parallelResponse {
	c := make(chan *parallelResponse, len(proxy.Substituters))

	go func() {
		var wg sync.WaitGroup
		wg.Add(len(proxy.Substituters))

		for _, substituter := range proxy.Substituters {
			go func(sub string) {
				res, err := doParallelReqeust(ctx, method, sub, path)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						proxy.log.Error("parallel request error", zap.Error(err))
					}
				} else {
					if res.code == 200 {
						select {
						case c <- res:
						case <-ctx.Done():
							proxy.log.Warn("timeout", zap.String("url", res.url))
						}
					}
				}

				wg.Done()
			}(substituter)
		}

		wg.Wait()
		close(c)
	}()

	var found *parallelResponse
	select {
	case found = <-c:
	case <-ctx.Done():
	}

	return found
}

func doParallelReqeust(ctx context.Context, method, sub, path string) (*parallelResponse, error) {
	subUrl, err := url.Parse(sub)
	if err != nil {
		return nil, err
	}
	subUrl.Path = path

	request, err := http.NewRequestWithContext(ctx, method, subUrl.String(), nil)
	if err != nil {
		return nil, err
	}

	res, err := http.DefaultClient.Do(request)
	if err != nil {
		urlErr, ok := err.(*url.Error)
		if ok && urlErr.Err.Error() == "context canceled" {
			return nil, err
		}

		return nil, err
	}

	switch res.StatusCode {
	case 200:
		return &parallelResponse{body: res.Body, url: subUrl.String(), code: res.StatusCode}, nil
	default:
		res.Body.Close()
		return &parallelResponse{url: subUrl.String(), code: res.StatusCode}, nil
	}
}
