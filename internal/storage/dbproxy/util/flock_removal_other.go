//go:build !windows

package util

import "os"

// TryLockForRemoval preserves ordinary TryLock behavior on non-Windows hosts.
func TryLockForRemoval(lockPath string) (*Lock, error) {
	return TryLock(lockPath)
}

// RemoveWhileHeld preserves ordinary unlink behavior without releasing the lock.
func (l *Lock) RemoveWhileHeld() error {
	return os.Remove(l.f.Name())
}
