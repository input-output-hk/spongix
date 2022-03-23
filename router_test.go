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
	"github.com/steinfletcher/apitest"
	"go.uber.org/zap"
)

var (
	testdata = map[string][]byte{}
	fNar     = "/nar/0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar"
	fNarXz   = "/nar/0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar.xz"
	fNarinfo = "/8ckxc8biqqfdwyhr0w70jgrcb4h7a4y5.narinfo"
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
	proxy.Substituters = []string{"http://example.com"}

	indexDir := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(filepath.Join(indexDir, "nar"), 0700); err != nil {
		panic(err)
	} else if proxy.localIndex, err = desync.NewLocalIndexStore(indexDir); err != nil {
		panic(err)
	}

	storeDir := filepath.Join(t.TempDir(), "store")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		panic(err)
	} else if proxy.localStore, err = desync.NewLocalStore(storeDir, defaultStoreOptions); err != nil {
		panic(err)
	}

	// proxy.s3Index = newFakeIndex()
	// proxy.s3Store = newFakeStore()
	proxy.Dir = t.TempDir()
	proxy.TrustedPublicKeys = []string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="}
	proxy.setupKeys()
	// NOTE: uncomment this line to enable logging
	proxy.log = zap.NewNop()
	return proxy
}

func withS3(proxy *Proxy) *Proxy {
	proxy.s3Index = newFakeIndex()
	proxy.s3Store = newFakeStore()
	return proxy
}

func TestRouterNixCacheInfo(t *testing.T) {
	proxy := testProxy(t)

	apitest.New().
		Handler(proxy.router()).
		Get("/nix-cache-info").
		Expect(t).
		Header(headerContentType, mimeNixCacheInfo).
		Body(`StoreDir: /nix/store
WantMassQuery: 1
Priority: 50`).
		Status(http.StatusOK).
		End()
}

func TestRouterNarinfoHead(t *testing.T) {
	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(``).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head(fNarinfo).
					RespondWith().
					Status(http.StatusOK).
					End(),
			).
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		insertFake(tt, proxy.localStore, proxy.localIndex, fNarinfo)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Index, fNarinfo)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarHead(t *testing.T) {
	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head(fNar+".xz").
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Head(fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End()).
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(``).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head(fNar+".xz").
					RespondWith().
					Status(http.StatusOK).
					End(),
				apitest.NewMock().
					Head(fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNar+".xz").
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		insertFake(tt, proxy.localStore, proxy.localIndex, fNar)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Index, fNar)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Head(fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("HEAD").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarGet(t *testing.T) {
	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Get(fNar+".xz").
					RespondWith().
					Status(http.StatusNotFound).
					End(),
				apitest.NewMock().
					Get(fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(`not found`).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found remote xz", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Get(fNar+".xz").
					RespondWith().
					Body(string(testdata[fNarXz])).
					Status(http.StatusOK).
					End(),
				apitest.NewMock().
					Get(fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNar+".xz").
			Header(headerContentType, mimeNar).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})

	t.Run("found remote xz and requested xz", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(
				apitest.NewMock().
					Get(fNarXz).
					RespondWith().
					Body(string(testdata[fNarXz])).
					Status(http.StatusOK).
					End(),
				apitest.NewMock().
					Get(fNar).
					RespondWith().
					Status(http.StatusNotFound).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL(fNarXz).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNar+".xz").
			Header(headerContentType, mimeNar).
			Body(string(testdata[fNarXz])).
			Status(http.StatusOK).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		insertFake(tt, proxy.localStore, proxy.localIndex, fNar)

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Index, fNar)

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNar).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarinfoGet(t *testing.T) {
	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(`not found`).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		insertFake(tt, proxy.localStore, proxy.localIndex, fNarinfo)

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Index, fNarinfo)

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			EnableMockResponseDelay().
			Mocks(
				apitest.NewMock().
					Get(fNarinfo).
					RespondWith().
					FixedDelay((1*time.Second).Milliseconds()).
					Body(string(testdata[fNarinfo])).
					Status(http.StatusOK).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("copies remote to local", func(tt *testing.T) {
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
					Get(fNarinfo).
					RespondWith().
					Body(string(testdata[fNarinfo])).
					Status(http.StatusOK).
					End(),
			).
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()

		for metricRemoteCachedOk.Get()+metricRemoteCachedFail.Get() == 0 {
			time.Sleep(1 * time.Millisecond)
		}

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarinfoPut(t *testing.T) {
	t.Run("upload success", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(fNarinfo).
			Body(string(testdata[fNarinfo])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerContentType, mimeNarinfo).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("upload invalid", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(fNarinfo).
			Body("blah").
			Expect(tt).
			Header(headerContentType, mimeText).
			Body(`Failed to parse line: "blah"`).
			Status(http.StatusBadRequest).
			End()
	})

	t.Run("upload unsigned", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(fNarinfo).
			Body("blah").
			Expect(tt).
			Header(headerContentType, mimeText).
			Body(`Failed to parse line: "blah"`).
			Status(http.StatusBadRequest).
			End()
	})

	t.Run("signs unsigned narinfos", func(tt *testing.T) {
		proxy := testProxy(tt)

		seed := make([]byte, ed25519.SeedSize)
		proxy.secretKeys["foo"] = ed25519.NewKeyFromSeed(seed)

		emptyInfo := &Narinfo{}
		if err := emptyInfo.Unmarshal(bytes.NewReader(testdata[fNarinfo])); err != nil {
			tt.Fatal(err)
		}
		emptyInfo.Sig = []string{}
		empty := &bytes.Buffer{}
		if err := emptyInfo.Marshal(empty); err != nil {
			tt.Fatal(err)
		}

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(fNarinfo).
			Body(empty.String()).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		expectInfo := &Narinfo{}
		if err := expectInfo.Unmarshal(bytes.NewReader(testdata[fNarinfo])); err != nil {
			tt.Fatal(err)
		}
		expectInfo.Sig = []string{"foo:MGrENumWZ1kbm23vCTyYrw6hRBJtLGIIpfHjpZszs2D1G1AALMKvl49T66WIhx2X02s8n/zsfUPpga2bL6PmBQ=="}
		expect := &bytes.Buffer{}
		if err := expectInfo.Marshal(expect); err != nil {
			tt.Fatal(err)
		}

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(expect.String()).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarPut(t *testing.T) {
	t.Run("upload success", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(fNar).
			Body(string(testdata[fNar])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNar).
			Expect(tt).
			Header(headerContentType, mimeNar).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})

	t.Run("upload xz success", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(fNarXz).
			Body(string(testdata[fNarXz])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNar).
			Expect(tt).
			Header(headerContentType, mimeNar).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})

	t.Run("upload xz to /cache success", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL("/cache"+fNarXz).
			Body(string(testdata[fNarXz])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body("ok\n").
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(fNar).
			Expect(tt).
			Header(headerContentType, mimeNar).
			Header(headerCache, headerCacheHit).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})
}

func insertFake(
	t *testing.T,
	store desync.WriteStore,
	index desync.IndexWriteStore,
	path string) {
	if chunker, err := desync.NewChunker(bytes.NewBuffer(testdata[path]), chunkSizeMin(), chunkSizeAvg, chunkSizeMax()); err != nil {
		t.Fatal(err)
	} else if idx, err := desync.ChunkStream(context.Background(), chunker, store, 1); err != nil {
		t.Fatal(err)
	} else if rel, err := filepath.Rel("/", path); err != nil {
		t.Fatal(err)
	} else if err := index.StoreIndex(rel, idx); err != nil {
		t.Fatal(err)
	}
}
