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

// resolveBeadsDir follows .beads/redirect files to find the actual beads directory.
// If no redirect exists, returns the original path unchanged.
// This is a wrapper around beads.FollowRedirect for use within the fix package.
func resolveBeadsDir(beadsDir string) string {
	return beads.FollowRedirect(beadsDir)
}

// resolveExistingParent resolves dir to a symlink-free location when dir
// itself exists, and otherwise walks up to the nearest existing ancestor,
// returning that ancestor with dir's remaining components joined back on. A
// missing database parent is thereby compared by the directory that does
// exist, so a confinement cannot be satisfied by a symlinked ancestor below
// the last existing one. Adapted from PR #7889.
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
		return "", fmt.Errorf("not a directory: %s", dir)
	}
	return filepath.EvalSymlinks(dir)
}

// isStrictDescendant reports whether rel — the path of a candidate relative
// to its root — names an entry strictly below the root: not the root itself,
// a parent, or an absolute path (Rel's error stand-in).
func isStrictDescendant(rel string) bool {
	return rel != "." && rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(os.PathSeparator)) &&
		!filepath.IsAbs(rel)
}

// envSelectedDataDir returns the data directory BEADS_DOLT_DATA_DIR selects
// for beadsDir, resolved exactly like Config.GetDoltDataDir plus
// Config.DatabasePath: an absolute value is kept, a relative one is joined
// onto beadsDir, and the result is normalized with the same Abs/Clean
// handling the selected database path gets. ok is false when the variable is
// empty — an empty override selects nothing.
func envSelectedDataDir(beadsDir string) (dir string, ok bool) {
	raw := os.Getenv("BEADS_DOLT_DATA_DIR")
	if raw == "" {
		return "", false
	}
	selected := raw
	if !filepath.IsAbs(selected) {
		selected = filepath.Join(beadsDir, selected)
	}
	abs, err := filepath.Abs(selected)
	if err != nil {
		return "", false
	}
	return abs, true
}

// confinedDatabasePath validates that the database path a destructive doctor
// fix selected from workspace metadata stays inside the effective workspace
// .beads directory, and returns the absolute selected path when it does.
// getDatabasePath accepts relative or absolute dolt_data_dir values and an
// absolute database (Config.Save strips only the first), so unvalidated
// metadata can point a "repair" at arbitrary data: lexical traversal, a
// symlinked database leaf (a backup must represent the database entry
// itself, so a link is refused even when its target is inside the workspace
// or dangling), and a symlinked ancestor are all refused before any rename,
// removal or deletion happens. Two selections stay trusted: the workspace
// root may itself be a symlink (a supported alias — resolved, not refused),
// and an actual explicitly env-selected data directory is process input the
// operator named, exempted only when the selected path is exactly that
// path. Adapted from PR #7889; its blanket refusal of a symlinked workspace
// root is inverted to match this repository's supported layouts.
func confinedDatabasePath(beadsDir, dbPath string) (string, error) {
	absBeads, err := filepath.Abs(beadsDir)
	if err != nil {
		return "", fmt.Errorf("invalid beads directory: %w", err)
	}
	// Resolve the workspace root through its own symlink so confinement
	// compares real locations, not spellings.
	resolvedBeads, err := resolveExistingParent(absBeads)
	if err != nil {
		return "", fmt.Errorf("failed to resolve beads directory: %w", err)
	}
	absDB, err := filepath.Abs(dbPath)
	if err != nil {
		return "", fmt.Errorf("invalid database path: %w", err)
	}
	if envDir, ok := envSelectedDataDir(absBeads); ok && envDir == absDB {
		return absDB, nil
	}
	relLex, err := filepath.Rel(absBeads, absDB)
	if err != nil || !isStrictDescendant(relLex) {
		return "", fmt.Errorf("refusing database path outside the workspace .beads directory: %s", remedy.PrintedValue(dbPath))
	}
	if info, lerr := os.Lstat(absDB); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlinked database path: %s", remedy.PrintedValue(dbPath))
	}
	resolvedParent, err := resolveExistingParent(filepath.Dir(absDB))
	if err != nil {
		return "", fmt.Errorf("failed to resolve database parent directory: %w", err)
	}
	relParent, err := filepath.Rel(resolvedBeads, resolvedParent)
	if err != nil || (relParent != "." && !isStrictDescendant(relParent)) {
		return "", fmt.Errorf("refusing database path whose parent resolves outside the workspace .beads directory: %s", remedy.PrintedValue(dbPath))
	}
	if resolvedDB, rerr := filepath.EvalSymlinks(absDB); rerr == nil {
		relRes, relErr := filepath.Rel(resolvedBeads, resolvedDB)
		if relErr != nil || !isStrictDescendant(relRes) {
			return "", fmt.Errorf("refusing database path resolving outside the workspace .beads directory: %s", remedy.PrintedValue(dbPath))
		}
	} else if !os.IsNotExist(rerr) {
		return "", fmt.Errorf("failed to resolve database path: %w", rerr)
	}
	return absDB, nil
}
