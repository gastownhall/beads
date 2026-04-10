//go:build !linux

package main

// applyNoCOW is a no-op on non-Linux platforms. FS_NOCOW_FL is a
// Linux-specific inode attribute and has no analog elsewhere — btrfs
// compression thrashing only affects Linux hosts running dolt on btrfs.
func applyNoCOW(path string) error {
	return nil
}

// hasNoCOW always reports false on non-Linux platforms because the flag
// does not exist there. Doctor checks treat "!linux" as "nothing to check".
func hasNoCOW(path string) (bool, error) {
	return false, nil
}

// isBtrfs always reports false on non-Linux. btrfs is a Linux-only
// filesystem, so we short-circuit the check on other platforms.
func isBtrfs(path string) (bool, error) {
	return false, nil
}
