package main

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"os"
	"testing"

	"github.com/folbricht/desync"
	"github.com/smartystreets/assertions"
	"github.com/steinfletcher/apitest"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	fixtureNarinfo = "8ckxc8biqqfdwyhr0w70jgrcb4h7a4y5.narinfo"
	fixtureNar     = "0m8sd5qbmvfhyamwfv3af1ff18ykywf3zx5qwawhhp3jv1h777xz.nar"
)

type fakeStore struct {
	chunks map[desync.ChunkID]*desync.Chunk
}

func newFakeStore() *fakeStore                                        { return &fakeStore{chunks: map[desync.ChunkID]*desync.Chunk{}} }
func (s fakeStore) Close() error                                      { return nil }
func (s fakeStore) GetChunk(id desync.ChunkID) (*desync.Chunk, error) { return s.chunks[id], nil }
func (s fakeStore) HasChunk(id desync.ChunkID) (bool, error)          { _, ok := s.chunks[id]; return ok, nil }
func (s *fakeStore) StoreChunk(chunk *desync.Chunk) error             { s.chunks[chunk.ID()] = chunk; return nil }
func (s fakeStore) String() string                                    { return "" }

var testKeys = map[string]ed25519.PrivateKey{
	"test": ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0}, 32)),
}

func testProxy() *Proxy {
	proxy := defaultProxy()

	encoderCfg := zapcore.EncoderConfig{
		MessageKey:     "msg",
		LevelKey:       "level",
		NameKey:        "logger",
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	}
	core := zapcore.NewCore(zapcore.NewJSONEncoder(encoderCfg), os.Stderr, zap.WarnLevel)
	proxy.log = zap.New(core)

	proxy.DatabaseDSN = "file::memory:"
	proxy.Clean()
	proxy.SetupDesync()
	proxy.SetupNix()
	proxy.SetupDB()

	setupAWS(proxy)

	proxy.secretKeys = testKeys

	name, key, err := parseNixPair("cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=")
	fatal(err)
	proxy.trustedKeys[name] = ed25519.PublicKey(key)

	return proxy
}

func setupAWS(proxy *Proxy) {
	resetTransport := apitest.NewStandaloneMocks().End()

	proxy.BucketURL = "s3+https://s3-eu-central-1.amazonaws.com/ncp"
	proxy.BucketURL = "s3+http://127.0.0.1:1234/ncp"
	proxy.BucketRegion = "eu-central-1"

	os.Setenv("MINIO_ACCESS_KEY", "test")
	os.Setenv("MINIO_SECRET_KEY", "test")

	proxy.SetupS3()

	resetTransport()
}

func Test_RoutingNixCacheInfo(t *testing.T) {
	proxy := testProxy()

	apitest.New().
		Handler(proxy.routerV2()).
		Get("/nix-cache-info").
		Expect(t).
		Header("Content-Type", "text/x-nix-cache-info").
		Body(`StoreDir: /nix/store
WantMassQuery: 1
Priority: 50`).
		Status(http.StatusOK).
		End()
}

func Test_NarHeadNotFound(t *testing.T) {
	proxy := testProxy()
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Method("HEAD").
		URL("/nar/" + fixtureNar).
		Expect(t).
		Body("").
		Status(http.StatusNotFound).
		End()
}

func Test_NarGetNotFound(t *testing.T) {
	proxy := testProxy()
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Get("/nar/" + fixtureNar).
		Expect(t).
		Body("not found").
		Status(http.StatusNotFound).
		End()
}

func Test_NarPut(t *testing.T) {
	store := newFakeStore()
	proxy := testProxy()
	proxy.s3Store = store
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Put("/nar/" + fixtureNar).
		BodyFromFile("fixtures/" + fixtureNar).
		Expect(t).
		Status(http.StatusOK).
		End()

	a := assertions.New(t)
	a.So(store.chunks, assertions.ShouldHaveLength, 1)

	apitest.New().
		Handler(router).
		Get("/nar/" + fixtureNar).
		Expect(t).
		BodyFromFile("fixtures/" + fixtureNar).
		Status(http.StatusOK).
		End()

	apitest.New().
		Handler(router).
		Put("/nar/" + fixtureNar).
		Body("").
		Expect(t).
		Body("").
		Status(http.StatusLengthRequired).
		End()

	apitest.New().
		Handler(router).
		Put("/nar/" + fixtureNar).
		Body("").
		Expect(t).
		Body("").
		Status(http.StatusLengthRequired).
		End()
}

func Test_NarinfoHeadNotFound(t *testing.T) {
	proxy := testProxy()
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Method("HEAD").
		URL("/" + fixtureNarinfo).
		Expect(t).
		Body("").
		Status(http.StatusNotFound).
		End()
}

func Test_NarinfoHead(t *testing.T) {
	proxy := testProxy()
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Put("/" + fixtureNarinfo).
		BodyFromFile("fixtures/" + fixtureNarinfo).
		Expect(t).
		Body("").
		Status(http.StatusOK).
		End()

	apitest.New().
		Handler(router).
		Method("HEAD").
		URL("/" + fixtureNarinfo).
		Expect(t).
		Body("").
		Status(http.StatusNotFound).
		End()
}

func Test_NarinfoGetNotFound(t *testing.T) {
	proxy := testProxy()
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Get("/" + fixtureNarinfo).
		Expect(t).
		Body("not found in db").
		Status(http.StatusNotFound).
		End()
}

func Test_NarinfoPut(t *testing.T) {
	proxy := testProxy()
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Put("/" + fixtureNarinfo).
		BodyFromFile("fixtures/" + fixtureNarinfo).
		Expect(t).
		Body("").
		Status(http.StatusOK).
		End()
}

// The narinfo has trusted signature from cache.nixos.org-1. So we add our own
// signature to it.
func Test_NarinfoGet(t *testing.T) {
	a := assertions.New(t)

	proxy := testProxy()
	router := proxy.routerV2()

	apitest.New().
		Handler(router).
		Put("/" + fixtureNarinfo).
		BodyFromFile("fixtures/" + fixtureNarinfo).
		Expect(t).
		Body("").
		Status(http.StatusOK).
		End()

	body, err := os.Open("fixtures/" + fixtureNarinfo)
	a.So(err, assertions.ShouldBeNil)

	info := &Narinfo{}
	a.So(info.Unmarshal(body), assertions.ShouldBeNil)

	a.So(info.Sign("test", testKeys["test"]), assertions.ShouldBeNil)

	buf := []byte{}
	output := bytes.NewBuffer(buf)
	a.So(info.Marshal(output), assertions.ShouldBeNil)

	apitest.New().
		Handler(router).
		Get("/" + fixtureNarinfo).
		Expect(t).
		Body(output.String()).
		Status(http.StatusOK).
		End()
}
