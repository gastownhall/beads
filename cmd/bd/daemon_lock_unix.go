//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// flockExclusive acquires an exclusive non-blocking lock on the file
func flockExclusive(f *os.File) error {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err == unix.EWOULDBLOCK {
		return ErrDaemonLocked
	}
	return err
}
