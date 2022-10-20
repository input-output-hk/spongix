package main

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/smartystreets/assertions"
	"github.com/steinfletcher/apitest"
)

var validNarinfo = &Narinfo{
	StorePath:   "/nix/store/00000000000000000000000000000000-some",
	URL:         "nar/0000000000000000000000000000000000000000000000000000.nar.xz",
	Compression: "xz",
	FileHash:    "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
	FileSize:    1,
	NarHash:     "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
	NarSize:     1,
	References:  []Reference{"00000000000000000000000000000000-some"},
	Deriver:     "r92m816zcm8v9zjr55lmgy4pdibjbyjp-foo.drv",
}

func TestNarinfoMarshal(t *testing.T) {
	v := apitest.DefaultVerifier{}

	info := validNarinfo
	buf := bytes.Buffer{}
	err := info.Marshal(&buf)
	v.NoError(t, err)

	v.Equal(t, buf.String(), `StorePath: `+validNarinfo.StorePath+`
URL: `+validNarinfo.URL+`
Compression: `+validNarinfo.Compression+`
FileHash: `+validNarinfo.FileHash+`
FileSize: 1
NarHash: `+validNarinfo.NarHash+`
NarSize: 1
References: `+validNarinfo.References.String()+`
Deriver: `+validNarinfo.Deriver+`
`)
}

func TestNarinfoValidate(t *testing.T) {
	v := apitest.DefaultVerifier{}

	info := &Narinfo{
		Namespace:   "test",
		Compression: "invalid",
		References:  References{"invalid"},
	}

	v.Equal(t, `Invalid StorePath: ""`, info.Validate().Error())

	info.StorePath = "/nix/store/00000000000000000000000000000000-some"
	v.Equal(t, `Invalid URL: ""`, info.Validate().Error())

	info.URL = "nar/0000000000000000000000000000000000000000000000000000.nar.xz"
	v.Equal(t, `Invalid Compression: "invalid"`, info.Validate().Error())

	info.Compression = "xz"
	v.Equal(t, `Invalid FileHash: ""`, info.Validate().Error())

	info.FileHash = "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7"
	v.Equal(t, `Invalid FileSize: 0`, info.Validate().Error())

	info.FileSize = 1
	v.Equal(t, `Invalid NarHash: ""`, info.Validate().Error())

	info.NarHash = "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7"
	v.Equal(t, `Invalid NarSize: 0`, info.Validate().Error())

	info.NarSize = 1
	v.Equal(t, `Invalid Reference: "invalid"`, info.Validate().Error())

	info.References = References{"00000000000000000000000000000000-some"}
	v.Equal(t, nil, info.Validate())
}

func TestNarinfoVerify(t *testing.T) {
	a := assertions.New(t)
	name := "test"
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0}, 32))

	publicKeys := map[string]ed25519.PublicKey{}
	publicKeys[name] = key.Public().(ed25519.PublicKey)

	info := &Narinfo{
		StorePath:   "/nix/store/00000000000000000000000000000000-some",
		URL:         "nar/0000000000000000000000000000000000000000000000000000.nar.xz",
		Compression: "xz",
		FileHash:    "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
		FileSize:    1,
		NarHash:     "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
		NarSize:     1,
		References:  References{"00000000000000000000000000000000-some"},
		Deriver:     "r92m816zcm8v9zjr55lmgy4pdibjbyjp-foo.drv",
	}

	info.Sig = Signatures{}
	valid, invalid := info.ValidInvalidSignatures(publicKeys)
	a.So(valid, assertions.ShouldHaveLength, 0)
	a.So(invalid, assertions.ShouldHaveLength, 0)

	info.Sig = Signatures{"test:test"}
	valid, invalid = info.ValidInvalidSignatures(publicKeys)
	a.So(valid, assertions.ShouldHaveLength, 0)
	a.So(invalid, assertions.ShouldHaveLength, 1)

	info.Sig = Signatures{}
	info.Sign(name, key)
	valid, invalid = info.ValidInvalidSignatures(publicKeys)
	a.So(valid, assertions.ShouldHaveLength, 1)
	a.So(invalid, assertions.ShouldHaveLength, 0)

	// v.Equal(t, `No matching signature found in []`, info.(publicKeys).Error())

	// info.Sig = []string{}
	// v.NoError(t, info.Sign(name, key))
	// v.Equal(t, nil, info.Verify(publicKeys))
}

func TestNarinfoSanitizeNar(t *testing.T) {
	a := assertions.New(t)
	name := "test"
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0}, 32))

	publicKeys := map[string]ed25519.PublicKey{}
	publicKeys[name] = key.Public().(ed25519.PublicKey)

	info := &Narinfo{
		StorePath:   "/nix/store/00000000000000000000000000000000-some",
		URL:         "nar/0000000000000000000000000000000000000000000000000000.nar.xz",
		Compression: "xz",
		FileHash:    "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		FileSize:    1,
		NarHash:     "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		NarSize:     2,
		References:  References{"00000000000000000000000000000000-some"},
		Deriver:     "r92m816zcm8v9zjr55lmgy4pdibjbyjp-foo.drv",
	}

	info.SanitizeNar()

	a.So(info.FileSize, assertions.ShouldEqual, 2)
	a.So(info.FileHash, assertions.ShouldEqual, "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	a.So(info.Compression, assertions.ShouldEqual, "none")
	a.So(info.URL, assertions.ShouldEqual, "nar/0000000000000000000000000000000000000000000000000000.nar")
}
