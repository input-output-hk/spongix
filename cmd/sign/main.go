package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
)

// Given a narinfo file path and a secret key, add a signature and store the modified narinfo in a new file.
func main() {
	println("signing")

	secretKeyPath := os.Args[1]

	secretKeyRaw, err := os.ReadFile(secretKeyPath)
	if err != nil {
		panic(err)
	}

	secretKey, err := signature.LoadSecretKey(string(secretKeyRaw))
	if err != nil {
		panic(err)
	}
	publicKeys := []signature.PublicKey{secretKey.ToPublicKey()}

	err = filepath.WalkDir("/big/tmp", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		} else if filepath.Ext(path) != ".narinfo" {
			return nil
		} else if err := signNarinfo(path, secretKey, publicKeys); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		panic(err)
	}
}

func signNarinfo(path string, secretKey signature.SecretKey, publicKeys []signature.PublicKey) error {
	if narinfoReader, err := os.Open(path); err != nil {
		return err
	} else if info, err := narinfo.Parse(narinfoReader); err != nil {
		return err
	} else if err, modified := sign(info, secretKey, publicKeys); err != nil {
		return err
	} else if modified {
		if narinfoWriter, err := os.Create(filepath.Join("modified", filepath.Base(path))); err != nil {
			return err
		} else if _, err := narinfoWriter.WriteString(info.String()); err != nil {
			return err
		}
	}

	return nil
}

func sign(info *narinfo.NarInfo, secretKey signature.SecretKey, publicKeys []signature.PublicKey) (error, bool) {
	if signature.VerifyFirst(info.Fingerprint(), info.Signatures, publicKeys) {
		return nil, false
	}

	signature, err := secretKey.Sign(nil, info.Fingerprint())
	if err != nil {
		return err, false
	}

	fmt.Println(info.StorePath)

	info.Signatures = append(info.Signatures, signature)

	return nil, true
}
