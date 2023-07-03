package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/input-output-hk/spongix/pkg/config"
	"github.com/pkg/errors"
	"github.com/steinfletcher/apitest"
	"gotest.tools/assert"
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
	fixtureNarXz   = []byte{
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
		Dir: t.TempDir(),
		Namespaces: map[string]config.Namespace{
			testNamespace: {
				Substituters:      []string{upstream},
				TrustedPublicKeys: []string{"cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="},
				CacheInfoPriority: 50,
			},
		},
	})

	// proxy.setupDesync()
	proxy.s3Store = newFakeStore()
	proxy.s3Index = newFakeIndex()
	go proxy.startCache()

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

		apitest.New().
			Mocks(mockHeadNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusFound).
			Header(headerLocation, upstream+fNarinfo).
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
			Mocks(mockHeadNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusFound).
			Header(headerLocation, upstream+fNarinfo).
			End()
	})

	t.Run("found remote after Narinfo HEAD", func(tt *testing.T) {
		proxy := testProxy(tt)

		// This unfortunately can't be easily deduplicated because for some reason
		// one cannot specify the `URL` when using `Method` on mocks.
		apitest.New().
			Mocks(mockHeadNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodHead).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusFound).
			Header(headerLocation, upstream+fNarinfo).
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
			Mocks(mockHeadNarinfo200).
			Handler(proxy.router()).
			Method(http.MethodGet).
			URL(nsNarinfo).
			Expect(tt).
			Status(http.StatusFound).
			Header(headerLocation, upstream+fNarinfo).
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

	t.Run("upload xz success", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method(http.MethodPut).
			URL(nsNarXz).
			Body(string(fixtureNarXz)).
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
			Body(string(fixtureNarXz)).
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
			return errors.WithMessage(err, "decoding expected")
		}

		actual := Realisation{}
		dec := json.NewDecoder(w.Body)
		if err := dec.Decode(&actual); err != nil {
			return errors.WithMessage(err, "decoding actual")
		}

		assert.Equal(t, reflect.DeepEqual(expected, actual), true)

		return nil
	}
}
