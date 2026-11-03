package peercred

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	return key
}

// sealLegacy encrypts plaintext with AES-GCM under key with NO associated
// data — the pre-v2 federation format (GH#7890 findings 32/38). The plaintext
// was openable from any peer row; the migration planner must still be able to
// read blobs in this shape.
func sealLegacy(t *testing.T, plaintext string, key []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("new gcm: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatalf("generate nonce: %v", err)
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil)
}

var victimBinding = Binding{Name: "victim", URL: "https://victim.example/db", Username: "victimuser"}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("s3cret", key, victimBinding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !IsV2(blob) {
		t.Fatalf("sealed blob %q does not carry the v2 prefix", blob)
	}
	got, err := Open(blob, key, victimBinding)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("round trip = %q, want %q", got, "s3cret")
	}
}

// Changing any one binding field must fail authentication, so a copied blob
// under a different peer identity never releases the password.
func TestOpenRejectsChangedBindingField(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("s3cret", key, victimBinding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	for _, change := range []struct {
		name string
		b    Binding
	}{
		{"copied into a different peer row", Binding{Name: "attacker", URL: "https://attacker.example/db", Username: "victimuser"}},
		{"same name, diverged URL", Binding{Name: "victim", URL: "https://attacker.example/db", Username: "victimuser"}},
		{"same name and URL, different username", Binding{Name: "victim", URL: "https://victim.example/db", Username: "attackeruser"}},
		{"username cleared", Binding{Name: "victim", URL: "https://victim.example/db"}},
	} {
		t.Run(change.name, func(t *testing.T) {
			got, err := Open(blob, key, change.b)
			if err == nil {
				t.Fatalf("Open under changed binding returned %q, want error", got)
			}
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("Open error = %v, want ErrAuthentication", err)
			}
		})
	}
}

// A different local key must fail authentication too: the sentinel must not
// claim a proven identity mismatch where a wrong key is equally possible.
func TestOpenRejectsWrongKey(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("s3cret", key, victimBinding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := Open(blob, testKey(t), victimBinding); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("Open under wrong key error = %v, want ErrAuthentication", err)
	}
}

// Normal Open accepts v2 only: a legacy blob is classified, never decrypted by
// the authentication path, and no path may fall back to nil-AAD decryption.
func TestOpenRejectsLegacyBlob(t *testing.T) {
	key := testKey(t)
	legacy := sealLegacy(t, "s3cret", key)
	if _, err := Open(legacy, key, victimBinding); !errors.Is(err, ErrNotV2) {
		t.Fatalf("Open on legacy blob error = %v, want ErrNotV2", err)
	}
	// A legacy nonce can begin with the bytes "v2:"; prefix presence alone must
	// not route such a blob to the v2 framing either (it fails there too).
	legacy2 := sealLegacy(t, "s3cret", key)
	for len(legacy2) < 3 || string(legacy2[:3]) != "v2:" {
		legacy2 = sealLegacy(t, "s3cret", key)
	}
	if _, err := Open(legacy2, key, victimBinding); err == nil {
		t.Fatal("Open accepted a legacy blob whose nonce begins with the v2 prefix")
	}
}

// OpenLegacy is the migration-only reader: it round-trips the legacy framing
// and fails on v2 blobs (those are not its to open).
func TestOpenLegacy(t *testing.T) {
	key := testKey(t)
	legacy := sealLegacy(t, "s3cret", key)
	got, err := OpenLegacy(legacy, key)
	if err != nil {
		t.Fatalf("OpenLegacy: %v", err)
	}
	if got != "s3cret" {
		t.Fatalf("OpenLegacy = %q, want %q", got, "s3cret")
	}
	blob, err := Seal("s3cret", key, victimBinding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := OpenLegacy(blob, key); err == nil {
		t.Fatal("OpenLegacy accepted a v2 blob")
	}
}

func TestSealRejectsShortKey(t *testing.T) {
	if _, err := Seal("s3cret", []byte("short"), victimBinding); !errors.Is(err, ErrBadKey) {
		t.Fatalf("Seal with short key error = %v, want ErrBadKey", err)
	}
	if _, err := Open(sealLegacy(t, "x", testKey(t)), []byte("short"), victimBinding); err == nil {
		t.Fatal("Open with short key unexpectedly succeeded")
	}
}

// Each seal mints a fresh nonce, so equal passwords produce unequal blobs:
// rebinding never leaves a duplicate blob behind.
func TestSealIsRandomized(t *testing.T) {
	key := testKey(t)
	a, err := Seal("s3cret", key, victimBinding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	b, err := Seal("s3cret", key, victimBinding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("two seals of one password produced identical ciphertext")
	}
}

func TestMarkerValue(t *testing.T) {
	key := testKey(t)
	value := MarkerValue(key)
	if !MarkerMatches(value, key) {
		t.Fatal("MarkerValue does not match its own key")
	}
	if MarkerMatches(value, testKey(t)) {
		t.Fatal("marker matched an unrelated key: it is not completion evidence for that key")
	}
	if MarkerMatches(nil, key) {
		t.Fatal("empty marker content matched")
	}
	// Domain separation: a bare HMAC of the key (no label) must not pass.
	if MarkerMatches(hmacSHA256(key, nil), key) {
		t.Fatal("marker accepted an unlabelled HMAC of the key")
	}
	if MarkerMatches(hmacSHA256(key, []byte(MarkerFile)), key) {
		t.Fatal("marker accepted an HMAC under the wrong domain label")
	}
}
func hmacSHA256(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(msg)
	return mac.Sum(nil)
}
