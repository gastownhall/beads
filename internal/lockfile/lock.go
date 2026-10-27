package lockfile

import (
	"errors"
	"fmt"
)

// ErrLocked is returned when a lock cannot be acquired because it is held by another process.
var ErrLocked = errProcessLocked

// ErrLockBusy is returned when a non-blocking lock cannot be acquired
// because another process holds a conflicting lock.
var ErrLockBusy = errors.New("lock busy: held by another process")

// ErrUnsupported matches (via errors.Is) a lock failure caused by the
// filesystem rather than by another holder: NFS without lockd, some SMB and
// FUSE mounts, and Windows' 9P redirector for \\wsl$ paths. Retrying or
// waiting cannot help.
var ErrUnsupported = errors.New("file locking is not supported on this filesystem")

// UnsupportedError is the error a lock helper returns when the filesystem
// cannot lock. It matches ErrUnsupported and unwraps to the underlying cause
// (the errno), so errors.Is on that errno keeps working.
type UnsupportedError struct {
	Op string
	// Path is the lock file whose filesystem cannot lock, set by a caller
	// that knows it (workspacegate sets its gate file); the lock helpers
	// leave it empty. Error does not print it.
	Path string
	Err  error
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%v (%s: %v)", ErrUnsupported, e.Op, e.Err)
}

// Is reports whether target is ErrUnsupported.
func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

// Unwrap returns the underlying cause.
func (e *UnsupportedError) Unwrap() error { return e.Err }

// IsLocked returns true if the error indicates a lock is held by another process.
func IsLocked(err error) bool {
	return errors.Is(err, errProcessLocked)
}
