package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/folbricht/desync"
	"github.com/jmoiron/sqlx"
	"github.com/steinfletcher/apitest"
)

const (
	fNar     = "/nar/0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar"
	fNarXz   = "/nar/0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar.xz"
	fNarinfo = "/8ckxc8biqqfdwyhr0w70jgrcb4h7a4y5.narinfo"
	fKey     = "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
	fSig     = "foo:MGrENumWZ1kbm23vCTyYrw6hRBJtLGIIpfHjpZszs2D1G1AALMKvl49T66WIhx2X02s8n/zsfUPpga2bL6PmBQ=="
)

var (
	testdata       = map[string][]byte{}
	testNamespaces = []string{"sunlight", "daylight"}
)

func TestMain(m *testing.M) {
	for _, name := range []string{
		fNar, fNarXz, fNarinfo,
	} {
		content, err := os.ReadFile(filepath.Join("testdata", filepath.Base(name)))
		if err != nil {
			panic(err)
		}

		testdata[name] = content
	}

	os.Exit(m.Run())
}

func testProxy(t *testing.T) *Proxy {
	proxy := NewProxy()
	proxy.Substituters = []string{
		"http://example.com",
		"http://example.org/foo/bar",
	}

	indexDir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(filepath.Join(indexDir, "nar"), 0700); err != nil {
		panic(err)
	} else if proxy.localIndices[""], err = desync.NewLocalIndexStore(indexDir); err != nil {
		panic(err)
	}

	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		panic(err)
	} else if proxy.localStore, err = desync.NewLocalStore(storeDir, defaultStoreOptions); err != nil {
		panic(err)
	}

	proxy.Dir = t.TempDir()
	proxy.TrustedPublicKeys = []string{fKey}
	proxy.setupKeys()

	// NOTE: comment this line to enable logging
	// proxy.log = zap.NewNop()

	proxy.DSN = ":memory:"
	proxy.setupDatabase()

	proxy.Namespaces = testNamespaces

	for _, namespace := range proxy.Namespaces {
		privateIndexDir := filepath.Join(t.TempDir(), "privateIndex/"+namespace)
		if err := os.MkdirAll(filepath.Join(privateIndexDir, "nar"), 0700); err != nil {
			panic(err)
		} else if proxy.localIndices[namespace], err = desync.NewLocalIndexStore(privateIndexDir); err != nil {
			panic(err)
		}
	}
	return proxy
}

func withS3(proxy *Proxy) *Proxy {
	proxy.s3Store = newFakeStore()
	proxy.s3Indices[""] = newFakeIndex()
	for _, namespace := range proxy.Namespaces {
		proxy.s3Indices[namespace] = newFakeIndex()
	}
	return proxy
}

func TestRouterNixCacheInfo(t *testing.T) {
	for _, namespace := range testNamespaces {
		t.Run(namespace, func(tt *testing.T) {
			proxy := testProxy(tt)
			apitest.New().
				Handler(proxy.router()).
				Get("/"+namespace+"/nix-cache-info").
				Expect(tt).
				Header(headerContentType, mimeNixCacheInfo).
				Body(`StoreDir: /nix/store
WantMassQuery: 1
Priority: 50`).
				Status(http.StatusOK).
				End()
		})
	}
}

