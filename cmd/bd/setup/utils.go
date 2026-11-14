package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/utils"
)

var errRefuseSymlinkWrite = errors.New("refusing to write through symlink")

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

// boundProjectDir is the project directory bd setup installs, checks and
// removes a project's integration files in when it is not the process's
// working directory: the absolute directory bd -C names (SetProjectDir). ""
// means the working directory, and every project path stays as it was.
var boundProjectDir string

// SetProjectDir makes the project-scope integration files of bd setup's
// recipes resolve under dir, an absolute path, instead of the working
// directory, until the returned restore runs. bd -C <dir> setup uses it: -C
// selects the project without changing the working directory, so a recipe
// that wrote relative to it would write into the directory bd was launched
// in (#8283). "" restores the working directory. It is process-global: a
// test that calls it must not run in parallel with one that runs a recipe.
func SetProjectDir(dir string) (restore func()) {
	prev := boundProjectDir
	boundProjectDir = dir
	return func() { boundProjectDir = prev }
}

// workingProjectDir is the absolute project directory a recipe's default
// environment installs into: the bound one (SetProjectDir), else the working
// directory.
func workingProjectDir() (string, error) {
	if boundProjectDir != "" {
		return boundProjectDir, nil
	}
	return os.Getwd()
}

// ProjectPath returns rel, a project-relative path, under the bound project
// directory (SetProjectDir). It returns rel as it is when no directory is
// bound, so output without bd -C is unchanged, and when rel is empty, which
// names no file, or absolute, which is already rooted.
func ProjectPath(rel string) string {
	if boundProjectDir == "" || rel == "" || filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(boundProjectDir, rel)
}
