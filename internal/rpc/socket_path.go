//go:build !windows

package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/steveyegge/beads/internal/utils"
)

// MaxUnixSocketPath is the maximum length for Unix socket paths.
// macOS has a 104-byte limit (including null terminator), Linux has 108.
// We use 103 to be safe across platforms.
const MaxUnixSocketPath = 103

// ShortSocketPath returns a short socket path suitable for Unix sockets.
// On Unix systems with socket path length limits (macOS: 104 chars, Linux: 108),
// this function returns a path in /tmp/beads-{hash}/ to avoid exceeding limits.
//
// The hash is derived from the canonicalized workspace path, ensuring:
// - Different workspaces get different socket directories
// - The same workspace always gets the same hash (deterministic)
// - Symlinks and case differences resolve to the same hash
//
// If the computed .beads/bd.sock path is short enough, it returns that directly.
// This preserves backwards compatibility for workspaces with short paths.
func ShortSocketPath(workspacePath string) string {
	// Canonicalize path for consistent hashing across symlinks and case
	canonical := utils.NormalizePathForComparison(workspacePath)
	if canonical == "" {
		canonical = workspacePath
	}

	// Compute the "natural" socket path in .beads/
	naturalPath := filepath.Join(workspacePath, ".beads", "bd.sock")

	// If natural path is short enough, use it (backwards compatible)
	if len(naturalPath) <= MaxUnixSocketPath {
		return naturalPath
	}

	// Path too long - use /tmp with hash
	return shortSocketDir(canonical)
}

// shortSocketDir returns a socket path in /tmp/beads-{hash}/.
// The hash is 8 hex characters derived from SHA256 of the workspace path.
func shortSocketDir(canonicalPath string) string {
	hash := sha256.Sum256([]byte(canonicalPath))
	hashStr := hex.EncodeToString(hash[:4]) // 8 hex chars from 4 bytes

	dir := filepath.Join(tmpDir(), "beads-"+hashStr)
	return filepath.Join(dir, "bd.sock")
}

// tmpDir returns the appropriate temp directory for sockets.
// On macOS, /tmp is a symlink to /private/tmp, but /tmp is shorter.
func tmpDir() string {
	if runtime.GOOS == "darwin" {
		// On macOS, prefer /tmp over $TMPDIR which can be long
		// (/var/folders/xx/xxxxxxxxxxxx/T/)
		return "/tmp"
	}
	// On Linux and other Unix, use /tmp
	return "/tmp"
}

// EnsureSocketDir creates the socket directory if it doesn't exist.
// Returns the socket path (unchanged) and any error.
// This should be called by the daemon before listening.
func EnsureSocketDir(socketPath string) (string, error) {
	dir := filepath.Dir(socketPath)

	// Only create if it's a /tmp/beads-* directory
	// Don't create .beads directories - those should exist
	if strings.HasPrefix(dir, filepath.Join(tmpDir(), "beads-")) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
	}

	return socketPath, nil
}

// CleanupSocketDir removes the socket directory if it's in /tmp/beads-*.
// This should be called when the daemon shuts down.
func CleanupSocketDir(socketPath string) error {
	dir := filepath.Dir(socketPath)

	// Only remove if it's a /tmp/beads-* directory we created
	if strings.HasPrefix(dir, filepath.Join(tmpDir(), "beads-")) {
		// Remove socket file first
		_ = os.Remove(socketPath)
		// Remove directory (will fail if not empty, which is fine)
		return os.Remove(dir)
	}

	// For .beads/ directories, just remove the socket file
	return os.Remove(socketPath)
}

// NeedsShortPath returns true if the workspace path would result in a socket
// path exceeding Unix limits.
func NeedsShortPath(workspacePath string) bool {
	naturalPath := filepath.Join(workspacePath, ".beads", "bd.sock")
	return len(naturalPath) > MaxUnixSocketPath
}
