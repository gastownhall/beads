// Package peercred seals and opens federation peer passwords with the peer's
// identity bound as AES-GCM associated data (GH#7890).
//
// federation_peers rows replicate between federation partners, but the
// encryption key does not, so a partner can copy another peer's
// password_encrypted (and username) into a row that points at its own URL.
// Sealing the password with the peer's (name, remote URL, username) as
// authenticated data makes that copy useless: the ciphertext fails its GCM
// authentication when it is opened for any other identity, and a failed open
// looks exactly like a wrong-key failure, so no information about the
// password leaks either way.
//
// Blobs sealed here carry a "v2:" version prefix so a pre-binding blob
// (sealed by the older codecs in the dolt and embeddeddolt stores with nil
// associated data) can be told apart and refused with ErrLegacyCredential
// rather than silently enrolled under an unverified identity.
package peercred

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// versionPrefix marks blobs sealed by this package. It doubles as part of the
// associated-data domain: a blob is only ever opened through Open, which
// requires the prefix, so it needs no resistance to crafting.
const versionPrefix = "v2:"

// aadDomain labels the associated data so a v2 blob cannot be confused with
// any other AES-GCM payload that happens to reuse the key.
const aadDomain = "beads-federation-peer-credential"

// ErrLegacyCredential reports a stored password that predates identity
// binding: it was sealed by an older codec with no associated data, so a
// successful decrypt could not prove the row is the binding the password was
// enrolled for. Callers wrap it with storage.CredentialKeyMismatchError and
// refuse to use the credential; re-entering the password with add-peer seals
// a bound v2 blob.
var ErrLegacyCredential = errors.New("stored peer password predates peer-identity binding (legacy format) and is refused until the password is re-entered")

// Binding identifies the federation peer a sealed password belongs to. The
// values are the raw stored row values: the name and URL as add-peer wrote
// them, the username (empty for a NULL row). They are not canonicalized —
// two spellings of the same URL are two bindings, which is the fail-closed
// direction; equivalences that Dolt provably reads back identical can be
// accepted at the destination check instead (see the stores'
// remote-URL verification), where a refusal is visible and actionable,
// rather than baked into the credential.
type Binding struct {
	Name       string
	RemoteURL  string
	Username   string
}

// Seal encrypts plaintext for key under the identity b. An empty plaintext
// seals to nil, matching the stores' empty-password behavior (no password
// stored, nothing to seal). The output is versionPrefix, the GCM nonce, then
// ciphertext and tag.
func Seal(plaintext string, key []byte, b Binding) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(versionPrefix)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, versionPrefix...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, []byte(plaintext), b.aad()), nil
}

// Open decrypts a blob sealed by Seal for the same key and identity. A blob
// without the version prefix is a legacy password and fails with
// ErrLegacyCredential; a v2 blob sealed or opened for any other binding or
// key fails with the GCM authentication error. Callers classify both through
// storage.CredentialKeyMismatchError — a wrong key and a wrong identity are
// indistinguishable on purpose — while ErrLegacyCredential stays reachable
// through errors.Is for the legacy re-entry remediation.
func Open(blob []byte, key []byte, b Binding) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	if !bytes.HasPrefix(blob, []byte(versionPrefix)) {
		return "", ErrLegacyCredential
	}
	data := blob[len(versionPrefix):]
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, b.aad())
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// IsLegacy reports whether err is the ErrLegacyCredential refusal, for
// callers that spell the re-entry remediation themselves.
func IsLegacy(err error) bool {
	return errors.Is(err, ErrLegacyCredential)
}

// aad builds the associated data: the domain label, then each bound value as
// a four-byte big-endian length followed by the raw bytes. The length
// prefixes make the concatenation unambiguous ("ab"+"c" never equals
// "a"+"bc"), so every distinct (name, URL, username) tuple is a distinct
// authentication.
func (b Binding) aad() []byte {
	buf := make([]byte, 0, len(aadDomain)+3*4+len(b.Name)+len(b.RemoteURL)+len(b.Username))
	buf = append(buf, aadDomain...)
	for _, v := range [...]string{b.Name, b.RemoteURL, b.Username} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(v)))
		buf = append(buf, length[:]...)
		buf = append(buf, v...)
	}
	return buf
}

// newGCM builds the AES-256-GCM for key. The stores load or generate exactly
// a 32-byte key, so aes.NewCipher only fails on a mis-sized key, which is the
// caller's bug and surfaces unwrapped.
func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}