package main

import (
	"bytes"
	"crypto/ed25519"
	"log"
	"net/http"
	"os"
	"testing"

	"github.com/steinfletcher/apitest"
)

const fakeCredentials = `
[nix-cache-proxy]
aws_access_key_id = AAAAAAAAAAAAAAAAAAAA
aws_secret_access_key = 0000000000000000000000000000000000000000
`

func testProxy() *Proxy {
	proxy := defaultProxy()
	proxy.BucketName = "test-bucket"
	proxy.BucketRegion = "eu-central-1"
	proxy.AWSProfile = "nix-cache-proxy"
	proxy.awsCredentialsFile = ".fake-credentials"

	if err := os.WriteFile(".fake-credentials", []byte(fakeCredentials), 0777); err != nil {
		log.Panic(err)
	}

	proxy.Clean()
	proxy.SetupDir("nar")

	setupAWS(proxy)

	proxy.secretKeys = map[string]ed25519.PrivateKey{
		"test": ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0}, 32)),
	}

	return proxy
}

func Test_RoutingNixCacheInfo(t *testing.T) {
	proxy := testProxy()

	apitest.New().
		Handler(proxy.router()).
		Get("/bucket/nix-cache-info").
		Expect(t).
		Header("Content-Type", "text/x-nix-cache-info").
		Body(`StoreDir: /nix/store
WantMassQuery: 1
Priority: 50`).
		Status(http.StatusOK).
		End()
}

func Test_UploadNarinfo(t *testing.T) {
	proxy := testProxy()
	router := proxy.router()

	const key = "9bqwjlnbz28kn9is62nbvbp9mfx8skhy.narinfo"

	apitest.New().
		Handler(router).
		Get("/bucket/" + key).
		Expect(t).
		Body(`<?xml version="1.0" encoding="UTF-8"?>
<Error>
  <Code>NoSuchKey</Code>
  <Message>The specified key does not exist.</Message>
  <Resource>/bucket/` + key + `</Resource>
  <BucketName>bucket</BucketName>
  <Key>` + key + `</Key>
  <RequestId>16B81914FBB8345F</RequestId>
  <HostId>672a09d6-39bb-41a6-bcf3-b0375d351cfe</HostId>
</Error>`).
		Status(http.StatusNotFound).
		End()

	mock := apitest.NewMock().
		Put("https://test-bucket.s3.eu-central-1.amazonaws.com/" + key).
		RespondWith().
		Status(http.StatusOK).
		End()

	apitest.New().
		Mocks(mock).
		Handler(router).
		Put("/bucket/" + key).
		BodyFromFile("fixtures/" + key).
		Expect(t).
		Status(http.StatusOK).
		End()

	for _, url := range []string{"/bucket/" + key, "/" + key} {
		apitest.New().
			Handler(router).
			Get(url).
			Expect(t).
			BodyFromFile("fixtures/" + key + ".signed").
			Status(http.StatusOK).
			End()
	}
}

func Test_UploadNar(t *testing.T) {
	proxy := testProxy()
	router := proxy.router()

	const key = "0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7.nar.xz"

	apitest.New().
		Handler(router).
		Method("HEAD").
		URL("/bucket/nar/"+key).
		Expect(t).
		Header("Content-Type", "text/html").
		Status(http.StatusNotFound).
		End()

	mock := apitest.NewMock().
		Put("https://test-bucket.s3.eu-central-1.amazonaws.com/nar/" + key).
		RespondWith().
		Status(http.StatusOK).
		End()

	apitest.New().
		Mocks(mock).
		Handler(router).
		Put("/bucket/nar/" + key).
		BodyFromFile("fixtures/" + key).
		Expect(t).
		Status(http.StatusOK).
		End()

	apitest.New().
		Handler(router).
		Get("/nar/"+key).
		Expect(t).
		Header("Content-Type", "application/x-nix-nar").
		BodyFromFile("fixtures/" + key).
		Status(http.StatusOK).
		End()
}

func setupAWS(proxy *Proxy) {
	resetTransport := apitest.NewStandaloneMocks(
		apitest.NewMock().
			Head("https://s3.eu-central-1.amazonaws.com/").
			RespondWith().
			Body(`{"a": 12345}`).
			Status(http.StatusOK).
			End(),
	).End()
	proxy.SetupAWS()
	resetTransport()
}
