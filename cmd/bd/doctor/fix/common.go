package fix

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/remedy"
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

// isStrictDescendant reports whether candidate lies strictly below root;
// both are absolute and clean.
func isStrictDescendant(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) &&
		!strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// resolveExistingParent resolves the symlinks in dir, resolving its nearest
// existing ancestor when dir does not exist yet.
// Adapted from #7889 by @Wenjix.
func resolveExistingParent(dir string) (string, error) {
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", err
		}
		resolved, err := resolveExistingParent(parent)
		if err != nil {
			return "", err
		}
		return filepath.Join(resolved, filepath.Base(dir)), nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("%s is not a directory", remedy.PrintedValue(dir))
	}
	return filepath.EvalSymlinks(dir)
}

// envSelectedDatabasePath is the database location BEADS_DOLT_DATA_DIR names
// for beadsDir, or "" when the variable is unset or empty.
func envSelectedDatabasePath(beadsDir string) (string, error) {
	dataDir := os.Getenv("BEADS_DOLT_DATA_DIR")
	if dataDir == "" {
		return "", nil
	}
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(beadsDir, dataDir)
	}
	return filepath.Abs(dataDir)
}

// confinedDatabasePath admits dbPath for a doctor fix that backs up or
// deletes it, returning it as an absolute path. The path must lie lexically
// strictly inside the workspace .beads directory (which may itself be a
// symlink) and may not be a symlink, and its existing ancestors must not
// resolve outside .beads. The one exception is the location BEADS_DOLT_DATA_DIR selects,
// which the user named explicitly.
// Adapted from #7889 by @Wenjix.
func confinedDatabasePath(beadsDir, dbPath string) (string, error) {
	absBeads, err := filepath.Abs(beadsDir)
	if err != nil {
		return "", fmt.Errorf("invalid beads directory: %w", err)
	}
	absDB, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("invalid database path: %w", err)
	}
	envPath, err := envSelectedDatabasePath(absBeads)
	if err != nil {
		return "", fmt.Errorf("invalid BEADS_DOLT_DATA_DIR: %w", err)
	}
	if envPath != "" && envPath == absDB {
		return absDB, nil
	}

	refuse := func(why string) error {
		return &databaseRefusal{path: absDB, why: why}
	}
	resolvedBeads, err := resolveExistingParent(absBeads)
	if err != nil {
		return "", fmt.Errorf("failed to resolve beads directory %s: %w", remedy.PrintedValue(absBeads), err)
	}
	// Metadata must name the database below .beads as spelled: a path that
	// reaches the store only through .beads' resolved target is refused.
	if !isStrictDescendant(absBeads, absDB) {
		return "", refuse("it is outside the workspace .beads directory " + remedy.PrintedValue(absBeads))
	}
	info, err := os.Lstat(absDB)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", refuse("it is a symlink")
	}
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to stat database path %s: %w", remedy.PrintedValue(absDB), err)
	}
	resolvedParent, err := resolveExistingParent(filepath.Dir(absDB))
	if err != nil {
		return "", fmt.Errorf("failed to resolve database parent directory: %w", err)
	}
	if resolvedParent != resolvedBeads && !isStrictDescendant(resolvedBeads, resolvedParent) {
		return "", refuse("its parent directory resolves outside .beads, to " + remedy.PrintedValue(resolvedParent))
	}
	if info != nil {
		resolvedDB, err := filepath.EvalSymlinks(absDB)
		if err != nil {
			return "", fmt.Errorf("failed to resolve database path %s: %w", remedy.PrintedValue(absDB), err)
		}
		if !isStrictDescendant(resolvedBeads, resolvedDB) {
			return "", refuse("it resolves outside .beads, to " + remedy.PrintedValue(resolvedDB))
		}
	}
	return absDB, nil
}

// databaseRefusal is confinedDatabasePath's refusal of a database location;
// why says what makes the location unsafe.
type databaseRefusal struct {
	path, why string
}

func (e *databaseRefusal) Error() string {
	return fmt.Sprintf("refusing to back up or delete the database at %s: %s\n"+
		"Recover it by hand, or set BEADS_DOLT_DATA_DIR to that location to select it explicitly",
		remedy.PrintedValue(e.path), e.why)
}

// resolveBeadsDir follows .beads/redirect files to find the actual beads directory.
// If no redirect exists, returns the original path unchanged.
// This is a wrapper around beads.FollowRedirect for use within the fix package.
func resolveBeadsDir(beadsDir string) string {
	return beads.FollowRedirect(beadsDir)
}
