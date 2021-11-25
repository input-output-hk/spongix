package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os/exec"
)

type nixConfig struct {
	secretKeys        map[string]ed25519.PrivateKey
	trustedPublicKeys map[string]ed25519.PublicKey
	substituters      []string
}

type rawNixConfig struct {
	SecretKeyFiles struct {
		Value []string `json:"value"`
	} `json:"secret-key-files"`

	TrustedPublicKeys struct {
		Value []string `json:"value"`
	} `json:"trusted-public-keys"`

	Substituters struct {
		Value []string `json:"value"`
	} `json:"substituters"`
}

func loadNixConfig() (*nixConfig, error) {
	cmd := exec.Command("nix", "show-config", "--json")
	buf := bytes.Buffer{}
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, err
	}

	rawConfig := rawNixConfig{}

	if err := json.Unmarshal(buf.Bytes(), &rawConfig); err != nil {
		return nil, err
	}

	config := &nixConfig{
		secretKeys:        map[string]ed25519.PrivateKey{},
		trustedPublicKeys: map[string]ed25519.PublicKey{},
		substituters:      rawConfig.Substituters.Value,
	}

	for _, value := range rawConfig.TrustedPublicKeys.Value {
		name, key, err := parseNixPair(value)
		if err != nil {
			return nil, err
		}
		config.trustedPublicKeys[name] = ed25519.PublicKey(key)
	}

	for _, value := range rawConfig.SecretKeyFiles.Value {
		key, err := LoadNixPrivateKey(value)
		if err != nil {
			return nil, err
		}
		config.secretKeys[key.name] = key.key
	}

	return config, nil
}
