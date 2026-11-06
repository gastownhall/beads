package util

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/lockfile"
)

// TryRetirableLock is TryLock for lifecycle leases whose lock file must be
// removed while the lease is still held: the reverse proxied→server
// migration retires its control files before releasing them (GH#6884). On
// Windows the file is opened with delete sharing so Retire can remove the
// pathname without dropping the lease; on other platforms acquisition is
// identical to TryLock, where removing a held lock file is already
// possible. Contention is reported exactly like TryLock: an error that
// satisfies lockfile.IsLocked.
func TryRetirableLock(lockPath string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		return nil, fmt.Errorf("util: creating lock directory: %w", err)
	}
	f, err := openRetirable(lockPath)
	if err != nil {
		return nil, fmt.Errorf("util: opening lock file: %w", err)
	}
	if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{f: f, retirable: true}, nil
}

// Retire removes the lock file of a retirable lease while the lease stays
// held, so no new acquisition can take the pathname until Unlock releases
// the lease. On Windows this goes through a short-lived deletion handle
// (see retireFile); elsewhere it is a plain removal of the path, which
// never conflicts with a held flock. Retiring an ordinary or released lease
// is an error; retiring twice succeeds as a no-op, and so does retiring a
// path that is already gone.
func (l *Lock) Retire() error {
	switch {
	case l == nil || l.f == nil:
		return errors.New("util: Retire on an unusable lease")
	case l.released:
		return errors.New("util: Retire on a released lease")
	case !l.retirable:
		return errors.New("util: Retire on a lease not acquired as retirable")
	case l.retired:
		return nil
	}
	if err := l.retireFile(); err != nil {
		return err
	}
	l.retired = true
	return nil
}
