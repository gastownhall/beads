//go:build !windows

package atomicfile

import "os"

// rename moves oldpath over newpath. rename(2) replaces the name even while
// other processes hold the old file open; they go on reading the old content.
func rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
