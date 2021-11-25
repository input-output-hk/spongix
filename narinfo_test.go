package main

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/steinfletcher/apitest"
)

var validNarinfo = &NarInfo{
	StorePath:   "/nix/store/00000000000000000000000000000000-some",
	URL:         "nar/0000000000000000000000000000000000000000000000000000.nar.xz",
	Compression: "xz",
	FileHash:    "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
	FileSize:    1,
	NarHash:     "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
	NarSize:     1,
	References:  []string{"00000000000000000000000000000000-some"},
	Deriver:     "r92m816zcm8v9zjr55lmgy4pdibjbyjp-foo.drv",
}

func Test_NarinfoMarshal(t *testing.T) {
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
References: `+strings.Join(validNarinfo.References, " ")+`
Deriver: `+validNarinfo.Deriver+`
`)
}

func Test_NarinfoValidate(t *testing.T) {
	v := apitest.DefaultVerifier{}

	info := &NarInfo{
		Compression: "invalid",
		References:  []string{"invalid"},
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

	info.References = []string{"00000000000000000000000000000000-some"}
	v.Equal(t, `Invalid Deriver: ""`, info.Validate().Error())

	info.Deriver = "r92m816zcm8v9zjr55lmgy4pdibjbyjp-foo.drv"
	v.Equal(t, nil, info.Validate())
}

func Test_NarinfoVerify(t *testing.T) {
	v := apitest.DefaultVerifier{}

	key := &NixPrivateKey{
		name: "test",
		key:  ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0}, 32)),
	}
	publicKeys := map[string]ed25519.PublicKey{}
	publicKeys[key.name] = key.key.Public().(ed25519.PublicKey)

	info := &NarInfo{
		StorePath:   "/nix/store/00000000000000000000000000000000-some",
		URL:         "nar/0000000000000000000000000000000000000000000000000000.nar.xz",
		Compression: "xz",
		FileHash:    "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
		FileSize:    1,
		NarHash:     "sha256:0f54iihf02azn24vm6gky7xxpadq5693qrjzkaavbnd68shvgbd7",
		NarSize:     1,
		References:  []string{"00000000000000000000000000000000-some"},
		Deriver:     "r92m816zcm8v9zjr55lmgy4pdibjbyjp-foo.drv",
	}

	v.Equal(t, nil, info.Validate())

	info.Sig = []string{}
	v.Equal(t, `No matching signature found`, info.Verify(publicKeys).Error())

	info.Sig = []string{"test:test"}
	v.Equal(t, `Signed by "test" but signature doesn't match narinfo`, info.Verify(publicKeys).Error())

	info.Sig = []string{}
	v.NoError(t, info.Sign(key))
	v.Equal(t, nil, info.Verify(publicKeys))
}
