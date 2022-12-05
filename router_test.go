package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"crawshaw.io/sqlite"
	"github.com/folbricht/desync"
	"github.com/input-output-hk/spongix/pkg/config"
	"github.com/steinfletcher/apitest"
)

var (
	testdata      = map[string][]byte{}
	fNar          = "/nar/0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar"
	fNarXz        = "/nar/0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar.xz"
	fNarinfo      = "/8ckxc8biqqfdwyhr0w70jgrcb4h7a4y5.narinfo"
	fRealisation  = "/realisations/sha256:b95e6ccddcbc1df53705c1d66e96e6afd19f2629885755e98972e9b95d18cfa8!out.doi"
	testNamespace = "test"
	nsNarinfo     = "/" + testNamespace + fNarinfo
	nsNarXz       = "/" + testNamespace + fNarXz
	nsNar         = "/" + testNamespace + fNar
	nsRealisation = "/" + testNamespace + fRealisation
	upstream      = "http://example.com"
)

func TestMain(m *testing.M) {
	fixtures := []string{
		fNar, fNarXz, fNarinfo, fRealisation,
	}

	for _, name := range fixtures {
		content, err := os.ReadFile(filepath.Join("testdata", filepath.Base(name)))
		if err != nil {
			panic(err)
		}

		testdata[name] = content
	}

	os.Exit(m.Run())
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

	proxy.setupDesync()
	proxy.migrate()

	// proxy.s3Index = newFakeIndex()
	// proxy.s3Store = newFakeStore()
	// proxy.setupKeys()

	// NOTE: comment the next line to enable logging
	// proxy.log = zap.NewNop()
	return proxy
}

func withS3(proxy *Proxy) *Proxy {
	for namespace := range proxy.config.Namespaces {
		proxy.s3Indices[namespace] = newFakeIndex()
	}
	proxy.s3Store = newFakeStore()
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
			Method("HEAD").
			URL(nsNarinfo).
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
					Header(headerContentType, mimeNarinfo).
					Status(http.StatusOK).
					End(),
			).
			Handler(proxy.router()).
			Method("HEAD").
			URL(nsNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheRemote).
			Header(headerCacheUpstream, upstream+fNarinfo).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found local", func(tt *testing.T) {
		proxy := testProxy(tt)

		apitest.New().
			Handler(proxy.router()).
			Method("PUT").
			URL(nsNarinfo).
			Body(string(testdata[fNarinfo])).
			Expect(tt).
			Status(http.StatusOK).
			End()

		proxy.inspectTable("files")
		proxy.inspectTable("indices")

		dbErr := proxy.withDbReadOnly(func(db *sqlite.Conn) error {
			query := db.Prep(`
			SELECT
			COALESCE(files.content_type, indices.content_type)
			FROM files, indices
			WHERE (  files.namespace IS :namespace AND   files.url IS :url) 
			   OR (indices.namespace IS :namespace AND indices.url IS :url)
			LIMIT 1
`)

			query.SetText(":namespace", testNamespace)
			query.SetText(":url", "8ckxc8biqqfdwyhr0w70jgrcb4h7a4y5.narinfo")

			if err := query.Reset(); err != nil {
				return err
			}

			rowReturned, err := query.Step()
			if !rowReturned {
				pp("no rows")
				return nil
			}

			if err != nil {
				return err
			}

			for col := 0; col < query.ColumnCount(); col += 1 {
				switch query.ColumnType(col) {
				case sqlite.SQLITE_INTEGER:
					pp(query.ColumnName(col), ":", query.ColumnInt(col))
				case sqlite.SQLITE_FLOAT:
					pp(query.ColumnName(col), ":", query.ColumnFloat(col))
				case sqlite.SQLITE_TEXT:
					pp(query.ColumnName(col), ":", query.ColumnText(col))
				case sqlite.SQLITE_BLOB:
					head := make([]byte, 1024)
					query.ColumnBytes(col, head)
					pp(query.ColumnName(col), ":", head)
				case sqlite.SQLITE_NULL:
					pp(query.ColumnName(col), ":", "NULL")
				}
			}

			return nil
		})

		if dbErr != nil {
			panic(dbErr)
		}

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL(nsNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})

	t.Run("found s3", func(tt *testing.T) {
		proxy := withS3(testProxy(tt))
		insertFake(tt, proxy.s3Store, proxy.s3Indices[testNamespace], fNarinfo)

		apitest.New().
			Handler(proxy.router()).
			Method("HEAD").
			URL(nsNarinfo).
			Expect(tt).
			Header(headerCache, headerCacheHit).
			Header(headerContentType, mimeNarinfo).
			Body(``).
			Status(http.StatusOK).
			End()
	})
}

