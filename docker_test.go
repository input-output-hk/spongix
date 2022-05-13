package main

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/folbricht/desync"
	"github.com/gorilla/mux"
	"github.com/steinfletcher/apitest"
	"go.uber.org/zap"
)

func testDocker(t *testing.T) dockerHandler {
	var store desync.LocalStore
	var index desync.LocalIndexStore

	indexDir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(filepath.Join(indexDir, "spongix/blobs"), 0700); err != nil {
		t.Fatal(err)
	} else if index, err = desync.NewLocalIndexStore(indexDir); err != nil {
		t.Fatal(err)
	}

	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		t.Fatal(err)
	} else if store, err = desync.NewLocalStore(storeDir, defaultStoreOptions); err != nil {
		t.Fatal(err)
	}

	log, err := zap.NewDevelopment()
	if err != nil {
		t.Fatal(err)
	}
	return newDockerHandler(log, store, index, mux.NewRouter())
}

func TestDocker(t *testing.T) {
	proxy := testProxy(t)

	apitest.New().
		Handler(proxy.router()).
		Get("/v2/").
		Expect(t).
		Header(headerContentType, mimeJson).
		Body(`{}`).
		Status(http.StatusOK).
		End()
}

func TestDockerBlob(t *testing.T) {
	proxy := testProxy(t)
	router := proxy.router()

	uploadResult := apitest.New().
		Handler(router).
		Post("/v2/spongix/blobs/uploads/").
		Body(`{}`).
		Expect(t).
		Status(http.StatusAccepted).
		HeaderPresent("Location").
		Header("Content-Length", "0").
		Header("Range", "0-0").
		HeaderPresent("Docker-Upload-UUID").
		End()

	location, err := url.Parse(uploadResult.Response.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}

	digest := "sha256:bd60d81d7c94dec8378b4e6fb652462a9156618bfd34c6673ad9d81566d2d6cc"

	apitest.New().
		Handler(router).
		Put(location.RequestURI()).
		Query("digest", digest).
		Body(`{}`).
		Expect(t).
		Status(http.StatusCreated).
		Header("Content-Length", "0").
		Header("Range", "0-2").
		HeaderPresent("Docker-Upload-UUID").
		End()

	apitest.New().
		Handler(router).
		Method("HEAD").
		URL("/v2/spongix/blobs/" + digest).
		Expect(t).
		Status(http.StatusOK).
		Headers(map[string]string{}).
		End()
}
