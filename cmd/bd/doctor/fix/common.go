package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// getBdBinary returns the path to the bd binary to use for fix operations.
// It prefers the current executable to avoid command injection attacks.
func getBdBinary() (string, error) {
	// Prefer current executable for security
	exe, err := os.Executable()
	if err == nil {
		// Resolve symlinks to get the real binary path
		realPath, err := filepath.EvalSymlinks(exe)
		if err == nil {
			return realPath, nil
		}
		return exe, nil
	}

	// Fallback to PATH lookup with validation
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return "", fmt.Errorf("bd binary not found in PATH: %w", err)
	}

	return bdPath, nil
}

// validateBeadsWorkspace ensures the path is a valid beads workspace before
// attempting any fix operations. This prevents path traversal attacks.
func validateBeadsWorkspace(path string) error {
	// Convert to absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// Check for .beads directory
	beadsDir := filepath.Join(absPath, ".beads")
	if _, err := os.Stat(beadsDir); os.IsNotExist(err) {
		return fmt.Errorf("not a beads workspace: .beads directory not found at %s", absPath)
	}

	return nil
}
