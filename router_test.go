package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/alitto/pond"
	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/config"
	"github.com/smarty/assertions"
	"github.com/steinfletcher/apitest"
	"gotest.tools/v3/assert"
)

var (
	fNarinfo           = "/hyhrnrnpsz9fw5p9dk85a58y31ink18c.narinfo"
	fixtureNarinfoNone = `StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar
Compression: none
FileHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
FileSize: 512
NarHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
NarSize: 512
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: kappa:JccDYkaQjN7ywE9VGJ6/RAzCt7XJoqWsmjTRsdAdM8DF40ebDDu3XWaasuJkaezbhVxjaRLJm3VWDEk6EmRpCw==
`

	fixtureNarinfoNoneUpstream = `StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar
Compression: none
FileHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
FileSize: 512
NarHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
NarSize: 512
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: kappa:JccDYkaQjN7ywE9VGJ6/RAzCt7XJoqWsmjTRsdAdM8DF40ebDDu3XWaasuJkaezbhVxjaRLJm3VWDEk6EmRpCw==
`

	fixtureLog = `some log`

	fNar       = "/nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar"
	fixtureNar = []byte{
		0x0d, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x6e, 0x69, 0x78, 0x2d, 0x61, 0x72, 0x63, 0x68,
		0x69, 0x76, 0x65, 0x2d, 0x31, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x28, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x74, 0x79, 0x70, 0x65, 0x00, 0x00, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x64, 0x69, 0x72, 0x65, 0x63, 0x74, 0x6f, 0x72, 0x79, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x65, 0x6e, 0x74, 0x72, 0x79, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x28, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x6e, 0x61, 0x6d, 0x65, 0x00, 0x00, 0x00, 0x00,
		0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x62, 0x69, 0x6e, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x6e, 0x6f, 0x64, 0x65, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x28, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x74, 0x79, 0x70, 0x65, 0x00, 0x00, 0x00, 0x00,
		0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x64, 0x69, 0x72, 0x65, 0x63, 0x74, 0x6f, 0x72,
		0x79, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x05, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x65, 0x6e, 0x74, 0x72, 0x79, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x28, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x6e, 0x61, 0x6d, 0x65, 0x00, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x68, 0x69, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x6e, 0x6f, 0x64, 0x65, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x28, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x74, 0x79, 0x70, 0x65, 0x00, 0x00, 0x00, 0x00, 0x07, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x73, 0x79, 0x6d, 0x6c, 0x69, 0x6e, 0x6b, 0x00, 0x06, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x74, 0x61, 0x72, 0x67, 0x65, 0x74, 0x00, 0x00, 0x39, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x2f, 0x6e, 0x69, 0x78, 0x2f, 0x73, 0x74, 0x6f, 0x72, 0x65, 0x2f, 0x35, 0x62, 0x34, 0x63, 0x70,
		0x72, 0x6a, 0x68, 0x6a, 0x77, 0x33, 0x35, 0x77, 0x79, 0x7a, 0x76, 0x67, 0x6d, 0x67, 0x76, 0x71,
		0x61, 0x79, 0x34, 0x68, 0x6a, 0x66, 0x35, 0x39, 0x68, 0x37, 0x78, 0x2d, 0x74, 0x65, 0x73, 0x74,
		0x2f, 0x62, 0x69, 0x6e, 0x2f, 0x74, 0x65, 0x73, 0x74, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x29, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x29, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x29, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x29, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x29, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	}

	fNarXz         = fNar + ".xz"
	fNarXzUpstream = "/nar/0rjb49w7dldq79d3ax30gw279bnbn48w9q925g3lr0rhrl0ycxvs.nar.xz"

	// Narinfo with xz compression, as served by upstream caches like cache.nixos.org.
	// URL points to a .nar.xz file; rewriteNarinfo will strip the extension.
	fixtureNarinfoXzUpstream = `StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar.xz
Compression: xz
FileHash: sha256:0rjb49w7dldq79d3ax30gw279bnbn48w9q925g3lr0rhrl0ycxvs
FileSize: 224
NarHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
NarSize: 512
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: kappa:JccDYkaQjN7ywE9VGJ6/RAzCt7XJoqWsmjTRsdAdM8DF40ebDDu3XWaasuJkaezbhVxjaRLJm3VWDEk6EmRpCw==
`

	fixtureNarXz = []byte{
		0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00, 0x00, 0x04, 0xe6, 0xd6, 0xb4, 0x46, 0x02, 0x00, 0x21, 0x01,
		0x16, 0x00, 0x00, 0x00, 0x74, 0x2f, 0xe5, 0xa3, 0xe0, 0x01, 0xff, 0x00, 0x9b, 0x5d, 0x00, 0x06,
		0x80, 0x36, 0x1f, 0xef, 0xa6, 0xce, 0xbf, 0x76, 0xba, 0x1a, 0x7a, 0x4f, 0x2b, 0xea, 0xa4, 0x53,
		0x24, 0xed, 0xa1, 0x23, 0x04, 0x71, 0xff, 0x5b, 0xbf, 0x97, 0x69, 0xba, 0xf3, 0x29, 0xbe, 0x25,
		0x7a, 0x71, 0x2b, 0xe9, 0xb4, 0xda, 0x0d, 0x7e, 0x9a, 0x8f, 0x9d, 0x5e, 0x24, 0x6d, 0xe2, 0x14,
		0x06, 0xd9, 0x0d, 0xdf, 0x06, 0x03, 0x65, 0xda, 0x60, 0x9a, 0xeb, 0x90, 0x01, 0xd6, 0x81, 0xe1,
		0x86, 0x82, 0x61, 0x5d, 0x1f, 0x0a, 0x64, 0x2c, 0xa8, 0x02, 0x61, 0x61, 0x36, 0x75, 0x72, 0xc6,
		0x52, 0x06, 0xc5, 0x9b, 0xa9, 0x2e, 0x1f, 0x1f, 0xb8, 0xc3, 0xb9, 0x90, 0x7d, 0xec, 0x38, 0x61,
		0xde, 0xab, 0x15, 0x77, 0x0c, 0x70, 0x75, 0x3c, 0xca, 0x33, 0x62, 0x92, 0x89, 0x31, 0xea, 0x53,
		0x4e, 0xb9, 0xf8, 0x9c, 0xfc, 0x3f, 0x23, 0xb5, 0x80, 0x92, 0x10, 0x0c, 0xe0, 0x7c, 0xca, 0xf3,
		0x2c, 0x0c, 0x34, 0x9e, 0x8b, 0x3f, 0x9d, 0x4b, 0x98, 0x36, 0xe0, 0x8f, 0xea, 0x49, 0x71, 0xc2,
		0x79, 0x53, 0xb1, 0xfc, 0x31, 0x59, 0xf1, 0xe8, 0x91, 0xa0, 0x00, 0x00, 0x65, 0xe0, 0x26, 0xb1,
		0x82, 0x0d, 0x97, 0xb8, 0x00, 0x01, 0xb7, 0x01, 0x80, 0x04, 0x00, 0x00, 0x47, 0xc0, 0xaa, 0x13,
		0xb1, 0xc4, 0x67, 0xfb, 0x02, 0x00, 0x00, 0x00, 0x00, 0x04, 0x59, 0x5a,
	}

	fRealisation       = "/realisations/sha256:4ef2eb79caed8101898542afdbc991f969d915773ece52dbf3b6cfa78fa08d92!out.doi"
	fLog               = "/log/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test.drv"
	fixtureRealisation = strings.TrimSpace(`
{
  "dependentRealisations": {},
  "id": "sha256:4ef2eb79caed8101898542afdbc991f969d915773ece52dbf3b6cfa78fa08d92!out",
  "outPath": "hyhrnrnpsz9fw5p9dk85a58y31ink18c-test",
  "signatures": [
    "kappa:oCQlzq3S7dzADbX5SeDhl95sQLbQYc1Pf5cmUuPDMLcIjI5iG8LxMojoQbhr9VgEWg/TZ5D7vCSaXknJyAEuBQ=="
  ]
}
`)

	suffix        = "/something"
	upstream      = "http://example.com" + suffix
	testNamespace = "test"

	nsNarinfo     = "/" + testNamespace + fNarinfo
	nsNar         = "/" + testNamespace + fNar
	nsNarXz       = "/" + testNamespace + fNarXz
	nsRealisation = "/" + testNamespace + fRealisation
	nsLog         = "/" + testNamespace + fLog

	mockGetNar200      = mockGet(suffix+fNar, 200).Body(string(fixtureNar)).Header(headerContentType, mimeNar).End()
	mockHeadNarinfo200 = mockHead(suffix+fNarinfo, 200).Body(fixtureNarinfoNoneUpstream).Header(headerContentType, mimeNarinfo).End()
	mockGetNarinfo200  = mockGet(suffix+fNarinfo, 200).Body(fixtureNarinfoNoneUpstream).Header(headerContentType, mimeNarinfo).End()
	mockGetNar404      = mockGet(suffix+fNar, 404).End()
	mockGetNarinfo404  = mockGet(suffix+fNarinfo, 404).End()

	mockGetNarXz404 = mockGet(suffix+fNarXzUpstream, 404).End()
)

