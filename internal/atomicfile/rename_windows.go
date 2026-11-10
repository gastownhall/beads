//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// renameWait bounds how long rename waits for other handles on the target to
// close. A reader holds these files for one read, so a replace that collides
// with one waits milliseconds; a handle still open after renameWait fails the
// replace, and Close leaves the target untouched.
const renameWait = 2 * time.Second

// rename moves oldpath over newpath. MoveFileEx refuses to replace a file that
// another handle holds open without FILE_SHARE_DELETE, which os.Open never
// asks for, so a reader that has the target open fails the rename with
// ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION until it closes the file.
// Those two errors are retried with backoff until renameWait runs out, as the
// Go toolchain's cmd/internal/robustio does; any other error returns at once.
// A target that stays unreplaceable for another reason (read-only, a
// directory) fails the same way, only renameWait later.
func rename(oldpath, newpath string) error {
	deadline := time.Now().Add(renameWait)
	backoff := time.Millisecond
	for {
		err := os.Rename(oldpath, newpath)
		if err == nil || !mayBeHeldOpen(err) || time.Now().Add(backoff).After(deadline) {
			return err
		}
		time.Sleep(backoff)
		backoff = min(2*backoff, 100*time.Millisecond)
	}
}

func mayBeHeldOpen(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
