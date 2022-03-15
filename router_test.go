package main

import (
	"bytes"
	"crypto/ed25519"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steinfletcher/apitest"
)

var (
	testdata = map[string][]byte{}
	fNar     = "0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar"
	fNarXz   = "0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar.xz"
	fNarinfo = "8ckxc8biqqfdwyhr0w70jgrcb4h7a4y5.narinfo"
)

func TestMain(m *testing.M) {
	narinfoGetTimeout = 2 * time.Second

	walkErr := filepath.WalkDir("testdata", func(path string, info fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := filepath.Base(path)

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		testdata[base] = content

		return nil
	})

	if walkErr != nil {
		panic(walkErr)
	}

	os.Exit(m.Run())
}

func testProxy(t *testing.T) *Proxy {
	proxy := NewProxy()
	proxy.Substituters = []string{"http://example.com"}
	proxy.localIndex = newFakeIndex()
	proxy.localStore = newFakeStore()
	// proxy.s3Index = newFakeIndex()
	// proxy.s3Store = newFakeStore()
	proxy.Dir = t.TempDir()
	proxy.TrustedPublicKeys = []string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="}
	proxy.SetupKeys()
	proxy.startNarFetchers()
	// proxy.log = zap.NewNop()
	return proxy
}

func withS3(proxy *Proxy) *Proxy {
	proxy.s3Index = newFakeIndex()
	proxy.s3Store = newFakeStore()
	return proxy
}

func TestRouterNixCacheInfo(t *testing.T) {
	proxy := testProxy(t)
	defer proxy.log.Sync()

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
	url := "/" + fNarinfo

	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(``).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Head(url).
			RespondWith().
			Status(http.StatusOK).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com/"+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()
		insertFake(tt, proxy.localStore, proxy.localIndex, fNarinfo, testdata[fNarinfo])

		mock := apitest.NewMock().
			Head("/" + fNarinfo).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		defer proxy.log.Sync()
		insertFake(tt, proxy.s3Store, proxy.s3Index, fNarinfo, testdata[fNarinfo])

		mock := apitest.NewMock().
			Head("/" + fNarinfo).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarHead(t *testing.T) {
	url := "/nar/" + fNar

	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Head(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(``).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock1 := apitest.NewMock().
			Head(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		mock2 := apitest.NewMock().
			Head(url + ".xz").
			RespondWith().
			Status(http.StatusOK).
			End()

		apitest.New().
			Mocks(mock2, mock1).
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com/nar/"+fNar+".xz").
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()
		insertFake(tt, proxy.localStore, proxy.localIndex, "nar/"+fNar, testdata[fNar])

		mock := apitest.NewMock().
			Head(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		defer proxy.log.Sync()
		insertFake(tt, proxy.s3Store, proxy.s3Index, "nar/"+fNar, testdata[fNar])

		mock := apitest.NewMock().
			Head(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("HEAD").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarGet(t *testing.T) {
	url := "/nar/" + fNar

	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(`not found`).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found remote xz", func(tt *testing.T) {
		proxy := testProxy(tt)
		proxy.parallelRequestOrdered = true
		defer proxy.log.Sync()

		mock1 := apitest.NewMock().
			Get(url).
			RespondWith().
			Body(``).
			Status(http.StatusNotFound).
			End()

		mock2 := apitest.NewMock().
			Get(url + ".xz").
			RespondWith().
			Body(string(testdata[fNarXz])).
			Status(http.StatusOK).
			End()

		apitest.New().
			Mocks(mock2, mock1).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com/nar/"+fNar+".xz").
			Header(headerContentType, mimeNar).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()
		insertFake(tt, proxy.localStore, proxy.localIndex, "nar/"+fNar, testdata[fNar])

		mock := apitest.NewMock().
			Head(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		defer proxy.log.Sync()
		insertFake(tt, proxy.s3Store, proxy.s3Index, "nar/"+fNar, testdata[fNar])

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNar).
			Body(``).
			Status(http.StatusOK).
			End()

		idx, err := proxy.s3Index.GetIndex("nar/" + fNar)
		if err != nil {
			tt.Error(err)
		}
		hasChunk, err := proxy.s3Store.HasChunk(idx.Chunks[0].ID)
		if err != nil {
			tt.Error(err)
		}
		if !hasChunk {
			tt.Error("Chunk not present in s3 store")
		}
	})
}

func TestRouterNarinfoGet(t *testing.T) {
	url := "/" + fNarinfo

	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(`not found`).
			Status(http.StatusNotFound).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)
		insertFake(tt, proxy.localStore, proxy.localIndex, fNarinfo, testdata[fNarinfo])
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Index, fNarinfo, testdata[fNarinfo])
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			Status(http.StatusNotFound).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			FixedDelay((narinfoGetTimeout - (1 * time.Second)).Milliseconds()).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()

		apitest.New().
			EnableMockResponseDelay().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+url).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("copies remote to local", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			FixedDelay((narinfoGetTimeout - (500 * time.Millisecond)).Milliseconds()).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()

		apitest.New().
			EnableMockResponseDelay().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+url).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(string(testdata[fNarinfo])).
			Status(http.StatusOK).
			End()
	})

	t.Run("signs unsigned narinfos", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		seed := make([]byte, ed25519.SeedSize)
		proxy.secretKeys["foo"] = ed25519.NewKeyFromSeed(seed)

		emptyInfo := &Narinfo{}
		if err := emptyInfo.Unmarshal(bytes.NewReader(testdata[fNarinfo])); err != nil {
			tt.Error(err)
		}
		emptyInfo.Sig = []string{}
		empty := &bytes.Buffer{}
		if err := emptyInfo.Marshal(empty); err != nil {
			tt.Error(err)
		}

		expectInfo := &Narinfo{}
		if err := expectInfo.Unmarshal(bytes.NewReader(testdata[fNarinfo])); err != nil {
			tt.Error(err)
		}
		expectInfo.Sig = []string{"foo:MGrENumWZ1kbm23vCTyYrw6hRBJtLGIIpfHjpZszs2D1G1AALMKvl49T66WIhx2X02s8n/zsfUPpga2bL6PmBQ=="}
		expect := &bytes.Buffer{}
		if err := expectInfo.Marshal(expect); err != nil {
			tt.Error(err)
		}

		mock := apitest.NewMock().
			Get(url).
			RespondWith().
			Body(empty.String()).
			Status(http.StatusOK).
			End()

		apitest.New().
			Mocks(mock).
			Handler(proxy.router()).
			Method("GET").
			URL(url).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, "http://example.com"+url).
			Header(headerContentType, mimeNarinfo).
			Body(string(expect.String())).
			Status(http.StatusOK).
			End()
	})
}

