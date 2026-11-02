package peercred

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"testing"
)

func testKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// AC-1: a sealed password opens only under the key and the exact raw
// (name, remote_url, username) it was sealed for.
func TestSealOpenBindsKeyAndEveryIdentityField(t *testing.T) {
	key := testKey(1)
	id := Identity{Name: "prod", RemoteURL: "https://prod.example/db", Username: "produser"}
	blob, err := Seal(key, id, "s3cret")
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if !bytes.HasPrefix(blob, []byte(v2Prefix)) {
		t.Fatalf("Seal() = %q, want the %q prefix", blob, v2Prefix)
	}
	got, err := Open(key, id, blob)
	if err != nil || got != "s3cret" {
		t.Fatalf("Open(original) = %q, %v; want s3cret", got, err)
	}

	changed := map[string]Identity{
		"name":            {Name: "evil", RemoteURL: id.RemoteURL, Username: id.Username},
		"remote_url":      {Name: id.Name, RemoteURL: "https://attacker.example/db", Username: id.Username},
		"username":        {Name: id.Name, RemoteURL: id.RemoteURL, Username: "other"},
		"empty username":  {Name: id.Name, RemoteURL: id.RemoteURL},
		"url trailing /":  {Name: id.Name, RemoteURL: id.RemoteURL + "/", Username: id.Username},
		"url case":        {Name: id.Name, RemoteURL: "https://PROD.example/db", Username: id.Username},
		"field boundary":  {Name: "prodh", RemoteURL: "ttps://prod.example/db", Username: id.Username},
		"username to url": {Name: id.Name, RemoteURL: id.RemoteURL + "produser", Username: ""},
	}
	for name, other := range changed {
		t.Run(name, func(t *testing.T) {
			if got, err := Open(key, other, blob); err == nil {
				t.Fatalf("Open(%+v) = %q, want an authentication failure", other, got)
			}
		})
	}
	t.Run("wrong key", func(t *testing.T) {
		if got, err := Open(testKey(2), id, blob); err == nil {
			t.Fatalf("Open(wrong key) = %q, want an authentication failure", got)
		}
	})
}

func TestSealOpenEmptyPassword(t *testing.T) {
	id := Identity{Name: "prod", RemoteURL: "https://prod.example/db"}
	blob, err := Seal(testKey(1), id, "")
	if err != nil || blob != nil {
		t.Fatalf("Seal(empty) = %q, %v; want nil, nil", blob, err)
	}
	got, err := Open(testKey(1), id, nil)
	if err != nil || got != "" {
		t.Fatalf("Open(nil) = %q, %v; want empty", got, err)
	}
}

// AC-3: an unprefixed blob is legacy and is never decrypted, even when it is
// a valid unbound AES-GCM blob under the current key.
func TestOpenRejectsLegacyBlob(t *testing.T) {
	key := testKey(1)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	legacy := gcm.Seal(nonce, nonce, []byte("s3cret"), nil)

	got, err := Open(key, Identity{Name: "prod"}, legacy)
	if !errors.Is(err, ErrLegacyCredential) {
		t.Fatalf("Open(legacy) = %q, %v; want ErrLegacyCredential", got, err)
	}
}

func TestOpenShortV2Blob(t *testing.T) {
	_, err := Open(testKey(1), Identity{Name: "prod"}, []byte(v2Prefix+"short"))
	if err == nil || errors.Is(err, ErrLegacyCredential) {
		t.Fatalf("Open(short v2) error = %v, want a malformed-blob error", err)
	}
}
