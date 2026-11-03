// Package peercred seals and opens the federation peer passwords both Dolt
// stores keep in federation_peers.password_encrypted.
//
// federation_peers rows replicate with the database, so a peer can write a
// row naming its own URL that carries another row's ciphertext and username.
// The legacy format (nonce || AES-GCM, no associated data) authenticates such
// a copied blob, and the victim then sends the decrypted password to the
// attacker on the next remote operation (GH#7890). The v2 format binds the
// ciphertext to the row it was written for: the associated data is the
// stored peer name, the exact stored remote URL and the username, so a blob
// moved to any other binding fails authentication.
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
	"math"
)

// KeySize is the length of the machine-local credential key (AES-256).
const KeySize = 32

// v2Prefix opens every v2 ciphertext: v2Prefix || nonce || sealed data.
const v2Prefix = "v2:"

// v2Domain separates v2 associated data from any other use of the key.
const v2Domain = "beads federation peer credential v2\x00"

// ErrLegacyFormat reports a ciphertext without the v2 framing. Normal reads
// never fall back to the unbound legacy open; only migration may read it.
var ErrLegacyFormat = errors.New("stored peer credential uses the legacy format, which is not bound to the peer's name, URL and username")

// ErrNotAuthenticated reports a v2 ciphertext that does not authenticate
// under this key for this binding. AES-GCM cannot tell the causes apart: the
// blob was sealed for another peer row, under another machine's key, or it is
// corrupt.
var ErrNotAuthenticated = errors.New("stored peer credential does not authenticate for this peer's name, URL and username under this machine's key")

// Binding is the peer identity a credential is sealed to: the stored peer
// name, the stored remote URL byte for byte, and the username (SQL NULL is
// the empty string). Sovereignty and sync timestamps are not part of it.
type Binding struct {
	Name     string
	URL      string
	Username string
}

// associatedData is the v2 domain label followed by each binding field as a
// uint32 big-endian byte length and the bytes, so no two bindings encode
// alike.
func (b Binding) associatedData() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(v2Domain)
	for _, field := range []string{b.Name, b.URL, b.Username} {
		if uint64(len(field)) > math.MaxUint32 {
			return nil, fmt.Errorf("peer credential binding field too long (%d bytes)", len(field))
		}
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(field))) //nolint:gosec // G115: bounded above
		buf.Write(n[:])
		buf.WriteString(field)
	}
	return buf.Bytes(), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("credential key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts password as a v2 ciphertext bound to b. An empty password
// seals to nil, matching the stores' "no stored password" representation.
func Seal(key []byte, b Binding, password string) ([]byte, error) {
	if password == "" {
		return nil, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ad, err := b.associatedData()
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(v2Prefix), len(v2Prefix)+gcm.NonceSize()+len(password)+gcm.Overhead())
	copy(out, v2Prefix)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, []byte(password), ad), nil
}

// Open decrypts a v2 ciphertext sealed for b. It never reads the legacy
// format: a blob without the v2 framing is ErrLegacyFormat, and a v2 blob
// that does not authenticate for b is ErrNotAuthenticated (wrapping the
// cipher error). An empty blob opens to the empty password.
func Open(key []byte, b Binding, blob []byte) (string, error) {
	if len(blob) == 0 {
		return "", nil
	}
	if !bytes.HasPrefix(blob, []byte(v2Prefix)) {
		return "", ErrLegacyFormat
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	ad, err := b.associatedData()
	if err != nil {
		return "", err
	}
	body := blob[len(v2Prefix):]
	if len(body) < gcm.NonceSize()+gcm.Overhead() {
		return "", fmt.Errorf("%w: ciphertext too short", ErrNotAuthenticated)
	}
	nonce, sealed := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, ad)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotAuthenticated, err)
	}
	return string(plaintext), nil
}
