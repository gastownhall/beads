// Package peercred seals and opens federation peer credentials, binding each
// stored ciphertext to the identity of the federation_peers row it was written
// for: the peer name, the raw remote URL, and the username.
//
// federation_peers rows replicate with the database, so a peer that controls
// its own row can copy another row's password_encrypted and username into it
// (GH#7890 findings 32/38). A nil-AAD ciphertext decrypts for any row, which
// turns the next sync into a password disclosure to the attacker's URL. The
// v2 format seals the password with all three binding fields as AES-GCM
// associated data, so a copied ciphertext fails authentication under the
// attacker's identity and no credential is released.
//
// The package is pure crypto and classification: it takes keys, bindings, and
// blobs, and never touches SQL, the filesystem, or the environment. The drivers
// (internal/storage/dolt, internal/storage/embeddeddolt) own connections,
// migration orchestration, and the marker file.
package peercred

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// v2Prefix marks v2 ciphertext. A full v2 blob is
// "v2:" || nonce (gcm.NonceSize()) || AES-GCM sealed data.
const v2Prefix = "v2:"

// v2Domain labels the v2 associated data so a v2 blob cannot be replayed as
// ciphertext sealed under any other scheme or field layout.
const v2Domain = "beads.peercred.v2"

// markerDomain labels the migration marker's HMAC so the marker authenticates
// the key that finished the migration, not just any key-shaped file.
const markerDomain = "beads.peercred.v2-migrated"

// KeySize is the required credential key length (AES-256), matching the
// 32-byte random key both drivers store beside the database.
const KeySize = 32

// MarkerFile is the migration completion marker, written beside the credential
// key (.beads-credential-key) once every legacy row this machine can read has
// been re-sealed as v2. Its content is MarkerValue under the finishing key.
const MarkerFile = ".beads-credential-key.v2-migrated" //nolint:gosec // G101: filename, not a credential

// ErrNotV2 reports a blob that does not carry the v2 marker. Normal
// authentication never falls back to the legacy framing, so this is the
// classification for rows that predate the binding and were never migrated.
var ErrNotV2 = errors.New("stored peer credential is not in the v2 identity-bound format")

// ErrAuthentication reports a ciphertext that failed AES-GCM authentication.
// It covers a copied ciphertext under the wrong binding, a wrong local key,
// and corruption; no single one of those causes can be inferred from the
// failure alone.
var ErrAuthentication = errors.New("peer credential failed authentication: it does not match this peer row's identity or this machine's credential key")

// ErrBadKey reports a key of the wrong length.
var ErrBadKey = errors.New("credential key must be 32 bytes")

// Binding identifies the federation peer row a credential belongs to: the
// stored peer name, the raw remote URL exactly as stored, and the username
// (SQL NULL normalized to the drivers' empty-string representation).
//
// The binding uses the exact stored URL bytes, not a canonicalized form: the
// existing canonicalizer folds case in places where native scheme authorities
// stay case-sensitive, so it is unsuitable as a security identity. An operator
// changes a peer's identity by re-running add-peer, which already writes URL,
// username, and sealed password together.
type Binding struct {
	Name     string
	URL      string
	Username string
}

// associatedData builds the v2 AAD: a fixed domain label, then each binding
// field as uint32 big-endian byte length plus bytes. Length prefixes make the
// encoding unambiguous; lengths are validated rather than truncated.
func associatedData(b Binding) ([]byte, error) {
	aad := make([]byte, 0, len(v2Domain)+3*(4+len(b.Name)+len(b.URL)+len(b.Username)))
	aad = append(aad, v2Domain...)
	for _, field := range []string{b.Name, b.URL, b.Username} {
		if uint64(len(field)) > math.MaxUint32 {
			return nil, fmt.Errorf("peer binding field too long to represent (%d bytes)", len(field))
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		aad = append(aad, length[:]...)
		aad = append(aad, field...)
	}
	return aad, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrBadKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts password under key for the peer row described by b and returns
// the v2 blob. An empty password is the caller's to short-circuit (both
// drivers store no ciphertext for one); Seal seals whatever it is given.
func Seal(password string, key []byte, b Binding) ([]byte, error) {
	aad, err := associatedData(b)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	out := make([]byte, 0, len(v2Prefix)+len(nonce)+len(password)+gcm.Overhead())
	out = append(out, v2Prefix...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, []byte(password), aad), nil
}

// IsV2 reports whether blob carries the v2 marker prefix. Prefix presence
// alone does not make a blob migrated or even authentic: it only routes the
// blob to the v2 framing, whose GCM tag is the actual authentication.
func IsV2(blob []byte) bool {
	return bytes.HasPrefix(blob, []byte(v2Prefix))
}

// Open decrypts a v2 blob under key, authenticating it against b, and returns
// the original password.
//
// Open accepts v2 blobs only and never falls back to the legacy nil-AAD
// framing: a rejected credential is withheld, not retried under a weaker
// scheme. Legacy blobs are opened exclusively by the migration path
// (OpenLegacy), and only while the migration has not completed.
func Open(blob []byte, key []byte, b Binding) (string, error) {
	if !IsV2(blob) {
		return "", ErrNotV2
	}
	aad, err := associatedData(b)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	rest := blob[len(v2Prefix):]
	if len(rest) < gcm.NonceSize() {
		return "", fmt.Errorf("%w: ciphertext too short", ErrAuthentication)
	}
	nonce, sealed := rest[:gcm.NonceSize()], rest[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return "", ErrAuthentication
	}
	return string(plaintext), nil
}

// OpenLegacy opens a legacy nil-AAD blob (nonce || sealed data) under key.
//
// Migration only. No normal authentication path may call this: the legacy
// framing carries no binding, so it authenticates a copied blob just as
// readily as the original — the defect this package exists to close. Only the
// one-time migration, which first proves each blob's provenance is
// unambiguous, may decrypt legacy rows, and only until its marker exists.
func OpenLegacy(blob []byte, key []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(blob) < gcm.NonceSize() {
		return "", fmt.Errorf("legacy ciphertext too short")
	}
	nonce, sealed := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("legacy authentication failed: %w", err)
	}
	return string(plaintext), nil
}

// MarkerValue is the content of the migration marker file for key: a
// domain-separated HMAC-SHA256 under key. A marker only proves completion for
// the key that matches its HMAC; an unrelated key's marker is not completion
// evidence and must not stop that key's migration.
func MarkerValue(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(markerDomain))
	return mac.Sum(nil)
}

// MarkerMatches reports whether content, read from a marker file, is the
// completion marker for key.
func MarkerMatches(content []byte, key []byte) bool {
	return len(content) == sha256.Size && hmac.Equal(content, MarkerValue(key))
}