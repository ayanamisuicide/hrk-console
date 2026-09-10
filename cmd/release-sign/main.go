// release-sign writes a detached Ed25519 signature for each archive argument.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"heroku-console/internal/releaseauth"
)

func signFiles(files []string) error {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("HKC_RELEASE_SIGNING_SEED")))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("HKC_RELEASE_SIGNING_SEED must contain a base64 Ed25519 seed")
	}
	key := ed25519.NewKeyFromSeed(seed)
	trusted, err := releaseauth.PublicKey()
	if err != nil || !key.Public().(ed25519.PublicKey).Equal(trusted) {
		return fmt.Errorf("signing key does not match the pinned public key")
	}
	if len(files) == 0 {
		return fmt.Errorf("usage: release-sign archive.tar.gz [...]")
	}
	for _, name := range files {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return err
		}
		message, err := releaseauth.Message(filepath.Base(name), h.Sum(nil))
		if err != nil {
			return err
		}
		if err := os.WriteFile(name+".sig", ed25519.Sign(key, message), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if err := signFiles(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