func mockHead(url string, status int) *apitest.MockResponse {
	return apitest.NewMock().Head(url).RespondWith().Status(status)
}

func mockGet(url string, status int) *apitest.MockResponse {
	return apitest.NewMock().Get(url).RespondWith().Status(status)
}

func testProxy(t *testing.T) *Proxy {
	proxy := NewProxy(&config.Config{
		Chunks: &config.Chunks{
			S3: &config.S3{},
		},
		Namespaces: map[string]*config.Namespace{
			testNamespace: {
				Substituters:      []string{upstream},
				CacheInfoPriority: 50,
				S3:                &config.S3{},
			},
		},
	})

	a := assertions.New(t)
	a.So(proxy.config.Prepare(), assertions.ShouldBeNil)

	// proxy.setupDesync()
	proxy.chunkStore = newFakeStore()
	proxy.indexStores = map[string]desync.IndexWriteStore{testNamespace: newFakeIndex()}

	// proxy.setupKeys()

	// NOTE: comment the next line to enable logging
	// proxy.log = zap.NewNop()
	return proxy
}

func TestRouterNixCacheInfo(t *testing.T) {
	proxy := testProxy(t)

	apitest.New().
		Handler(proxy.router()).
		Get("/"+testNamespace+"/nix-cache-info").
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
			Method(http.MethodHead).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusNotFound).
			Header(headerContentType, mimeText).
			Body(``).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)

		// Narinfos are proxied (not redirected) so upstream content
		// is fetched, rewritten, and served directly.
		apitest.New().
			Mocks(mockHeadNarinfo200, mockGetNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarinfo).
			Body(fixtureNarinfoNone).
			Expect(tt).
			Status(http.StatusCreated).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			End()
	})
}

