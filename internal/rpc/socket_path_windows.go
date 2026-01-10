//go:build windows

package rpc

import (
	"os"
	"path/filepath"
)

// MaxUnixSocketPath is not applicable on Windows (uses TCP).
// Kept for API compatibility.
const MaxUnixSocketPath = 103

// ShortSocketPath returns the socket path for Windows.
// Windows uses TCP instead of Unix sockets, so path length is not a concern.
// The "socket path" is actually a file containing the TCP endpoint info.
func ShortSocketPath(workspacePath string) string {
	return filepath.Join(workspacePath, ".beads", "bd.sock")
}

// EnsureSocketDir is a no-op on Windows since the .beads directory
// should already exist.
func EnsureSocketDir(socketPath string) (string, error) {
	return socketPath, nil
}

// CleanupSocketDir removes the socket file on Windows.
func CleanupSocketDir(socketPath string) error {
	return os.Remove(socketPath)
}

// NeedsShortPath always returns false on Windows since TCP is used.
func NeedsShortPath(workspacePath string) bool {
	return false
}
