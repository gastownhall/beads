// Package identity provides shared managed dbproxy identity primitives.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/atomicfile"
)

// SecretFileName is the per-workspace secret used to authenticate control
// listener requests.
const SecretFileName = "proxy.secret"

// RootID returns the SHA-256 of rootDir's symlink-resolved absolute path.
// It identifies the workspace proxy root, not the Dolt data directory;
// upstream_id continues to identify the backend through DoltServer.ID.
// Darwin's default case-insensitive filesystems can resolve the same directory
// through differently cased path spellings; callers should use a canonical
// workspace spelling when they need stable IDs across invocations.
func RootID(rootDir string) (string, error) {
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return "", fmt.Errorf("identity: absolute root path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("identity: resolve root path: %w", err)
	}
	sum := sha256.Sum256([]byte(resolved))
	return hex.EncodeToString(sum[:]), nil
}

// WriteSecret creates and atomically writes a new control-listener secret.
// Each proxy start intentionally rotates the previous secret.
func WriteSecret(rootDir string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("identity: generate proxy secret: %w", err)
	}
	secret := hex.EncodeToString(raw)
	if err := atomicfile.WriteFile(filepath.Join(rootDir, SecretFileName), []byte(secret+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("identity: write proxy secret: %w", err)
	}
	return secret, nil
}

// ReadSecret reads and validates the control-listener secret.
func ReadSecret(rootDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(rootDir, SecretFileName)) // #nosec G304 - rootDir is the workspace proxy root, not user input
	if err != nil {
		return "", fmt.Errorf("identity: read proxy secret: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if len(secret) != 64 {
		return "", errors.New("identity: invalid proxy secret")
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return "", errors.New("identity: invalid proxy secret")
	}
	return secret, nil
}