func TestRouterNarHead(t *testing.T) {
	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(mockGetNarXz404, mockGetNar404).
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNar).
			Expect(tt).
			Status(http.StatusNotFound).
			Body(``).
			End()
	})

	t.Run("not found because of missing remote narinfo", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(mockGetNar200, mockGetNarinfo404).
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNar).
			Expect(tt).
			Status(http.StatusNotFound).
			Body(``).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNar).
			Body(string(fixtureNar)).
			Expect(tt).
			Status(http.StatusCreated).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNar).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNar).
			Body(``).
			End()
	})
}

func TestRouterNarGet(t *testing.T) {
	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(mockGetNar404).
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNar).
			Expect(tt).
			Status(http.StatusNotFound).
			Header(headerContentType, mimeText).
			Body(``).
			End()
	})

	t.Run("found remote after Narinfo GET", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(mockHeadNarinfo200, mockGetNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			End()
	})

	t.Run("found remote after Narinfo HEAD", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Mocks(mockHeadNarinfo200, mockGetNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			End()
	})
}

func TestRouterNarinfoGet(t *testing.T) {
	t.Run("not found", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusNotFound).
			Header(headerCache, headerCacheMiss).
			Header(headerContentType, mimeText).
			Body(``).
			End()
	})

	t.Run("found remote", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			EnableMockResponseDelay().
			Mocks(mockHeadNarinfo200, mockGetNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			End()
	})
}

