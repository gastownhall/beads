package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
)

// ErrTestBinary is returned when getBdBinary detects it's running as a test binary.
// This prevents fork bombs when tests call functions that execute bd subcommands.
var ErrTestBinary = fmt.Errorf("running as test binary - cannot execute bd subcommands")

type workspaceBeadsDirs struct {
	local    string
	resolved string
}

func newBdCmd(bdBinary string, args ...string) *exec.Cmd {
	cmd := exec.Command(bdBinary, args...) // #nosec G204 -- bdBinary from validated executable path
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
		// when tests call functions that execute bd subcommands.
		if isTestBinary(exe) {
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

// isTestBinary reports whether exe is a Go test binary. testing.Testing() is
// the authoritative signal: both `go test` and rules_go link test binaries with
// it set, whatever the file is called (Bazel names them <target>_test, not
// <pkg>.test). The name check is kept for binaries built by other means.
func isTestBinary(exe string) bool {
	if testing.Testing() {
		return true
	}
	baseName := filepath.Base(exe)
	return strings.HasSuffix(baseName, ".test") || strings.Contains(baseName, ".test.")
}

// validateBeadsWorkspace ensures the path is a valid beads workspace before
// attempting any fix operations. This prevents path traversal attacks.
func validateBeadsWorkspace(path string) error {
	_, err := resolveWorkspaceBeadsDirs(path)
	return err
}

func resolveWorkspaceBeadsDirs(path string) (workspaceBeadsDirs, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return workspaceBeadsDirs{}, fmt.Errorf("invalid path: %w", err)
	}

	dirs := workspaceBeadsDirs{
		local:    filepath.Join(absPath, ".beads"),
		resolved: beads.ResolveBeadsDirForRepo(absPath),
	}

	if info, err := os.Stat(dirs.resolved); err != nil || !info.IsDir() {
		return workspaceBeadsDirs{}, fmt.Errorf("not a beads workspace: .beads directory not found for %s", absPath)
	}

	return dirs, nil
}

func resolvedWorkspaceBeadsDir(path string) (string, error) {
	dirs, err := resolveWorkspaceBeadsDirs(path)
	if err != nil {
		return "", err
	}

	return dirs.resolved, nil
}

func localWorkspaceBeadsDir(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}
	return filepath.Join(absPath, ".beads"), nil
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

func resolveExistingParent(dir string) (string, error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		parent, err := resolveExistingParent(filepath.Dir(dir))
		if err != nil {
			return "", err
		}
		return filepath.Join(parent, filepath.Base(dir)), nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("parent is not a directory: %s", dir)
	}
	return filepath.EvalSymlinks(dir)
}

func confinedDatabasePath(beadsDir, dbPath string) (string, error) {
	absBeads, err := filepath.Abs(beadsDir)
	if err != nil {
		return "", fmt.Errorf("invalid beads directory: %w", err)
	}
	if info, lerr := os.Lstat(absBeads); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlinked beads directory: %s", beadsDir)
	}
	resolvedBeads, err := resolveExistingParent(absBeads)
	if err != nil {
		return "", fmt.Errorf("failed to resolve beads directory: %w", err)
	}
	absDB, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("invalid database path: %w", err)
	}
	sep := string(filepath.Separator)
	relLex, err := filepath.Rel(absBeads, absDB)
	if err != nil || relLex == "." || relLex == ".." || strings.HasPrefix(relLex, ".."+sep) || filepath.IsAbs(relLex) {
		return "", fmt.Errorf("refusing database path outside workspace .beads directory: %s", dbPath)
	}
	if info, lerr := os.Lstat(absDB); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlinked database path: %s", dbPath)
	}
	resolvedParent, err := resolveExistingParent(filepath.Dir(absDB))
	if err != nil {
		return "", fmt.Errorf("failed to resolve database parent directory: %w", err)
	}
	relParent, err := filepath.Rel(resolvedBeads, resolvedParent)
	if err != nil || (relParent != "." && (relParent == ".." || strings.HasPrefix(relParent, ".."+sep) || filepath.IsAbs(relParent))) {
		return "", fmt.Errorf("refusing database path whose parent resolves outside .beads directory: %s", dbPath)
	}
	if resolvedDB, rerr := filepath.EvalSymlinks(absDB); rerr == nil {
		relRes, err := filepath.Rel(resolvedBeads, resolvedDB)
		if err != nil || relRes == "." || relRes == ".." || strings.HasPrefix(relRes, ".."+sep) || filepath.IsAbs(relRes) {
			return "", fmt.Errorf("refusing database path resolving outside .beads directory: %s", dbPath)
		}
	} else if !os.IsNotExist(rerr) {
		return "", rerr
	}
	return absDB, nil
}

// resolveBeadsDir follows .beads/redirect files to find the actual beads directory.
// If no redirect exists, returns the original path unchanged.
// This is a wrapper around beads.FollowRedirect for use within the fix package.
func resolveBeadsDir(beadsDir string) string {
	return beads.FollowRedirect(beadsDir)
}
