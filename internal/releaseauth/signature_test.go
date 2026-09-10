package releaseauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestSignatureBindsArchiveAndName(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := publicKeyText
	publicKeyText = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { publicKeyText = old })
	digest := sha256.Sum256([]byte("archive"))
	name := "hkc-v1.0.0-linux-amd64.tar.gz"
	message, err := Message(name, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(key, message)
	if err := Verify(name, digest[:], sig); err != nil {
		t.Fatal(err)
	}
	if err := Verify("hkc-v2.0.0-linux-amd64.tar.gz", digest[:], sig); err == nil {
		t.Fatal("renamed release accepted")
	}
	digest[0] ^= 1
	if err := Verify(name, digest[:], sig); err == nil {
		t.Fatal("modified archive accepted")
	}
	digest[0] ^= 1
	if err := Verify(name, digest[:], sig[:20]); err == nil {
		t.Fatal("truncated signature accepted")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	if err := Verify(name, digest[:], ed25519.Sign(other, message)); err == nil {
		t.Fatal("untrusted key accepted")
	}
}
