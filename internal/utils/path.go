// Package utils provides utility functions for issue ID parsing and path handling.
package utils

import (
	"path/filepath"
)

// CanonicalizePath converts a path to its canonical form by:
// 1. Converting to absolute path
// 2. Resolving symlinks
//
// If either step fails, it falls back to the best available form:
// - If symlink resolution fails, returns absolute path
// - If absolute path conversion fails, returns original path
//
// This function is used to ensure consistent path handling across the codebase,
// particularly for BEADS_DIR environment variable processing.
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

	return canonical
}