func TestRouterNarinfoPut(t *testing.T) {
	t.Run("upload success", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarinfo).
			Body(fixtureNarinfoNone).
			Expect(tt).
			Status(http.StatusCreated).
			Body(``).
			End()

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNarinfo).
			Body(fixtureNarinfoNone).
			End()
	})
}

func TestRouterNarPut(t *testing.T) {
	t.Run("upload success", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNar).
			Body(string(fixtureNar)).
			Expect(tt).
			Status(http.StatusCreated).
			Body("").
			End()

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNar).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeNar).
			Body(string(fixtureNar)).
			End()
	})

	t.Run("upload xz rejected", func(tt *testing.T) {
		proxy := testProxy(tt)

		// Compressed NAR uploads are rejected with 400
		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarXz).
			Body(string(fixtureNarXz)).
			Expect(tt).
			Status(http.StatusBadRequest).
			End()
	})
}

func TestRouterRealisationPut(t *testing.T) {
	t.Run("upload success", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsRealisation).
			Body(fixtureRealisation).
			Expect(tt).
			Status(http.StatusCreated).
			Body("").
			End()

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsRealisation).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeJson).
			Assert(realisationMatches(tt, []byte(fixtureRealisation))).
			End()
	})
}

func TestRouterLogPut(t *testing.T) {
	t.Run("upload success", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsLog).
			Body(fixtureLog).
			Expect(tt).
			Status(http.StatusCreated).
			Body("").
			End()

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsLog).
			Expect(tt).
			Status(http.StatusOK).
			Header(headerContentType, mimeText).
			Body(string(fixtureLog)).
			End()
	})
}

func realisationMatches(t *testing.T, expectedBody []byte) func(*http.Response, *http.Request) error {
	return func(w *http.Response, r *http.Request) error {
		expected := Realisation{}
		if err := json.Unmarshal(expectedBody, &expected); err != nil {
			return fmt.Errorf("decoding expected: %w", err)
		}

		actual := Realisation{}
		dec := json.NewDecoder(w.Body)
		if err := dec.Decode(&actual); err != nil {
			return fmt.Errorf("decoding actual: %w", err)
		}

		assert.Equal(t, reflect.DeepEqual(expected, actual), true)

		return nil
	}
}

func TestS3ErrorsMaskedAs404(t *testing.T) {
	proxy := testProxy(t)
	// Replace the index for test namespace with an errorFakeIndex that returns "connection refused"
	proxy.indexStores[testNamespace] = &errorFakeIndex{
		fakeIndex: newFakeIndex(),
		getError:  errors.New("connection refused"),
	}

	// GET a narinfo — a "connection refused" error from S3 should result in 500,
	// not 404. The bug at router.go:197 does string matching on the error and
	// treats all non-"key not found" errors as 404.
	apitest.New().
		Handler(proxy.router()).
		Method(http.MethodGet).
		URL(nsNarinfo).
		Expect(t).
		Status(http.StatusInternalServerError). // correct: S3 connection error → 500
		End()
}

