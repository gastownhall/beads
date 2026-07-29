//go:build linux

package config

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openBeadsDirHandle(path string) (beadsDirHandle, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err == nil {
		return os.NewFile(uintptr(fd), path), nil
	}
	if !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EPERM) {
		return nil, err
	}

	// O_PATH needs no read permission. Chmod uses fchmodat2 with AT_EMPTY_PATH,
	// so the operation remains anchored to this descriptor rather than the path.
	return openBeadsDirPathHandle(path)
}

func openBeadsDirPathHandle(path string) (beadsDirHandle, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	return &linuxPathDirHandle{File: os.NewFile(uintptr(fd), path)}, nil
}

type linuxPathDirHandle struct {
	*os.File
}

func (h *linuxPathDirHandle) Chmod(mode os.FileMode) error {
	return unix.Fchmodat(int(h.Fd()), "", uint32(mode.Perm()), unix.AT_EMPTY_PATH)
}
