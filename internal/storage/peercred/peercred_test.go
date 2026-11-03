package peercred

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"strings"
	"testing"
)

func testKey(seed byte) []byte {
	key := make([]byte, KeySize)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

// legacySeal is the pre-v2 format: nonce || AES-GCM with no associated data.
func legacySeal(t *testing.T, key []byte, nonce []byte, password string) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(append([]byte(nil), nonce...), nonce, []byte(password), nil)
}

var team = Binding{Name: "team", URL: "https://peer.example/db", Username: "peeruser"}

func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(1)
	blob, err := Seal(key, team, "peerpass")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !bytes.HasPrefix(blob, []byte("v2:")) {
		t.Errorf("blob does not start with the v2 prefix: %q", blob[:3])
	}
	got, err := Open(key, team, blob)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got != "peerpass" {
		t.Errorf("Open = %q, want %q", got, "peerpass")
	}
}

func TestSealEmptyPasswordIsNil(t *testing.T) {
	blob, err := Seal(testKey(1), team, "")
	if err != nil || blob != nil {
		t.Fatalf("Seal(empty) = %v, %v; want nil, nil", blob, err)
	}
	got, err := Open(testKey(1), team, nil)
	if err != nil || got != "" {
		t.Fatalf("Open(nil) = %q, %v; want empty, nil", got, err)
	}
}

func TestOpenRejectsEachBindingChange(t *testing.T) {
	key := testKey(1)
	blob, err := Seal(key, team, "peerpass")
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string]Binding{
		"name":           {Name: "other", URL: team.URL, Username: team.Username},
		"url":            {Name: team.Name, URL: "https://other.example/db", Username: team.Username},
		"url case":       {Name: team.Name, URL: "https://PEER.example/db", Username: team.Username},
		"url trailing /": {Name: team.Name, URL: team.URL + "/", Username: team.Username},
		"username":       {Name: team.Name, URL: team.URL, Username: "other"},
		"no username":    {Name: team.Name, URL: team.URL},
		// Field boundaries are length-prefixed, so moving bytes between
		// adjacent fields is a different binding.
		"shifted boundary": {Name: "tea", URL: "m" + team.URL, Username: team.Username},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(key, b, blob)
			if !errors.Is(err, ErrNotAuthenticated) {
				t.Fatalf("Open = %v, want ErrNotAuthenticated", err)
			}
			if !strings.Contains(err.Error(), "cipher: message authentication failed") {
				t.Errorf("error %q does not keep the cipher cause", err)
			}
		})
	}
}

func TestOpenRejectsWrongKeyAndMalformedFrames(t *testing.T) {
	key := testKey(1)
	blob, err := Seal(key, team, "peerpass")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(testKey(2), team, blob); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("wrong key: %v, want ErrNotAuthenticated", err)
	}
	for name, b := range map[string][]byte{
		"prefix only": []byte("v2:"),
		"truncated":   blob[:len(blob)-1],
		"short":       []byte("v2:short"),
	} {
		if _, err := Open(key, team, b); !errors.Is(err, ErrNotAuthenticated) {
			t.Errorf("%s: %v, want ErrNotAuthenticated", name, err)
		}
	}
	if _, err := Open(testKey(1)[:16], team, blob); err == nil {
		t.Error("16-byte key accepted, want the 32-byte key required")
	}
}

// Normal Open never authenticates the legacy unbound format, even when a
// random legacy nonce happens to begin with the v2 prefix.
func TestOpenNeverFallsBackToLegacy(t *testing.T) {
	key := testKey(1)
	plain := legacySeal(t, key, []byte("0123456789ab"), "peerpass")
	if _, err := Open(key, team, plain); !errors.Is(err, ErrLegacyFormat) {
		t.Errorf("legacy blob: %v, want ErrLegacyFormat", err)
	}
	prefixed := legacySeal(t, key, []byte("v2:456789ab"+"c"), "peerpass")
	if _, err := Open(key, team, prefixed); !errors.Is(err, ErrNotAuthenticated) {
		t.Errorf("legacy blob with v2-looking nonce: %v, want ErrNotAuthenticated", err)
	}
}

func TestAssociatedDataIsUnambiguous(t *testing.T) {
	a, err := Binding{Name: "ab", URL: "c"}.associatedData()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Binding{Name: "a", URL: "bc"}.associatedData()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Error("distinct bindings encode to the same associated data")
	}
}