func TestDoCacheTimeout(t *testing.T) {
	proxy := testProxy(t)
	fakeIdx := newFakeIndex()
	proxy.indexStores[testNamespace] = fakeIdx

	// Serve 1MB body slowly: write in chunks with delays, so the full read
	// takes ~12s — well within the 5min context timeout in doCache.
	body := make([]byte, 1024*1024)
	for i := range body {
		body[i] = byte(i % 256)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		chunkSize := len(body) / 4
		for i := 0; i < 4; i++ {
			start := i * chunkSize
			end := start + chunkSize
			if end > len(body) {
				end = len(body)
			}
			_, _ = w.Write(body[start:end])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if i < 3 {
				time.Sleep(4 * time.Second) // 3 sleeps × 4s = 12s total
			}
		}
	}))
	defer ts.Close()

	location := "indices/narinfo/test/test"
	start := time.Now()
	proxy.doCache(&cacheRequest{namespace: testNamespace, url: ts.URL, location: location})
	elapsed := time.Since(start)

	// With HTTP client timeout disabled and context set to 5min, the 12s
	// download should complete successfully (context is the sole timeout
	// authority and 12s is well within the 5min bound).
	if elapsed < 10*time.Second || elapsed > 20*time.Second {
		t.Errorf("doCache took %v, expected ~12s (download should complete within 5min context)", elapsed)
	}
}

func TestTrySubmitDropsJobsUnderLoad(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".narinfo") {
			w.Header().Set("Content-Type", mimeNarinfo)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fixtureNarinfoNone))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	proxy := testProxy(t)
	proxy.config.Namespaces[testNamespace].Substituters = []string{ts.URL}
	proxy.cachePool = pond.New(1, 1)

	fakeIdx := newFakeIndex()
	proxy.indexStores[testNamespace] = fakeIdx

	// Fill the pool completely: 1 worker blocked + 1 queue slot filled.
	blocker := make(chan struct{})
	proxy.cachePool.Submit(func() { <-blocker })    // blocks the only worker
	proxy.cachePool.TrySubmit(func() { <-blocker }) // fills the queue
	defer close(blocker)

	// GET request triggers proxy + background TrySubmit.
	// Pool is saturated so TrySubmit returns false and the cache job is dropped.
	// The client still receives 200 with the proxied narinfo.
	req, _ := http.NewRequest(http.MethodGet, nsNarinfo, nil)
	rr := httptest.NewRecorder()
	proxy.router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	time.Sleep(50 * time.Millisecond)

	_, err := fakeIdx.GetIndex("indices/narinfo/hyhr/hyhrnrnpsz9fw5p9dk85a58y31ink18c")
	if err == nil {
		t.Error("expected cache job to be dropped under load, but index was stored")
	}
}

// TestCacheSubmitDoesNotLeakGoroutine verifies that when the cachePool is
// saturated, no goroutine is left blocking on pool.Submit. With the old code
// (go func() { cachePool.Submit(...) }()), the goroutine blocked indefinitely
// until pool capacity was available. The fix is to use cachePool.TrySubmit,
// which drops the job but never leaks a goroutine.
func TestCacheSubmitDoesNotLeakGoroutine(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".narinfo") {
			w.Header().Set("Content-Type", mimeNarinfo)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fixtureNarinfoNone))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	proxy := testProxy(t)
	proxy.config.Namespaces[testNamespace].Substituters = []string{ts.URL}
	// 1 worker, 0 queue: pool is at capacity as soon as the single worker is busy.
	proxy.cachePool = pond.New(1, 0)
	blocker := make(chan struct{})
	proxy.cachePool.Submit(func() { <-blocker })
	defer close(blocker)

	// GET request finds narinfo upstream and attempts a background cache job.
	// With blocking Submit the goroutine stays alive until the pool drains.
	// With TrySubmit the goroutine exits immediately (job dropped, no leak).
	req, _ := http.NewRequest(http.MethodGet, nsNarinfo, nil)
	rr := httptest.NewRecorder()
	proxy.router().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Close idle HTTP connections so their goroutines don't appear in the dump.
	proxy.httpHeadClient.CloseIdleConnections()
	proxy.httpGetClient.CloseIdleConnections()
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)

	// A goroutine blocked inside pond.Submit (channel send) is the leak.
	// With TrySubmit there is no blocking call so this string never appears.
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	dump := string(buf[:n])
	if strings.Contains(dump, "pond.(*WorkerPool).Submit") {
		t.Errorf("goroutine blocking on cachePool.Submit (pool is full):\n%s", dump)
	}
}

