//go:build !windows && !linux

package config

import "os"

// os.Open may follow a path swapped to a symlink on these platforms. The
// caller's descriptor identity check prevents chmod unless it still refers to
// the directory inspected with Lstat.
func openBeadsDirHandle(path string) (beadsDirHandle, error) {
	//nolint:gosec // G304: path is the .beads directory fixBeadsDirPermissions just inspected with Lstat; its descriptor identity check gates the chmod.
	return os.Open(path)
}
