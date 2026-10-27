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
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/doltremote"
)

type Binding struct {
	Name      string
	RemoteURL string
	Username  string
}

var ErrLegacyCredential = errors.New("stored peer password uses the legacy unbound format")

const MigrationMarkerFile = ".beads-credential-key.v2-migrated"

const (
	v2Prefix        = "v2:"
	associatedLabel = "beads-federation-peer-credential-v2"
	maxFieldLength  = uint64(1<<32 - 1)
)

func Seal(key []byte, password string, binding Binding) ([]byte, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	ad, err := associatedData(binding)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	blob := make([]byte, len(v2Prefix), len(v2Prefix)+len(nonce)+len(password)+aead.Overhead())
	copy(blob, v2Prefix)
	blob = append(blob, nonce...)
	return aead.Seal(blob, nonce, []byte(password), ad), nil
}

func Open(key []byte, blob []byte, binding Binding) (string, error) {
	if !IsV2(blob) {
		return "", ErrLegacyCredential
	}
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	blob = blob[len(v2Prefix):]
	if len(blob) < aead.NonceSize()+aead.Overhead() {
		return "", fmt.Errorf("ciphertext too short")
	}
	ad, err := associatedData(binding)
	if err != nil {
		return "", err
	}
	nonce, ciphertext := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, ad)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func OpenLegacy(key []byte, blob []byte) (string, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return "", err
	}
	if len(blob) < aead.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := blob[:aead.NonceSize()], blob[aead.NonceSize():]
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func IsV2(blob []byte) bool {
	return len(blob) >= len(v2Prefix) && bytes.Equal(blob[:len(v2Prefix)], []byte(v2Prefix))
}

// DuplicateBlobs returns blobs stored in more than one row. Copies of one ciphertext cannot be told apart, so none of them is migrated.
func DuplicateBlobs(blobs [][]byte) map[string]bool {
	counts := make(map[string]int, len(blobs))
	for _, blob := range blobs {
		counts[string(blob)]++
	}
	duplicates := make(map[string]bool)
	for blob, count := range counts {
		if count > 1 {
			duplicates[blob] = true
		}
	}
	return duplicates
}

// RemoteURLMatches reports whether a local Dolt remote URL matches the peer
// row's remote_url after canonical comparison, and both are non-empty.
func RemoteURLMatches(localRemoteURL, peerRemoteURL string) bool {
	if localRemoteURL == "" || peerRemoteURL == "" {
		return false
	}
	return doltremote.CanonicalForComparison(localRemoteURL) == doltremote.CanonicalForComparison(peerRemoteURL)
}

func MigrationDone(dir string) bool {
	if dir == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, MigrationMarkerFile)) //nolint:gosec // G304: dir is the store's trusted beadsDir.
	return err == nil && !info.IsDir()
}

func MarkMigrated(dir string) error {
	if dir == "" {
		return fmt.Errorf("credential migration directory is empty")
	}
	marker, err := os.OpenFile(filepath.Join(dir, MigrationMarkerFile), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G304: dir is the store's trusted beadsDir.
	if err != nil {
		return err
	}
	if err := marker.Chmod(0o600); err != nil {
		_ = marker.Close()
		return err
	}
	if err := marker.Close(); err != nil {
		return err
	}
	return nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func associatedData(binding Binding) ([]byte, error) {
	fields := []string{
		binding.Name,
		doltremote.CanonicalForComparison(binding.RemoteURL),
		binding.Username,
	}
	ad := make([]byte, 0, len(associatedLabel)+len(binding.Name)+len(binding.RemoteURL)+len(binding.Username)+12)
	ad = append(ad, associatedLabel...)
	for _, field := range fields {
		if uint64(len(field)) > maxFieldLength {
			return nil, fmt.Errorf("peer credential binding field is too long")
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		ad = append(ad, length[:]...)
		ad = append(ad, field...)
	}
	return ad, nil
}
