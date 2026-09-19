//go:build darwin

package utils

import (
	"bytes"
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// errIrregularFile reports a path the fast query declines to open. It is
// deliberately NOT a not-exist error: the caller must fall back to the
// portable walk, which can case-resolve such a path without opening it.
var errIrregularFile = errors.New("canonical-case fast path: not a directory or regular file")

// canonicalCaseFast returns the true on-disk path of an existing file or
// directory by asking the kernel, via darwin's F_GETPATH fcntl, for the path
// the vnode was created from. APFS/HFS+ store the name in its authored case
// and match lookups case-insensitively, so this recovers the real case in a
// single syscall regardless of how the caller spelled it — and, unlike a
// directory walk, its cost does not depend on how many entries the ancestor
// directories hold.
//
// The returned path is also symlink- and firmlink-resolved by the kernel, so
// it agrees with the filepath.EvalSymlinks + component-walk pair it replaces.
//
// Errors are returned for the caller to classify: a not-exist error is a real
// verdict (the walk would agree), anything else means "could not resolve this
// way" and the caller should fall back to the portable walk.
func canonicalCaseFast(path string) (string, error) {
	// Only directories and regular files are safe to open purely to interrogate
	// them: opening a FIFO blocks until a writer appears and opening a device
	// node can have side effects, neither of which the directory walk this
	// replaces would ever do. Anything else falls back to the walk.
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsDir() && !fi.Mode().IsRegular() {
		return "", errIrregularFile
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(fd) }()

	buf := make([]byte, unix.PathMax)
	// F_GETPATH has no typed wrapper in x/sys/unix: it writes a NUL-terminated
	// path of up to MAXPATHLEN bytes into the buffer named by the third arg.
	_, _, errno := unix.Syscall( // nolint:gosec // buf is a live []byte of PathMax bytes, the exact contract F_GETPATH requires
		unix.SYS_FCNTL,
		uintptr(fd),
		uintptr(unix.F_GETPATH),
		uintptr(unsafe.Pointer(&buf[0])),
	)
	if errno != 0 {
		return "", errno
	}

	n := bytes.IndexByte(buf, 0)
	if n <= 0 {
		return "", unix.EINVAL
	}
	return string(buf[:n]), nil
}
