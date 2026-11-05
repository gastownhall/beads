package lockfile

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// errProbeBusy is the cause recorded when a private probe file also reports
// busy. It deliberately does not wrap ErrLockBusy: the result means "this
// filesystem cannot lock", and must not match IsLocked or ErrLockBusy.
var errProbeBusy = errors.New("the lock reports busy even on a private file")

// ProbeBusy tells a real holder from a filesystem whose locks cannot work.
// Call it only after try reported busy on a shared lock file: it creates a
// private file beside it (probePrefix + ".probe-<pid>-<rand>"), which no
// other process can know and so cannot be contended, runs the same try on
// it, then unlocks and removes it.
//
// It returns an error matching ErrUnsupported when the private file also
// reports busy or fails with an unsupported errno, and nil when the private
// file locks (the original busy result stands) or when the probe cannot be
// run or gives any other answer: unverifiable keeps the caller's behavior.
func ProbeBusy(probePrefix string, try func(*os.File) error) error {
	var f *os.File
	var path string
	// A collision with another probe's name is a vanishingly rare accident;
	// retry once with a fresh suffix rather than give up on the probe.
	for attempt := 0; attempt < 2 && f == nil; attempt++ {
		path = fmt.Sprintf("%s.probe-%d-%s", probePrefix, os.Getpid(), rand.Text())
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600) //nolint:gosec // G304: path derives from the caller's lock path plus a random suffix; O_EXCL refuses a pre-planted file or symlink
		if err != nil {
			f = nil
			if !errors.Is(err, fs.ErrExist) {
				return nil
			}
		}
	}
	if f == nil {
		return nil
	}
	defer func() {
		_ = FlockUnlock(f)
		_ = f.Close()
		_ = os.Remove(path)
	}()

	err := classifyLockErr(try(f))
	var unsupported *UnsupportedError
	switch {
	case errors.Is(err, ErrLockBusy) || IsLocked(err):
		return &UnsupportedError{Op: "probe", Err: errProbeBusy}
	case errors.As(err, &unsupported):
		return &UnsupportedError{Op: "probe", Err: unsupported.Err}
	}
	return nil
}
