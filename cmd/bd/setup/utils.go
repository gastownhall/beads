package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/utils"
)

var errRefuseSymlinkWrite = errors.New("refusing to write through symlink")

// setupProjectDir is the directory bd setup's project-scoped files are
// written in, checked against and removed from: the -C target while a bd
// setup invocation binds it (SetProjectDir), and none otherwise, so the
// process working directory keeps deciding, as before -C gained a project
// meaning (GH#8283). Global-scope files stay on the user's home directory
// and CODEX_HOME whatever this holds. Process-global: a test that binds it
// must not run in parallel with one that reads it.
var setupProjectDir string

// SetProjectDir binds dir as the directory bd setup's project-scoped
// operations run in (workingProjectDir, ProjectPath), replacing any earlier
// binding, and returns the restore that puts the previous binding back. A
// bd setup invocation binds the -C target for its own duration and defers
// the restore, so the binding unwinds on success and error alike; empty
// unbinds, selecting the process working directory.
func SetProjectDir(dir string) (restore func()) {
	prev := setupProjectDir
	setupProjectDir = dir
	return func() { setupProjectDir = prev }
}

// workingProjectDir is the directory a project-scoped setup operation runs
// in: the bound -C target when one is bound, else the process working
// directory. It errors only on the unbound branch, when the working
// directory cannot be found.
func workingProjectDir() (string, error) {
	if setupProjectDir != "" {
		return setupProjectDir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	return wd, nil
}

// ProjectPath anchors the relative path rel in the directory bd setup's
// project-scoped operations run in: the bound -C target when one is bound
// (SetProjectDir). Unbound, rel is returned as is, so the process working
// directory decides and today's relative spelling — paths and output alike —
// is preserved exactly. An empty rel stays empty (it names no file), and an
// absolute rel is returned as is: it is already rooted, by the caller rather
// than by the project.
func ProjectPath(rel string) string {
	if rel == "" || filepath.IsAbs(rel) || setupProjectDir == "" {
		return rel
	}
	return filepath.Join(setupProjectDir, rel)
}

// atomicWriteFile writes data to a file atomically using a unique temporary file.
// This prevents race conditions when multiple processes write to the same file.
// If path is a symlink, the write is refused; callers that deliberately want
// write-through behavior should use atomicWriteFileFollowingSymlink.
// An optional permissions argument sets the file mode. Without one, a file that
// already exists keeps its permission bits and a new file gets 0644.
//
//nolint:unparam // perm is intentionally variadic for callers that need non-default permissions
func atomicWriteFile(path string, data []byte, perm ...os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", errRefuseSymlinkWrite, path)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect path: %w", err)
	}

	return atomicWriteFileFollowingSymlink(path, data, perm...)
}

// atomicWriteFileFollowingSymlink writes atomically to the resolved target.
// Keep this helper named at call sites so symlink write-through is explicit.
// The file mode is chosen as atomicWriteFile describes, from the target.
func atomicWriteFileFollowingSymlink(path string, data []byte, perm ...os.FileMode) error {
	targetPath, err := utils.ResolveForWrite(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	mode := os.FileMode(0644)
	if len(perm) > 0 {
		mode = perm[0]
	} else if info, err := os.Stat(targetPath); err == nil {
		// The rename below puts a new file in place of the old one. Give it
		// the old one's permission bits, or every rewrite resets a mode the
		// owner narrowed (a 0600 ~/.gemini/GEMINI.md holding Gemini CLI's
		// saved memories, say) to 0644.
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect path: %w", err)
	}

	dir := filepath.Dir(targetPath)

	// Create unique temp file in same directory
	tmpFile, err := os.CreateTemp(dir, ".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Write data
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()    // best effort cleanup
		_ = os.Remove(tmpPath) // best effort cleanup
		return fmt.Errorf("write temp file: %w", err)
	}

	// Close temp file
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath) // Best effort cleanup
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath) // Best effort cleanup
		return fmt.Errorf("set permissions: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, targetPath); err != nil {
		_ = os.Remove(tmpPath) // Best effort cleanup
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// DirExists checks if a directory exists
func DirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// FileExists checks if a file exists
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// EnsureDir creates a directory if it doesn't exist
func EnsureDir(path string, perm os.FileMode) error {
	if DirExists(path) {
		return nil
	}
	if err := os.MkdirAll(path, perm); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	return nil
}

// RemoveEmptyDir removes dir if it is a real directory that holds nothing.
// An integration's --remove calls it only for a directory it has just taken
// one of bd's files, or such a directory, out of, so that it removes no
// directory it took nothing from. It never removes a symlink to a directory,
// or on Windows a junction: os.Remove unlinks one whatever its target holds.
// A dir that is missing, is not a real directory, or holds anything is left
// as it is; the error is for one it could not read, or an empty one it could
// not remove.
func RemoveEmptyDir(dir string) error {
	return removeEmptyDir(dir, os.Remove)
}

// removeEmptyDir is RemoveEmptyDir with the call that removes the empty
// directory injected, for an integration whose env injects its removals.
func removeEmptyDir(dir string, remove func(string) error) error {
	// Lstat, not Stat: a link to a directory, or a junction, is no
	// directory. IsDir on its result, rather than a ModeSymlink test, also
	// keeps a junction, which Go reports as ModeIrregular.
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(dir) // #nosec G304 -- bd's own integration directories
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return nil
	}
	if err := remove(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
