package lockfile

import (
	"errors"
)

// ErrLocked is returned when a lock cannot be acquired because it is held by another process.
var ErrLocked = errProcessLocked

// ErrLockBusy is returned when a non-blocking lock cannot be acquired
// because another process holds a conflicting lock.
var ErrLockBusy = errors.New("lock busy: held by another process")

// IsLocked returns true if the error indicates a lock is held by another process.
func IsLocked(err error) bool {
	return err == errProcessLocked
}
