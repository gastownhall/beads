package peercred

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func testBinding() Binding {
	return Binding{
		Name:      "team",
		RemoteURL: "https://github.com/acme/beads.git",
		Username:  "sync-bot",
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey()
	binding := testBinding()
	blob, err := Seal(key, "secret", binding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !IsV2(blob) {
		t.Fatalf("Seal() blob = %q, want v2 prefix", blob[:min(len(blob), 3)])
	}
	got, err := Open(key, blob, binding)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "secret" {
		t.Fatalf("Open() = %q, want %q", got, "secret")
	}
}

func TestOpenRejectsChangedBinding(t *testing.T) {
	key := testKey()
	binding := testBinding()
	blob, err := Seal(key, "secret", binding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Binding)
	}{
		{name: "name", mutate: func(b *Binding) { b.Name = "evil" }},
		{name: "remote URL", mutate: func(b *Binding) { b.RemoteURL = "https://evil.example/beads" }},
		{name: "username", mutate: func(b *Binding) { b.Username = "other-user" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := binding
			test.mutate(&changed)
			if _, err := Open(key, blob, changed); err == nil {
				t.Fatal("Open() succeeded with changed binding")
			}
		})
	}
}

func TestOpenAcceptsCanonicalEquivalentRemoteURL(t *testing.T) {
	key := testKey()
	binding := Binding{
		Name:      "team",
		RemoteURL: "https://github.com/acme/beads.git",
		Username:  "sync-bot",
	}
	blob, err := Seal(key, "secret", binding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	binding.RemoteURL = "git+https://github.com/acme/beads"
	got, err := Open(key, blob, binding)
	if err != nil {
		t.Fatalf("Open with canonical-equivalent URL: %v", err)
	}
	if got != "secret" {
		t.Fatalf("Open() = %q, want %q", got, "secret")
	}
	binding.RemoteURL = "https://GITHUB.COM/acme/beads.git"
	got, err = Open(key, blob, binding)
	if err != nil {
		t.Fatalf("Open with host-case-equivalent URL: %v", err)
	}
	if got != "secret" {
		t.Fatalf("Open() with host-case-equivalent URL = %q, want %q", got, "secret")
	}
}

func TestOpenLengthPrefixPreventsBindingAmbiguity(t *testing.T) {
	key := testKey()
	sealedFor := Binding{Name: "ab", RemoteURL: "c", Username: "d"}
	ambiguousWithoutLengths := Binding{Name: "a", RemoteURL: "bc", Username: "d"}
	blob, err := Seal(key, "secret", sealedFor)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(key, blob, ambiguousWithoutLengths); err == nil {
		t.Fatal("Open() accepted a binding with ambiguous concatenated fields")
	}
}

func TestOpenReturnsLegacyCredentialSentinel(t *testing.T) {
	key := testKey()
	legacy, err := sealLegacyForTest(key, "secret")
	if err != nil {
		t.Fatalf("seal legacy: %v", err)
	}
	if _, err := Open(key, legacy, testBinding()); !errors.Is(err, ErrLegacyCredential) {
		t.Fatalf("Open(legacy) error = %v, want ErrLegacyCredential", err)
	}
}

func TestOpenLegacyReadsLegacyBlob(t *testing.T) {
	key := testKey()
	legacy, err := sealLegacyForTest(key, "legacy secret")
	if err != nil {
		t.Fatalf("seal legacy: %v", err)
	}
	got, err := OpenLegacy(key, legacy)
	if err != nil {
		t.Fatalf("OpenLegacy: %v", err)
	}
	if got != "legacy secret" {
		t.Fatalf("OpenLegacy() = %q, want %q", got, "legacy secret")
	}
}

func TestOpenLegacyShortCiphertext(t *testing.T) {
	if _, err := OpenLegacy(testKey(), []byte("short")); err == nil || err.Error() != "ciphertext too short" {
		t.Fatalf("OpenLegacy(short) error = %v, want ciphertext too short", err)
	}
}

func TestOpenV2ShortCiphertext(t *testing.T) {
	_, err := Open(testKey(), []byte(v2Prefix), testBinding())
	if err == nil || errors.Is(err, ErrLegacyCredential) {
		t.Fatalf("Open(short v2) error = %v, want a plain short-ciphertext error", err)
	}
	if err.Error() != "ciphertext too short" {
		t.Fatalf("Open(short v2) error = %v, want ciphertext too short", err)
	}
}

func TestDuplicateBlobs(t *testing.T) {
	shared := []byte{1, 2, 3}
	unique := []byte{4, 5, 6}
	duplicates := DuplicateBlobs([][]byte{shared, unique, shared, []byte{7}})

	if len(duplicates) != 1 || !duplicates[string(shared)] {
		t.Fatalf("DuplicateBlobs() = %#v, want only shared blob", duplicates)
	}
	if duplicates[string(unique)] {
		t.Fatalf("DuplicateBlobs() marked unique blob as duplicate")
	}
}

func TestMigrationMarker(t *testing.T) {
	dir := t.TempDir()
	if MigrationDone(dir) {
		t.Fatal("MigrationDone() = true before marker exists")
	}
	if err := MarkMigrated(dir); err != nil {
		t.Fatalf("MarkMigrated: %v", err)
	}
	if !MigrationDone(dir) {
		t.Fatal("MigrationDone() = false after MarkMigrated")
	}
	info, err := os.Stat(filepath.Join(dir, MigrationMarkerFile))
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("marker permissions = %04o, want 0600", got)
	}
}

func sealLegacyForTest(key []byte, password string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(password), nil), nil
}
