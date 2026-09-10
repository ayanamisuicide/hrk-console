// Package releaseauth authenticates release archives using a pinned Ed25519 key.
package releaseauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"
)

//go:embed public.key
var publicKeyText string

func PublicKey() (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyText))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid release public key")
	}
	return ed25519.PublicKey(b), nil
}

// Message binds the archive digest to its exact release, platform and product name.
func Message(name string, digest []byte) ([]byte, error) {
	if name == "" || strings.ContainsAny(name, "/\\\r\n") || len(digest) != sha256.Size {
		return nil, fmt.Errorf("invalid release signature input")
	}
	return []byte(fmt.Sprintf("hrk-console-release-v1\n%s\n%x\n", name, digest)), nil
}

func Verify(name string, digest, signature []byte) error {
	key, err := PublicKey()
	if err != nil {
		return err
	}
	message, err := Message(name, digest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key, message, signature) {
		return fmt.Errorf("подпись обновления недействительна; установка отменена")
	}
	return nil
}