func TestRouterNarinfoPut(t *testing.T) {
	url := "/" + fNarinfo

	t.Run("upload success", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		defer proxy.log.Sync()

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(url).
			Body(string(testdata[fNarinfo])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body(`ok`).
			Status(http.StatusOK).
			End()

		idx, err := proxy.localIndex.GetIndex(fNarinfo)
		if err != nil {
			tt.Error(err)
		}
		hasChunk, err := proxy.localStore.HasChunk(idx.Chunks[0].ID)
		if err != nil {
			tt.Error(err)
		}
		if !hasChunk {
			tt.Error("Chunk not present in local store")
		}

		idx, err = proxy.s3Index.GetIndex(fNarinfo)
		if err != nil {
			tt.Error(err)
		}
		hasChunk, err = proxy.s3Store.HasChunk(idx.Chunks[0].ID)
		if err != nil {
			tt.Error(err)
		}
		if !hasChunk {
			tt.Error("Chunk not present in s3 store")
		}
	})

	t.Run("upload invalid", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(url).
			Body("blah").
			Expect(tt).
			Header(headerContentType, mimeText).
			Body(`Failed to parse line: "blah"`).
			Status(http.StatusBadRequest).
			End()
	})
}

func TestRouterNarPut(t *testing.T) {
	url := "/nar/" + fNar

	t.Run("upload success", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		defer proxy.log.Sync()

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(url).
			Body(string(testdata[fNar])).
			Expect(tt).
			Header(headerContentType, mimeText).
			Body(`ok`).
			Status(http.StatusOK).
			End()

		idx, err := proxy.localIndex.GetIndex("nar/" + fNar)
		if err != nil {
			tt.Error(err)
		}
		hasChunk, err := proxy.localStore.HasChunk(idx.Chunks[0].ID)
		if err != nil {
			tt.Error(err)
		}
		if !hasChunk {
			tt.Error("Chunk not present in local store")
		}

		idx, err = proxy.s3Index.GetIndex("nar/" + fNar)
		if err != nil {
			tt.Error(err)
		}
		hasChunk, err = proxy.s3Store.HasChunk(idx.Chunks[0].ID)
		if err != nil {
			tt.Error(err)
		}
		if !hasChunk {
			tt.Error("Chunk not present in s3 store")
		}
	})
}

func TestStoryRemoteNarWithXz(t *testing.T) {
	// url := "/nar/" + fNar

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)
		defer proxy.log.Sync()

		mock1 := apitest.NewMock().
			Get("/nar/0a0i3igcs6ri8crf4cd8gxc67zrnxh7pdq0gs0gdmw9mb86m5mhz.nar").
			RespondWith().
			Status(http.StatusNotFound).
			End()

		mock2 := apitest.NewMock().
			Get("/nar/0a0i3igcs6ri8crf4cd8gxc67zrnxh7pdq0gs0gdmw9mb86m5mhz.nar.xz").
			RespondWith().
			Body(string(testdata[fNarXz])).
			Status(http.StatusOK).
			End()

		apitest.New().
			Report(apitest.SequenceDiagram()).
			Mocks(mock2, mock1).
			Handler(proxy.router()).
			Method("GET").
			URL("/nar/0a0i3igcs6ri8crf4cd8gxc67zrnxh7pdq0gs0gdmw9mb86m5mhz.nar").
			Body(string(testdata[fNar])).
			Expect(tt).
			Header(headerContentType, mimeNar).
			Body(string(testdata[fNar])).
			Status(http.StatusOK).
			End()
	})
}
