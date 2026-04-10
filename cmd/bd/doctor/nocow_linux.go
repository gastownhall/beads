//go:build linux

package doctor

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FS_NOCOW_FL is the "no copy-on-write" inode attribute set by `chattr +C`.
// On btrfs it disables both copy-on-write and transparent compression for
// new files created under the directory. See cmd/bd/nocow_linux.go for the
// full rationale — doctor has its own copy because subpackages cannot
// import from the main package. Both copies must stay in sync.
const fsNoCowFL = 0x00800000

// getInodeFlags wraps FS_IOC_GETFLAGS. The ioctl uses a platform-sized
// long on the kernel side (8 bytes on amd64) so we must pass an int*, not
// an int32* — the unix.IoctlSetPointerInt helper truncates to int32 and
// would produce wrong results on 64-bit systems.
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

// setInodeFlags wraps FS_IOC_SETFLAGS with a platform-sized flags value.
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

// applyNoCOW attempts to set FS_NOCOW_FL on path. Best-effort: returns
// nil on non-btrfs filesystems so the caller can treat the call as a
// no-op. On btrfs the ioctl error (if any) is propagated.
func applyNoCOW(path string) error {
	onBtrfs, err := isBtrfs(path)
	if err != nil || !onBtrfs {
		return nil
	}

	// nolint:gosec // G304: path is the caller-provided .beads/ dolt directory
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
		return nil
	}

	return setInodeFlags(int(f.Fd()), flags|fsNoCowFL)
}

// hasNoCOW reports whether FS_NOCOW_FL is set on path. Returns (false, nil)
// on non-btrfs filesystems where the flag does not apply.
func hasNoCOW(path string) (bool, error) {
	onBtrfs, err := isBtrfs(path)
	if err != nil || !onBtrfs {
		return false, nil
	}

	// nolint:gosec // G304: path is the caller-provided .beads/ dolt directory
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

// isBtrfs reports whether path lives on a btrfs filesystem. Used to gate
// warnings so we only complain on filesystems where FS_NOCOW_FL actually
// matters for performance.
func isBtrfs(path string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return false, err
	}
	const btrfsMagic = 0x9123683e
	return st.Type == btrfsMagic, nil
}
