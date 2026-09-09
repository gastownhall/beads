//go:build cgo && unix

package embeddeddolt

import "golang.org/x/sys/unix"

func filesystemHasAvailableSpace(path string) (bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return false, err
	}
	return stat.Bavail != 0, nil
}