// func TestRouterNarHead(t *testing.T) {
// 	t.Run("not found", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Mocks(
// 				apitest.NewMock().
// 					Head(fNarXz).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 				apitest.NewMock().
// 					Head(fNar).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End()).
// 			Handler(proxy.router()).
// 			Method("HEAD").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheMiss).
// 			Header(headerContentType, mimeText).
// 			Body(``).
// 			Status(http.StatusNotFound).
// 			End()
// 	})
//
// 	t.Run("found remote", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Mocks(
// 				apitest.NewMock().
// 					Head(fNarXz).
// 					RespondWith().
// 					Status(http.StatusOK).
// 					End(),
// 				apitest.NewMock().
// 					Head(fNar).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 			).
// 			Handler(proxy.router()).
// 			Method("HEAD").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheRemote).
// 			Header(headerCacheUpstream, upstream+fNarXz).
// 			Header(headerContentType, mimeNar).
// 			Body(``).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("found local", func(tt *testing.T) {
// 		proxy := testProxy(tt)
// 		insertFake(tt, proxy.localStore, proxy.localIndices[testNamespace], fNar)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("HEAD").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNar).
// 			Body(``).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("found s3", func(tt *testing.T) {
// 		proxy := withS3(testProxy(tt))
// 		insertFake(tt, proxy.s3Store, proxy.s3Indices[testNamespace], fNar)
//
// 		apitest.New().
// 			Mocks(
// 				apitest.NewMock().
// 					Head(fNar).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 			).
// 			Mocks(
// 				apitest.NewMock().
// 					Head(fNarXz).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 			).
// 			Handler(proxy.router()).
// 			Method("HEAD").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNar).
// 			Body(``).
// 			Status(http.StatusOK).
// 			End()
// 	})
// }
//
// func TestRouterNarGet(t *testing.T) {
// 	t.Run("not found", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Mocks(
// 				apitest.NewMock().
// 					Get(fNarXz).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 				apitest.NewMock().
// 					Get(fNar).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 			).
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheMiss).
// 			Header(headerContentType, mimeText).
// 			Body(`not found`).
// 			Status(http.StatusNotFound).
// 			End()
// 	})
//
// 	t.Run("found remote xz", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Mocks(
// 				apitest.NewMock().
// 					Get(fNarXz).
// 					RespondWith().
// 					Body(string(testdata[fNarXz])).
// 					Status(http.StatusOK).
// 					End(),
// 				apitest.NewMock().
// 					Get(fNar).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 			).
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheRemote).
// 			Header(headerCacheUpstream, upstream+fNarXz).
// 			Header(headerContentType, mimeNar).
// 			Body(string(testdata[fNar])).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("found remote xz and requested xz", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Mocks(
// 				apitest.NewMock().
// 					Get(fNarXz).
// 					RespondWith().
// 					Body(string(testdata[fNarXz])).
// 					Status(http.StatusOK).
// 					End(),
// 				apitest.NewMock().
// 					Get(fNar).
// 					RespondWith().
// 					Status(http.StatusNotFound).
// 					End(),
// 			).
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarXz).
// 			Expect(tt).
// 			Header(headerCache, headerCacheRemote).
// 			Header(headerCacheUpstream, upstream+fNarXz).
// 			Header(headerContentType, mimeNar).
// 			Body(string(testdata[fNarXz])).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("found local", func(tt *testing.T) {
// 		proxy := testProxy(tt)
// 		insertFake(tt, proxy.localStore, proxy.localIndices[testNamespace], fNar)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNar).
// 			Body(``).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("found s3", func(tt *testing.T) {
// 		proxy := withS3(testProxy(tt))
// 		insertFake(tt, proxy.s3Store, proxy.s3Indices[testNamespace], fNar)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNar).
// 			Body(``).
// 			Status(http.StatusOK).
// 			End()
// 	})
// }
//
// func TestRouterNarinfoGet(t *testing.T) {
// 	t.Run("not found", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerCache, headerCacheMiss).
// 			Header(headerContentType, mimeText).
// 			Body(`not found`).
// 			Status(http.StatusNotFound).
// 			End()
// 	})
//
// 	t.Run("found local", func(tt *testing.T) {
// 		proxy := testProxy(tt)
// 		insertFake(tt, proxy.localStore, proxy.localIndices[testNamespace], fNarinfo)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNarinfo).
// 			Body(string(testdata[fNarinfo])).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("found s3", func(tt *testing.T) {
// 		proxy := withS3(testProxy(tt))
// 		insertFake(tt, proxy.s3Store, proxy.s3Indices[testNamespace], fNarinfo)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNarinfo).
// 			Body(string(testdata[fNarinfo])).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("found remote", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			EnableMockResponseDelay().
// 			Mocks(
// 				apitest.NewMock().
// 					Get(fNarinfo).
// 					RespondWith().
// 					FixedDelay((1*time.Second).Milliseconds()).
// 					Body(string(testdata[fNarinfo])).
// 					Status(http.StatusOK).
// 					End(),
// 			).
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerCache, headerCacheRemote).
// 			Header(headerCacheUpstream, upstream+fNarinfo).
// 			Header(headerContentType, mimeNarinfo).
// 			Body(string(testdata[fNarinfo])).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("copies remote to local", func(tt *testing.T) {
// 		proxy := testProxy(tt)
// 		// go proxy.startCache()
// 		defer close(proxy.cacheChan)
//
// 		mockReset := apitest.NewStandaloneMocks(
// 			apitest.NewMock().
// 				Get(upstream + fNarinfo).
// 				RespondWith().
// 				Body(string(testdata[fNarinfo])).
// 				Status(http.StatusOK).
// 				End(),
// 		).End()
// 		defer mockReset()
//
// 		apitest.New().
// 			Mocks(
// 				apitest.NewMock().
// 					Get(fNarinfo).
// 					RespondWith().
// 					Body(string(testdata[fNarinfo])).
// 					Status(http.StatusOK).
// 					End(),
// 			).
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerCache, headerCacheRemote).
// 			Header(headerCacheUpstream, upstream+fNarinfo).
// 			Header(headerContentType, mimeNarinfo).
// 			Body(string(testdata[fNarinfo])).
// 			Status(http.StatusOK).
// 			End()
//
// 		for metricRemoteCachedOk.Get()+metricRemoteCachedFail.Get() == 0 {
// 			time.Sleep(1 * time.Second)
// 		}
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNarinfo).
// 			Body(string(testdata[fNarinfo])).
// 			Status(http.StatusOK).
// 			End()
// 	})
// }
//
// func TestRouterNarinfoPut(t *testing.T) {
// 	t.Run("upload success", func(tt *testing.T) {
// 		proxy := withS3(testProxy(tt))
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("PUT").
// 			URL(nsNarinfo).
// 			Body(string(testdata[fNarinfo])).
// 			Expect(tt).
// 			Header(headerContentType, mimeText).
// 			Body("ok\n").
// 			Status(http.StatusOK).
// 			End()
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerContentType, mimeNarinfo).
// 			Header(headerCache, headerCacheHit).
// 			Body(string(testdata[fNarinfo])).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("upload invalid", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("PUT").
// 			URL(nsNarinfo).
// 			Body("blah").
// 			Expect(tt).
// 			Header(headerContentType, mimeText).
// 			Body(`unable to find separator ': ' in blah`).
// 			Status(http.StatusBadRequest).
// 			End()
// 	})
//
// 	t.Run("upload unsigned", func(tt *testing.T) {
// 		proxy := testProxy(tt)
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("PUT").
// 			URL(nsNarinfo).
// 			Body("blah").
// 			Expect(tt).
// 			Header(headerContentType, mimeText).
// 			Body(`unable to find separator ': ' in blah`).
// 			Status(http.StatusBadRequest).
// 			End()
// 	})
//
// 	t.Run("signs unsigned narinfos", func(tt *testing.T) {
// 		rng := rand.New(rand.NewSource(42))
// 		proxy := testProxy(tt)
//
// 		sec, pub, err := signature.GenerateKeypair("test", rng)
// 		if err != nil {
// 			tt.Fatal(err)
// 		}
//
// 		proxy.secretKeys[testNamespace] = sec
// 		_ = pub
//
// 		emptyInfo, err := narinfo.Parse(bytes.NewReader(testdata[fNarinfo]))
// 		if err != nil {
// 			tt.Fatal(err)
// 		}
// 		emptyInfo.Signatures = []signature.Signature{}
// 		empty := emptyInfo.String()
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("PUT").
// 			URL(nsNarinfo).
// 			Body(empty).
// 			Expect(tt).
// 			Header(headerContentType, mimeText).
// 			Body("ok\n").
// 			Status(http.StatusOK).
// 			End()
//
// 		expectInfo, err := narinfo.Parse(bytes.NewReader(testdata[fNarinfo]))
// 		if err != nil {
// 			tt.Fatal(err)
// 		}
// 		sig, err := sec.Sign(rng, emptyInfo.Fingerprint())
// 		if err != nil {
// 			tt.Fatal(err)
// 		}
// 		expectInfo.Signatures = []signature.Signature{sig}
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNarinfo).
// 			Expect(tt).
// 			Header(headerCache, headerCacheHit).
// 			Header(headerContentType, mimeNarinfo).
// 			Body(expectInfo.String()).
// 			Status(http.StatusOK).
// 			End()
// 	})
// }
//
// func TestRouterNarPut(t *testing.T) {
// 	t.Run("upload success", func(tt *testing.T) {
// 		proxy := withS3(testProxy(tt))
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("PUT").
// 			URL(nsNar).
// 			Body(string(testdata[fNar])).
// 			Expect(tt).
// 			Header(headerContentType, mimeText).
// 			Body("ok\n").
// 			Status(http.StatusOK).
// 			End()
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerContentType, mimeNar).
// 			Header(headerCache, headerCacheHit).
// 			Body(string(testdata[fNar])).
// 			Status(http.StatusOK).
// 			End()
// 	})
//
// 	t.Run("upload xz success", func(tt *testing.T) {
// 		proxy := withS3(testProxy(tt))
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("PUT").
// 			URL(nsNarXz).
// 			Body(string(testdata[fNarXz])).
// 			Expect(tt).
// 			Header(headerContentType, mimeText).
// 			Body("ok\n").
// 			Status(http.StatusOK).
// 			End()
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsNar).
// 			Expect(tt).
// 			Header(headerContentType, mimeNar).
// 			Header(headerCache, headerCacheHit).
// 			Body(string(testdata[fNar])).
// 			Status(http.StatusOK).
// 			End()
// 	})
// }
//
// func TestRouterRealisationPut(t *testing.T) {
// 	t.Run("upload success", func(tt *testing.T) {
// 		proxy := withS3(testProxy(tt))
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("PUT").
// 			URL(nsRealisation).
// 			Body(string(testdata[fRealisation])).
// 			Expect(tt).
// 			Header(headerContentType, mimeText).
// 			Body("ok\n").
// 			Status(http.StatusOK).
// 			End()
//
// 		apitest.New().
// 			Handler(proxy.router()).
// 			Method("GET").
// 			URL(nsRealisation).
// 			Expect(tt).
// 			Header(headerContentType, mimeJson).
// 			Header(headerCache, headerCacheHit).
// 			Status(http.StatusOK).
// 			Assert(realisationMatches(tt, testdata[fRealisation])).
// 			End()
// 	})
// }
//
// func realisationMatches(t *testing.T, expectedBody []byte) func(*http.Response, *http.Request) error {
// 	return func(w *http.Response, r *http.Request) error {
// 		expected := realisation{}
// 		if err := json.Unmarshal(expectedBody, &expected); err != nil {
// 			return errors.WithMessage(err, "decoding expected")
// 		}
//
// 		actual := realisation{}
// 		dec := json.NewDecoder(w.Body)
// 		if err := dec.Decode(&actual); err != nil {
// 			return errors.WithMessage(err, "decoding actual")
// 		}
//
// 		assert.Equal(t, expected, actual)
//
// 		return nil
// 	}
// }

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

func (proxy *Proxy) inspectTable(name string) {
	dbErr := proxy.withDbReadOnly(func(db *sqlite.Conn) error {
		query := db.Prep(`SELECT * FROM ` + name)

		if err := query.Reset(); err != nil {
			return err
		}

		rowReturned, err := query.Step()
		if !rowReturned {
			pp("no rows in", name)
			return nil
		}

		if err != nil {
			return err
		}

		for col := 0; col < query.ColumnCount(); col += 1 {
			switch query.ColumnType(col) {
			case sqlite.SQLITE_INTEGER:
				pp(query.ColumnName(col), ":", query.ColumnInt(col))
			case sqlite.SQLITE_FLOAT:
				pp(query.ColumnName(col), ":", query.ColumnFloat(col))
			case sqlite.SQLITE_TEXT:
				pp(query.ColumnName(col), ":", query.ColumnText(col))
			case sqlite.SQLITE_BLOB:
				head := make([]byte, 1024)
				query.ColumnBytes(col, head)
				pp(query.ColumnName(col), ":", head)
			case sqlite.SQLITE_NULL:
				pp(query.ColumnName(col), ":", "NULL")
			}
		}

		return nil
	})

	if dbErr != nil {
		panic(dbErr)
	}
}
