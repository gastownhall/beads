// Package utils provides utility functions for issue ID parsing and path handling.
package utils

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

// ResolveForWrite returns the path to write to, resolving symlinks.
// If path is a symlink, returns the resolved target path.
// If path doesn't exist, returns path unchanged (new file).
func ResolveForWrite(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return path, nil
		}
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return filepath.EvalSymlinks(path)
	}
	return path, nil
}

// CanonicalizePath converts a path to its canonical form by:
// 1. Converting to absolute path
// 2. Resolving symlinks
// 3. On macOS/Windows, resolving the true filesystem case (GH#880)
//
// If any step fails, it falls back to the best available form:
// - If case resolution fails, returns symlink-resolved path
// - If symlink resolution fails, returns absolute path
// - If absolute path conversion fails, returns original path
//
// This function is used to ensure consistent path handling across the codebase,
// particularly for BEADS_DIR environment variable processing and git worktree
// paths which require exact case matching.
func CanonicalizePath(path string) string {
	// Try to get absolute path
	absPath, err := filepath.Abs(path)
	if err != nil {
		// If we can't get absolute path, return original
		return path
	}

	// Try to resolve symlinks
	canonical, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// If we can't resolve symlinks, return absolute path
		return absPath
	}

	// On case-insensitive filesystems, resolve to true filesystem case (GH#880)
	// This is critical for git operations which string-compare paths exactly.
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if resolved := resolveCanonicalCase(canonical); resolved != "" {
			return resolved
		}
	}

	return canonical
}

// resolveCanonicalCase resolves a path to its true filesystem case.
// On macOS, prefers a single kernel query (F_GETPATH) and falls back to
// walking each path component and matching against actual directory entries
// to recover the correct case (HFS+/APFS are case-insensitive).
// Returns empty string if resolution fails.
func resolveCanonicalCase(path string) string {
	if runtime.GOOS != "darwin" {
		// Windows: filepath.EvalSymlinks already handles case
		return ""
	}

	// Fast path: ask the kernel for the vnode's true path. This is O(1) in the
	// size of the ancestor directories; the component walk below is O(entries)
	// in EVERY ancestor, which collapses on a machine whose $TMPDIR holds tens
	// of thousands of leftover test directories — CanonicalizePath is called
	// once per FindBeadsDir, i.e. on the hot path of every audit Append and
	// every store open, so the walk turned whole test packages into apparent
	// hangs (wy-9ai3u: 640ms/call at 97k $TMPDIR entries, 8000 calls).
	switch resolved, err := canonicalCaseFast(path); {
	case err == nil:
		return resolved
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		// A component does not exist: the walk below would reach the same
		// "not found" verdict, so skip it and let the caller fall back.
		return ""
	}

	return resolveCanonicalCaseWalk(path)
}

// resolveCanonicalCaseWalk is the portable fallback for resolveCanonicalCase:
// it walks the path component-by-component, listing each parent directory and
// matching case-insensitively. Correct everywhere, but O(entries) per ancestor
// directory — see the fast path above for why that matters.
func resolveCanonicalCaseWalk(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	if len(parts) == 0 {
		return ""
	}

	// Start from root
	resolved := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}

		entries, err := os.ReadDir(resolved)
		if err != nil {
			return "" // can't read directory, fall back
		}

		found := false
		for _, entry := range entries {
			if strings.EqualFold(entry.Name(), part) {
				resolved = filepath.Join(resolved, entry.Name())
				found = true
				break
			}
		}
		if !found {
			return "" // component not found, fall back
		}
	}

	return resolved
}

// NormalizePathForComparison returns a normalized path suitable for comparison.
// It resolves symlinks and handles case-insensitive filesystems (macOS, Windows).
//
// On case-insensitive filesystems (darwin, windows), the path is lowercased
// to ensure that /Users/foo/Desktop and /Users/foo/desktop compare as equal.
//
// This function should be used whenever comparing workspace paths, not for
// storing or displaying paths (preserve original case for those purposes).
func NormalizePathForComparison(path string) string {
	if path == "" {
		return ""
	}

	// Try to get absolute path first
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	// Try to resolve symlinks
	canonical, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		// If symlink resolution fails (e.g., path doesn't exist), use absolute path
		canonical = absPath
	}

	// On case-insensitive filesystems, lowercase for comparison
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}

	return canonical
}

// PathsEqual compares two paths for equality, handling case-insensitive
// filesystems and symlinks.
func PathsEqual(path1, path2 string) bool {
	return NormalizePathForComparison(path1) == NormalizePathForComparison(path2)
}

// CanonicalizeIfRelative ensures a path is absolute for filepath.Rel() compatibility.
// If the path is non-empty and relative, it is canonicalized using CanonicalizePath.
// Absolute paths and empty strings are returned unchanged.
//
// This guards against code paths that might set paths to relative values,
// which would cause filepath.Rel() to fail or produce incorrect results.
//
// See GH#959 for root cause analysis of the original autoflush bug.
func CanonicalizeIfRelative(path string) string {
	if path != "" && !filepath.IsAbs(path) {
		return CanonicalizePath(path)
	}
	return path
}
