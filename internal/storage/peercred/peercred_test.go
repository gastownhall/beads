package peercred

import (
	"bytes"
	"errors"
	"testing"
)

// prodBinding is the identity a legitimate peer's password is sealed under;
// attackerBinding differs in the one field each subtest names. The attacker
// URL points at a host the attacker controls, the shape GH#7890 reports.
var prodBinding = Binding{
	Name:       "prod",
	RemoteURL:  "https://prod.example/db",
	Username:   "produser",
}

func testKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	return key
}

// AC-1: a sealed password opens with its original key and binding.
func TestSealOpenRoundTrip(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("prod-s3cret", key, prodBinding)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	got, err := Open(blob, key, prodBinding)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if got != "prod-s3cret" {
		t.Fatalf("Open() = %q, want %q", got, "prod-s3cret")
	}
}

// AC-1: the sealed blob is v2-wrapped — an Open with a different binding
// must fail in the GCM authentication, not parse as some other payload.
func TestSealIsVersioned(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("prod-s3cret", key, prodBinding)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if !bytes.HasPrefix(blob, []byte(versionPrefix)) {
		t.Fatalf("Seal() output %q lacks the %q version prefix", blob, versionPrefix)
	}
}

// AC-1: an empty password seals to nil, preserving the stores'
// no-password-stored representation, and an empty blob opens to "".
func TestEmptyPassword(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("", key, prodBinding)
	if err != nil {
		t.Fatalf("Seal(\"\") error = %v", err)
	}
	if blob != nil {
		t.Fatalf("Seal(\"\") = %v, want nil", blob)
	}
	got, err := Open(nil, key, prodBinding)
	if err != nil {
		t.Fatalf("Open(nil) error = %v", err)
	}
	if got != "" {
		t.Fatalf("Open(nil) = %q, want \"\"", got)
	}
}

// AC-1: changing any bound field — the copied-row attack of GH#7890, in
// every direction the row can diverge — fails the open.
func TestOpenFailsWhenBindingDiffers(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("prod-s3cret", key, prodBinding)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}

	// The attacker row: its own name and URL, the victim's username, the
	// victim's ciphertext copied verbatim.
	attacker := Binding{Name: "evil", RemoteURL: "https://attacker.example/db", Username: "produser"}
	cases := []struct {
		name string
		b    Binding
	}{
		{"copied to another peer name and url", attacker},
		{"another peer name, same url", Binding{Name: "evil", RemoteURL: prodBinding.RemoteURL, Username: prodBinding.Username}},
		{"another url, same name", Binding{Name: prodBinding.Name, RemoteURL: "https://attacker.example/db", Username: prodBinding.Username}},
		{"another username", Binding{Name: prodBinding.Name, RemoteURL: prodBinding.RemoteURL, Username: "otheruser"}},
		{"empty username", Binding{Name: prodBinding.Name, RemoteURL: prodBinding.RemoteURL}},
		{"empty binding", Binding{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Open(blob, key, tc.b)
			if err == nil {
				t.Fatalf("Open() = %q, want failure for a diverged binding", got)
			}
			if got != "" {
				t.Errorf("Open() released %q alongside an error", got)
			}
		})
	}
}

// AC-1: a different key fails the open. A wrong key and a diverged binding
// are indistinguishable by design; this pins that both reach the same GCM
// authentication error rather than some distinguishable failure.
func TestOpenFailsWithWrongKey(t *testing.T) {
	key := testKey(t)
	blob, err := Seal("prod-s3cret", key, prodBinding)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(i + 99)
	}
	if got, err := Open(blob, other, prodBinding); err == nil {
		t.Fatalf("Open() = %q, want failure with a wrong key", got)
	}
}

// AC-3: a blob without the v2 prefix — the pre-binding codec's nil-AAD
// output, or any other bytes — is refused as legacy, not parsed.
func TestOpenRefusesLegacyBlob(t *testing.T) {
	key := testKey(t)
	legacy := []byte("not a v2 blob, e.g. an old nil-AAD AES-GCM payload")
	for _, blob := range [][]byte{legacy, {}} {
		if len(blob) == 0 {
			continue
		}
		_, err := Open(blob, key, prodBinding)
		if !errors.Is(err, ErrLegacyCredential) {
			t.Fatalf("Open(legacy) error = %v, want ErrLegacyCredential", err)
		}
	}
	// A truncated v2 blob is corruption, not legacy: it carries the prefix
	// but is too short for a nonce.
	v2, err := Seal("prod-s3cret", key, prodBinding)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	_, err = Open(v2[:len(versionPrefix)+2], key, prodBinding)
	if err == nil {
		t.Fatal("Open(truncated v2) succeeded, want ciphertext too short")
	}
}

// AC-3: a legacy refusal is never a usable credential — IsLegacy exists so
// callers can spell the re-entry remediation without matching on prose.
func TestIsLegacy(t *testing.T) {
	if !IsLegacy(ErrLegacyCredential) {
		t.Error("IsLegacy(ErrLegacyCredential) = false, want true")
	}
	if !IsLegacy(wrappedLegacy()) {
		t.Error("IsLegacy(wrapped ErrLegacyCredential) = false, want true")
	}
	if IsLegacy(errors.New("cipher: message authentication failed")) {
		t.Error("IsLegacy(auth failure) = true, want false")
	}
}

func wrappedLegacy() error {
	return errors.Join(errors.New("decrypt password for peer prod: "), ErrLegacyCredential)
}