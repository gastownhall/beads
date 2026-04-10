//go:build linux

package main

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FS_NOCOW_FL is the "no copy-on-write" inode attribute. This is the flag
// that `chattr +C` sets. On btrfs it disables both copy-on-write AND
// transparent compression for the inode — new files created inside a
// directory with this flag inherit it automatically.
//
// We apply this to the .beads/ directory (and its dolt data subdirs) to
// avoid pathological kworker thrashing on dolt's hot append-only write
// path: with btrfs compression enabled, each small append forces a
// read-modify-write-recompress of the existing compressed extent. Setting
// NOCOW/+C disables both behaviors for new files and eliminates the
// thrashing.
//
// Note: the kernel also defines a separate `FS_NOCOMP_FL` (0x400) flag,
// but in practice FS_IOC_SETFLAGS returns EOPNOTSUPP for it on btrfs.
// FS_NOCOW_FL (0x00800000) is the flag the `chattr(1)` tool uses and the
// one btrfs actually honors at the inode level for this workload.
//
// On non-btrfs filesystems the ioctl may fail with ENOTTY/EOPNOTSUPP/
// EINVAL/EPERM. We suppress these so the optimization stays best-effort.
const fsNoCowFL = 0x00800000

// getInodeFlags wraps the FS_IOC_GETFLAGS ioctl. The kernel interface uses
// `long` (platform word size) — on amd64 that's 8 bytes — so we must pass
// an `int` (Go's platform-sized signed integer) pointer rather than int32.
// The IoctlSetPointerInt helper in golang.org/x/sys/unix narrows to int32
// which is incorrect on 64-bit platforms for this ioctl.
func getInodeFlags(fd int) (int, error) {
	var flags int
	// nolint:gosec // G103: unsafe.Pointer required for ioctl syscall argument
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(unix.FS_IOC_GETFLAGS),
		uintptr(unsafe.Pointer(&flags)),
	)
	if errno != 0 {
		return 0, errno
	}
	return flags, nil
}

// setInodeFlags wraps the FS_IOC_SETFLAGS ioctl. Same word-size caveat as
// getInodeFlags applies — we pass a pointer to a platform-sized int.
func setInodeFlags(fd int, flags int) error {
	// nolint:gosec // G103: unsafe.Pointer required for ioctl syscall argument
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		uintptr(fd),
		uintptr(unix.FS_IOC_SETFLAGS),
		uintptr(unsafe.Pointer(&flags)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// applyNoCOW attempts to set FS_NOCOW_FL on the directory at path.
//
// This is a best-effort optimization for btrfs. On non-btrfs filesystems
// we short-circuit to a no-op rather than attempting the ioctl at all,
// because (a) the flag is meaningless outside btrfs and (b) different
// filesystems return different errors (ENOTTY/EOPNOTSUPP/EINVAL/EPERM)
// and we'd rather not try to classify them all.
//
// Note: setting FS_NOCOW_FL on an already-populated directory only affects
// files created *after* the flag is set. Existing files retain their prior
// compression state and must be rewritten (e.g. via `mv` away and back) to
// pick up the new flag. The bd doctor check surfaces this caveat.
func applyNoCOW(path string) error {
	onBtrfs, err := isBtrfs(path)
	if err != nil || !onBtrfs {
		// Not btrfs (or statfs failed): nothing to do. We deliberately
		// don't propagate statfs errors because this is a best-effort
		// optimization — a failure here must not break `bd init`.
		return nil
	}

	// nolint:gosec // G304: path is the .beads/ dolt directory owned by the caller
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	flags, err := getInodeFlags(int(f.Fd()))
	if err != nil {
		return err
	}

	if flags&fsNoCowFL != 0 {
		return nil // Already set; no work to do.
	}

	return setInodeFlags(int(f.Fd()), flags|fsNoCowFL)
}

// hasNoCOW reports whether FS_NOCOW_FL is set on the directory at path.
//
// Returns (false, nil) when the filesystem is not btrfs, so callers can
// treat "not applicable" as "nothing to check" rather than an error.
func hasNoCOW(path string) (bool, error) {
	onBtrfs, err := isBtrfs(path)
	if err != nil || !onBtrfs {
		return false, nil
	}

	// nolint:gosec // G304: path is the .beads/ dolt directory owned by the caller
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	flags, err := getInodeFlags(int(f.Fd()))
	if err != nil {
		return false, err
	}
	return flags&fsNoCowFL != 0, nil
}

// isBtrfs reports whether the given path lives on a btrfs filesystem.
//
// Only on btrfs does FS_NOCOW_FL have a meaningful effect on performance;
// on other filesystems the flag is a no-op (or unsupported). We use this
// helper so the init path can skip doing work that would be pointless, and
// so doctor checks can report accurate guidance.
func isBtrfs(path string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false, err
	}
	// BTRFS_SUPER_MAGIC
	const btrfsMagic = 0x9123683e
	return st.Type == btrfsMagic, nil
}