// TestNarProxyDecompressOnCacheMiss verifies that when a narinfo on upstream
// advertises a compressed NAR (Compression: xz, URL: nar/HASH.nar.xz),
// Spongix rewrites the narinfo to uncompressed (Compression: none, URL: nar/HASH.nar),
// and then successfully serves the uncompressed NAR by proxying + decompressing
// from upstream. This is the bug where Spongix returned 404 for the uncompressed
// NAR URL because it only tried HEAD on the exact (uncompressed) URL upstream.
func TestNarProxyDecompressOnCacheMiss(t *testing.T) {
	// Upstream that serves xz-compressed narinfo and NAR.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".narinfo"):
			w.Header().Set("Content-Type", mimeNarinfo)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fixtureNarinfoXzUpstream))
		case strings.HasSuffix(r.URL.Path, ".nar.xz"):
			w.Header().Set("Content-Type", "application/x-xz")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixtureNarXz)
		case strings.HasSuffix(r.URL.Path, ".nar"):
			// Upstream does NOT have the uncompressed NAR.
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	proxy := testProxy(t)
	proxy.config.Namespaces[testNamespace].Substituters = []string{ts.URL}

	router := proxy.router()

	// Step 1: GET narinfo -- should be proxied and rewritten to uncompressed.
	rr := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, nsNarinfo, nil)
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("narinfo: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Compression: none") {
		t.Fatalf("narinfo not rewritten to Compression: none:\n%s", body)
	}
	if strings.Contains(body, ".nar.xz") {
		t.Fatalf("narinfo URL still has .xz extension:\n%s", body)
	}

	// Step 2: GET the uncompressed NAR URL (as a real Nix client would).
	// This is the critical test: upstream only has .nar.xz, Spongix must
	// proxy it, decompress, and serve uncompressed.
	rr2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, nsNar, nil)
	router.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("NAR: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	if rr2.Body.Len() != len(fixtureNar) {
		t.Errorf("NAR body length: got %d, want %d", rr2.Body.Len(), len(fixtureNar))
	}
	if !bytes.Equal(rr2.Body.Bytes(), fixtureNar) {
		t.Error("NAR body does not match expected decompressed content")
	}

	// Step 3: GET the NAR again -- should be served from local cache now.
	// Stop the upstream server first to prove we're not re-fetching.
	ts.Close()

	rr3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodGet, nsNar, nil)
	router.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Fatalf("NAR (cached): expected 200, got %d: %s", rr3.Code, rr3.Body.String())
	}
	if !bytes.Equal(rr3.Body.Bytes(), fixtureNar) {
		t.Error("NAR (cached): body does not match expected content")
	}
}

