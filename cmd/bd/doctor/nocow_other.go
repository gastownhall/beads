//go:build !linux

package doctor

// applyNoCOW is a no-op on non-Linux platforms: FS_NOCOW_FL is a
// Linux-specific inode attribute.
func applyNoCOW(path string) error {
	return nil
}

// hasNoCOW always returns false on non-Linux because the flag does not
// exist there. Doctor checks short-circuit to StatusOK before calling this
// on non-Linux, but we keep the symbol defined for the build.
func hasNoCOW(path string) (bool, error) {
	return false, nil
}

// isBtrfs always returns false on non-Linux since btrfs is Linux-only.
func isBtrfs(path string) (bool, error) {
	return false, nil
}
