package main

import (
	"reflect"
	"testing"
)

func TestParseNarinfoReferences(t *testing.T) {
	body := []byte(`StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar
Compression: none
FileHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
FileSize: 512
NarHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
NarSize: 512
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test abc123def456ghij789klmnopqr012345-other
Deriver: 914ivbx6hfpgczwphndm0vc4z6q2c8a1-test.drv
Sig: kappa:JccDYkaQjN7ywE9VGJ6/RAzCt7XJoqWsmjTRsdAdM8DF40ebDDu3XWaasuJkaezbhVxjaRLJm3VWDEk6EmRpCw==
`)
	got := parseNarinfoReferences(body)
	want := []string{"5b4cprjhjw35wyzvgmgvqay4hjf59h7x", "abc123def456ghij789klmnopqr012345"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNarinfoReferences = %v, want %v", got, want)
	}
}

func TestParseNarinfoReferencesEmpty(t *testing.T) {
	body := []byte(`StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar
Compression: none
References:
NarHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
NarSize: 512
`)
	got := parseNarinfoReferences(body)
	if got != nil {
		t.Fatalf("expected nil for empty References, got %v", got)
	}
}

func TestParseNarinfoReferencesNoField(t *testing.T) {
	body := []byte(`StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
URL: nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar
Compression: none
NarHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp
NarSize: 512
`)
	got := parseNarinfoReferences(body)
	if got != nil {
		t.Fatalf("expected nil when no References field, got %v", got)
	}
}

func TestParseNarinfoReferencesSingle(t *testing.T) {
	body := []byte(`StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
References: 5b4cprjhjw35wyzvgmgvqay4hjf59h7x-test
NarSize: 512
`)
	got := parseNarinfoReferences(body)
	want := []string{"5b4cprjhjw35wyzvgmgvqay4hjf59h7x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNarinfoReferences = %v, want %v", got, want)
	}
}

func TestParseNarinfoNarHash52(t *testing.T) {
	body := []byte("StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\nURL: nar/1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp.nar\nCompression: none\nNarHash: sha256:1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp\nNarSize: 512\n")
	got := parseNarinfoNarHash52(body)
	want := "1h6m2q7f8zq5z4kvn8j5wiz05jdic77df1x68dfwqg149jsy7gyp"
	if got != want {
		t.Fatalf("parseNarinfoNarHash52 = %q, want %q", got, want)
	}
}

func TestParseNarinfoNarHash52Missing(t *testing.T) {
	body := []byte("StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test\nURL: nar/hash.nar\nCompression: none\nNarSize: 512\n")
	got := parseNarinfoNarHash52(body)
	if got != "" {
		t.Fatalf("expected empty for missing NarHash, got %q", got)
	}
}

func TestParseNarinfoReferencesNoDash(t *testing.T) {
	body := []byte(`StorePath: /nix/store/hyhrnrnpsz9fw5p9dk85a58y31ink18c-test
References: nodashhere validhash-withname anothernodash
NarSize: 512
`)
	got := parseNarinfoReferences(body)
	// "nodashhere" and "anothernodash" have no dash, so they are skipped.
	// Only "validhash" (from "validhash-withname") should be extracted.
	want := []string{"validhash"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNarinfoReferences = %v, want %v", got, want)
	}
}
