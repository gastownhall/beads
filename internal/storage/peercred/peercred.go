// Package peercred seals and opens federation peer passwords bound to the
// peer identity they were stored for.
//
// federation_peers rows travel with the database, so a partner can copy one
// peer's password_encrypted (and username) into a row naming its own remote
// (GH#7890). Sealing binds the ciphertext to the row's raw (name, remote_url,
// username) through AES-GCM associated data: a copied blob read under any
// other identity fails authentication instead of decrypting.
package peercred

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// v2Prefix marks a password sealed with identity-bound associated data.
// Blobs without it are the legacy unbound format.
const v2Prefix = "v2:"

// aadLabel versions the associated data so a future layout cannot be confused
// with this one.
const aadLabel = "beads federation peer credential v2"

// ErrLegacyCredential reports a stored password in the legacy format, sealed
// without identity binding. It is never decrypted: a legacy blob that opens
// cannot prove it was stored for the row now carrying it, so the password
// must be re-entered with add-peer.
var ErrLegacyCredential = errors.New("stored peer password uses the legacy unbound format and must be re-entered")

// errCiphertextTooShort reports a v2 blob too short to carry its nonce.
var errCiphertextTooShort = errors.New("ciphertext too short")

// Identity is the federation_peers row a password is bound to. Values are
// the raw stored column values; a NULL username is the empty string.
type Identity struct {
	Name      string
	RemoteURL string
	Username  string
}

// aad encodes the label and each identity field with a four-byte big-endian
// length prefix, so no two distinct identities share an encoding.
func (id Identity) aad() []byte {
	fields := []string{aadLabel, id.Name, id.RemoteURL, id.Username}
	n := 0
	for _, f := range fields {
		n += 4 + len(f)
	}
	out := make([]byte, 0, n)
	for _, f := range fields {
		out = binary.BigEndian.AppendUint32(out, uint32(len(f))) //nolint:gosec // G115: row values are far below 4 GiB
		out = append(out, f...)
	}
	return out
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts password for id with key. An empty password seals to nil,
// matching a peer stored without a password.
func Seal(key []byte, id Identity, password string) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	out := make([]byte, 0, len(v2Prefix)+len(nonce)+len(password)+gcm.Overhead())
	out = append(out, v2Prefix...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, []byte(password), id.aad()), nil
}

// Open decrypts blob for id with key. An empty blob opens to the empty
// password. A blob without the v2 prefix returns ErrLegacyCredential and is
// never decrypted. A wrong key and a changed identity are indistinguishable:
// both return the bare authentication error, which callers classify.
func Open(key []byte, id Identity, blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	if len(blob) < len(v2Prefix) || string(blob[:len(v2Prefix)]) != v2Prefix {
		return "", ErrLegacyCredential
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	body := blob[len(v2Prefix):]
	if len(body) < gcm.NonceSize() {
		return "", errCiphertextTooShort
	}
	nonce, ciphertext := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, id.aad())
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}