func TestRouterNarinfoHead(t *testing.T) {
	forNamespaces(t, "not found", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(``).
			Status(http.StatusNotFound).
			End()
	})

	forNamespaces(t, "found remote", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head("http://example.com"+fNarinfo).
					RespondWith().
					Status(http.StatusOK).
					End(),
			).
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found local", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		insertNarinfos(tt, proxy.db, testNamespaces)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found s3", func(tt *testing.T, namespace string) {
		proxy := withS3(testProxy(tt))
		insertNarinfos(tt, proxy.db, testNamespaces)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func forNamespaces(t *testing.T, name string, f func(*testing.T, string)) {
	t.Helper()
	t.Run(name, func(tt *testing.T) {
		tt.Helper()
		for _, namespace := range testNamespaces {
			tt.Run(namespace, func(ttt *testing.T) {
				ttt.Helper()
				f(ttt, namespace)
			})
		}
	})
}

func TestRouterNarHead(t *testing.T) {
	forNamespaces(t, "not found", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head("/"+namespace+fNar+".xz").
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Head("/"+namespace+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End()).
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(``).
			Status(http.StatusNotFound).
			End()
	})

	forNamespaces(t, "found remote", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head("http://example.com"+fNar+".xz").
					RespondWith().
					Status(http.StatusOK).
					End(),
				apitest.NewMock().
					Head("http://example.com"+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Head("http://example.org/foo/bar"+fNar+".xz").
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Head("http://example.org/foo/bar"+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNar+".xz").
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found local", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		insertFake(tt, proxy.localStore, proxy.localIndices, "/"+namespace+fNar)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found s3", func(tt *testing.T, namespace string) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Indices, "/"+namespace+fNar)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head("/"+namespace+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("HEAD").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarGet(t *testing.T) {
	forNamespaces(t, "not found", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		apitest.New().
			Mocks(
				apitest.NewMock().
					Get("/"+namespace+fNar+".xz").
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Get("/"+namespace+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(`not found`).
			Status(http.StatusNotFound).
			End()
	})

	forNamespaces(t, "found remote xz", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Get("http://example.com"+fNar+".xz").
					RespondWith().
					Body(string(testdata[fNarXz])).
					Status(http.StatusOK).
					End(),
				apitest.NewMock().
					Get("http://example.com"+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Get("http://example.org/foo/bar"+fNar+".xz").
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Get("http://example.org/foo/bar"+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNar+".xz").
			Header(headerContentType, mimeNar).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found remote xz and requested xz", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		apitest.New().
			Mocks(
				apitest.NewMock().
					Get("http://example.com"+fNarXz).
					RespondWith().
					Body(string(testdata[fNarXz])).
					Status(http.StatusOK).
					End(),
				apitest.NewMock().
					Get("http://example.com"+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Get("http://example.org/foo/bar"+fNarXz).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Get("http://example.org/foo/bar"+fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarXz).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNarXz).
			Header(headerContentType, mimeNar).
			Body(string(testdata[fNarXz])).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found local", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		insertFake(tt, proxy.localStore, proxy.localIndices, "/"+namespace+fNar)

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found s3", func(tt *testing.T, namespace string) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Indices, "/"+namespace+fNar)

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarinfoGet(t *testing.T) {
	forNamespaces(t, "not found", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(`not found`).
			Status(http.StatusNotFound).
			End()
	})

	forNamespaces(t, "found local", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarinfo).
			Body(string(testdata[fNarinfo])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found s3", func(tt *testing.T, namespace string) {
		proxy := withS3(testProxy(tt))

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarinfo).
			Body(string(testdata[fNarinfo])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "found remote", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			EnableMockResponseDelay().
			Mocks(
				apitest.NewMock().
					Get("http://example.com"+fNarinfo).
					RespondWith().
					FixedDelay((1*time.Second).Milliseconds()).
					Body(string(testdata[fNarinfo])).
					Status(http.StatusOK).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "copies remote to local", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		go proxy.startCache()
		defer close(proxy.cacheChan)

		mockReset := apitest.NewStandaloneMocks(
			apitest.NewMock().
				Get("http://example.com" + fNarinfo).
				RespondWith().
				Body(string(testdata[fNarinfo])).
				Status(http.StatusOK).
				End(),
		).End()
		defer mockReset()

		apitest.New().
			Mocks(
				apitest.NewMock().
					Get("http://example.com"+fNarinfo).
					RespondWith().
					Body(string(testdata[fNarinfo])).
					Status(http.StatusOK).
					End(),
				apitest.NewMock().
					Get("http://example.org/foo/bar"+fNarinfo).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()

		time.Sleep(2 * time.Millisecond)
		for metricRemoteCachedOk.Get()+metricRemoteCachedFail.Get() == 0 {
			time.Sleep(1 * time.Millisecond)
		}

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarinfoPut(t *testing.T) {
	forNamespaces(t, "upload success with namespace", func(tt *testing.T, namespace string) {
		proxy := withS3(testProxy(tt))

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarinfo).
			Body(string(testdata[fNarinfo])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerContentType, mimeNarinfo).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "upload invalid with namespace", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarinfo).
			Body("blah").
			Expect(tt).
			Header(headerContentType, mimeText).
			Body(`Failed to parse line: "blah"`).
			Status(http.StatusBadRequest).
			End()
	})

	forNamespaces(t, "upload unsigned with namespace", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarinfo).
			Body("blah").
			Expect(tt).
			Header(headerContentType, mimeText).
			Body(`Failed to parse line: "blah"`).
			Status(http.StatusBadRequest).
			End()
	})

	forNamespaces(t, "signs unsigned narinfos", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)
		seed := make([]byte, ed25519.SeedSize)
		proxy.secretKeys["foo"] = ed25519.NewKeyFromSeed(seed)

		emptyInfo := &Narinfo{Namespace: "test"}
		if err := emptyInfo.Unmarshal(bytes.NewReader(testdata[fNarinfo])); err != nil {
			tt.Fatal(err)
		}
		emptyInfo.Sig = []Signature{}
		empty := &bytes.Buffer{}
		if err := emptyInfo.Marshal(empty); err != nil {
			tt.Fatal(err)
		}

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarinfo).
			Body(empty.String()).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		expectInfo := &Narinfo{Namespace: "test"}
		if err := expectInfo.Unmarshal(bytes.NewReader(testdata[fNarinfo])); err != nil {
			tt.Fatal(err)
		}
		expectInfo.Sig = []Signature{fSig}
		expect := &bytes.Buffer{}
		if err := expectInfo.Marshal(expect); err != nil {
			tt.Fatal(err)
		}

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(expect.String()).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarPut(t *testing.T) {
	forNamespaces(t, "upload success", func(tt *testing.T, namespace string) {
		proxy := withS3(testProxy(tt))
		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNar).
			Body(string(testdata[fNar])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerContentType, mimeNar).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})

	forNamespaces(t, "upload xz success with namespace", func(tt *testing.T, namespace string) {
		proxy := withS3(testProxy(tt))

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarXz).
			Body(string(testdata[fNarXz])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerContentType, mimeNar).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNamespaces(t *testing.T) {
	forNamespaces(t, "upload and try access on another namespace", func(tt *testing.T, namespace string) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/"+namespace+fNarXz).
			Body(string(testdata[fNarXz])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/"+namespace+fNar).
			Expect(tt).
			Header(headerContentType, mimeNar).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL("/another"+fNar).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(`not found`).
			Status(http.StatusNotFound).
			End()
	})
}

func insertFake(
	t *testing.T,
	store desync.WriteStore,
	indices map[string]desync.IndexWriteStore,
	path string) {

	name, index := findIndexByURL(path, indices)

	if chunker, err := desync.NewChunker(bytes.NewBuffer(testdata[path]), chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
		t.Fatal(err)
	} else if idx, err := desync.ChunkStream(context.Background(), chunker, store, 1); err != nil {
		t.Fatal(err)
	} else if rel, err := filepath.Rel("/", name); err != nil {
		t.Fatal(err)
	} else if err := index.StoreIndex(rel, idx); err != nil {
		t.Fatal(err)
	}
}

func insertNarinfos(t *testing.T, db *sqlx.DB, namespaces []string) {
	for _, namespace := range namespaces {
		narinfo := Narinfo{Namespace: namespace}
		if err := narinfo.Unmarshal(bytes.NewBuffer(testdata[fNarinfo])); err != nil {
			t.Fatalf("unmarshal narinfo: %q", err)
		} else if err := narinfo.dbInsert(db); err != nil {
			t.Fatalf("inserting narinfo: %q", err)
		}
	}
}
