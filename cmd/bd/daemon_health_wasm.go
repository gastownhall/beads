//go:build wasm

package main

// checkDiskSpace returns the available disk space in MB for the given path.
// Returns (availableMB, true) on success, (0, false) on failure.
// WASM builds don't support disk space checks.
func checkDiskSpace(path string) (uint64, bool) {
	return 0, false
}
