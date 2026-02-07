// Package utils provides utility functions for issue ID parsing and path handling.
package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// FindJSONLInDir finds the JSONL file in the given .beads directory.
// It prefers issues.jsonl over other .jsonl files to prevent accidentally
// reading/writing to deletions.jsonl or merge artifacts (bd-tqo fix).
// Always returns a path (defaults to issues.jsonl if nothing suitable found).
//
// Search order:
// 1. issues.jsonl (canonical name)
// 2. beads.jsonl (legacy support)
// 3. Any other .jsonl file except deletions/merge artifacts
// 4. Default to issues.jsonl
func FindJSONLInDir(dbDir string) string {
	pattern := filepath.Join(dbDir, "*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		// Default to issues.jsonl if glob fails or no matches
		return filepath.Join(dbDir, "issues.jsonl")
	}

	// Prefer issues.jsonl over other .jsonl files (bd-tqo fix)
	// This prevents accidentally using deletions.jsonl or merge artifacts
	for _, match := range matches {
		if filepath.Base(match) == "issues.jsonl" {
			return match
		}
	}

	// Fall back to beads.jsonl for legacy support
	for _, match := range matches {
		if filepath.Base(match) == "beads.jsonl" {
			return match
		}
	}

	// Last resort: use first match (but skip deletions.jsonl, interactions.jsonl, routes.jsonl, and merge artifacts)
	for _, match := range matches {
		base := filepath.Base(match)
		// Skip deletions manifest, interactions (audit trail), routes config, and merge artifacts
		if base == "deletions.jsonl" ||
			base == "interactions.jsonl" ||
			base == "routes.jsonl" ||
			base == "beads.base.jsonl" ||
			base == "beads.left.jsonl" ||
			base == "beads.right.jsonl" {
			continue
		}
		return match
	}

	// If only deletions/merge files exist, default to issues.jsonl
	return filepath.Join(dbDir, "issues.jsonl")
}

// findMoleculesJSONLInDir finds the molecules.jsonl file in the given .beads directory.
// Returns the path to molecules.jsonl if it exists, empty string otherwise.
// Molecules are template issues used for instantiation (beads-1ra).
func findMoleculesJSONLInDir(dbDir string) string {
	moleculesPath := filepath.Join(dbDir, "molecules.jsonl")
	// Check if file exists - we don't fall back to any other file
	// because molecules.jsonl is optional and specific
	if _, err := os.Stat(moleculesPath); err == nil {
		return moleculesPath
	}
	return ""
}

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
// On macOS, uses realpath(1) to get the canonical case.
// Returns empty string if resolution fails.
func resolveCanonicalCase(path string) string {
	if runtime.GOOS == "darwin" {
		// Use realpath to get canonical path with correct case
		// realpath on macOS returns the true filesystem case
		cmd := exec.Command("realpath", path)
		output, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(output))
		}
	}
	// Windows: filepath.EvalSymlinks already handles case on Windows
	// For other systems or if realpath fails, return empty to use fallback
	return ""
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
// filesystems and symlinks. This is the preferred way to compare workspace
// paths in the daemon registry and discovery code.
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
