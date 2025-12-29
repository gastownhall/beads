package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/beads"
)

// ErrTestBinary is returned when getBdBinary detects it's running as a test binary.
// This prevents fork bombs when tests call functions that execute bd subcommands.
var ErrTestBinary = fmt.Errorf("running as test binary - cannot execute bd subcommands")

func newBdCmd(bdBinary string, args ...string) *exec.Cmd {
	fullArgs := append([]string{"--no-daemon"}, args...)
	cmd := exec.Command(bdBinary, fullArgs...) // #nosec G204 -- bdBinary from validated executable path
	cmd.Env = append(os.Environ(), "BEADS_NO_DAEMON=1")
	return cmd
}

// getBdBinary returns the path to the bd binary to use for fix operations.
// It prefers the current executable to avoid command injection attacks.
// Returns ErrTestBinary if running as a test binary to prevent fork bombs.
func getBdBinary() (string, error) {
	// Prefer current executable for security
	exe, err := os.Executable()
	if err == nil {
		// Resolve symlinks to get the real binary path
		realPath, err := filepath.EvalSymlinks(exe)
		if err == nil {
			exe = realPath
		}

		// Check if we're running as a test binary - this prevents fork bombs
		// when tests call functions that execute bd subcommands
		baseName := filepath.Base(exe)
		if strings.HasSuffix(baseName, ".test") || strings.Contains(baseName, ".test.") {
			return "", ErrTestBinary
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

// safeWorkspacePath resolves relPath within the workspace root and ensures it
// cannot escape the workspace via path traversal.
func safeWorkspacePath(root, relPath string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("invalid workspace path: %w", err)
	}

	cleanRel := filepath.Clean(relPath)
	if filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("expected relative path, got absolute: %s", relPath)
	}

	joined := filepath.Join(absRoot, cleanRel)
	rel, err := filepath.Rel(absRoot, joined)
	if err != nil {
		return "", fmt.Errorf("failed to resolve path: %w", err)
	}

	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace: %s", relPath)
	}

	return joined, nil
}

// isWithinWorkspace reports whether candidate resides within the workspace root.
func isWithinWorkspace(root, candidate string) bool {
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	cleanCandidate := filepath.Clean(candidate)
	rel, err := filepath.Rel(cleanRoot, cleanCandidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// resolveBeadsDir follows .beads/redirect files to find the actual beads directory.
// If no redirect exists, returns the original path unchanged.
func resolveBeadsDir(beadsDir string) string {
	redirectFile := filepath.Join(beadsDir, beads.RedirectFileName)
	data, err := os.ReadFile(redirectFile) //nolint:gosec // redirect file path is constructed from known beadsDir
	if err != nil {
		// No redirect file - use original path
		return beadsDir
	}

	// Parse the redirect target
	target := strings.TrimSpace(string(data))
	if target == "" {
		return beadsDir
	}

	// Skip comments
	lines := strings.Split(target, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			target = line
			break
		}
	}

	// Resolve relative paths from the parent of the .beads directory
	if !filepath.IsAbs(target) {
		projectRoot := filepath.Dir(beadsDir)
		target = filepath.Join(projectRoot, target)
	}

	// Verify the target exists
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return beadsDir
	}

	return target
}