// TestDoCacheEagerNarCachingWithPathPrefix verifies that when doCache eagerly
// caches a NAR from a compressed upstream narinfo, it constructs the correct
// NAR download URL even when the substituter has a path prefix. Exposes the
// extractBaseURL bug: it strips the path prefix, causing the eager NAR download
// to hit the wrong URL and silently fail.
func TestDoCacheEagerNarCachingWithPathPrefix(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".narinfo"):
			if !strings.HasPrefix(r.URL.Path, "/something/") {
				t.Errorf("narinfo request missing path prefix: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", mimeNarinfo)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(fixtureNarinfoXzUpstream))

		case strings.HasSuffix(r.URL.Path, ".nar.xz"):
			if !strings.HasPrefix(r.URL.Path, "/something/") {
				t.Errorf("NAR request missing path prefix: %s (extractBaseURL bug)", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/x-xz")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fixtureNarXz)

		case strings.HasSuffix(r.URL.Path, ".nar"):
			w.WriteHeader(http.StatusNotFound)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	proxy := testProxy(t)
	proxy.config.Namespaces[testNamespace].Substituters = []string{ts.URL + "/something"}

	router := proxy.router()

	// Step 1: GET narinfo triggers proxyNarinfo + background doCache
	rr1 := httptest.NewRecorder()
	req1, _ := http.NewRequest(http.MethodGet, nsNarinfo, nil)
	router.ServeHTTP(rr1, req1)

	if rr1.Code != http.StatusOK {
		t.Fatalf("narinfo proxy: expected 200, got %d: %s", rr1.Code, rr1.Body.String())
	}
	if !strings.Contains(rr1.Body.String(), "Compression: none") {
		t.Fatalf("narinfo not rewritten to Compression: none:\n%s", rr1.Body.String())
	}

	// Step 2: Drain cachePool so doCache completes
	time.Sleep(200 * time.Millisecond)
	proxy.cachePool.StopAndWait()
	proxy.cachePool = pond.New(10, 1000)

	// Step 3: Shut down upstream -- everything after must come from cache
	ts.Close()

	// Step 4: GET narinfo -- should be cache HIT
	rr2 := httptest.NewRecorder()
	req2, _ := http.NewRequest(http.MethodGet, nsNarinfo, nil)
	router.ServeHTTP(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("narinfo from cache: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
	if rr2.Header().Get(headerCache) != headerCacheHit {
		t.Errorf("expected X-Cache: HIT, got %s", rr2.Header().Get(headerCache))
	}

	// Step 5: GET NAR -- should be cache HIT from doCache's eager caching.
	// BUG: extractBaseURL lost the path prefix, so doCache downloaded from
	// the wrong URL (404), and the NAR was never cached.
	rr3 := httptest.NewRecorder()
	req3, _ := http.NewRequest(http.MethodGet, nsNar, nil)
	router.ServeHTTP(rr3, req3)

	if rr3.Code != http.StatusOK {
		t.Fatalf("NAR from cache: expected 200, got %d.\n"+
			"extractBaseURL bug: doCache constructed wrong NAR URL "+
			"by stripping the path prefix from the substituter URL.", rr3.Code)
	}
	if !bytes.Equal(rr3.Body.Bytes(), fixtureNar) {
		t.Error("NAR body does not match expected decompressed content")
	}
}

func TestRedirectToUpstreamGroupWaitBlocking(t *testing.T) {
	// Start a test server that sleeps 800ms then returns 404
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	proxy := testProxy(t)
	// Replace substituters with 3 copies of the slow upstream
	proxy.config.Namespaces[testNamespace].Substituters = []string{
		ts.URL, ts.URL, ts.URL,
	}

	start := time.Now()
	apitest.New().
		Handler(proxy.router()).
		Method(http.MethodHead).
		URL(nsNarinfo).
		Expect(t).
		Status(http.StatusNotFound).
		End()
	elapsed := time.Since(start)

	// The 500ms timeout at router.go:172 should cap the total wait. But because
	// group.Wait() runs BEFORE the select (not concurrently), the timeout is
	// additive: ~800ms for group.Wait + 500ms select = ~1300ms total.
	if elapsed > 600*time.Millisecond {
		t.Errorf("HEAD request took %v, but should complete within 600ms "+
			"(BUG: group.Wait() at router.go:172 blocks before the select timeout, "+
			"making the 500ms timeout additive not concurrent)", elapsed)
	}
}
