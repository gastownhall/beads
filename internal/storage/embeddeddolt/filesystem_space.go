package embeddeddolt

import "syscall"

// FilesystemFullError reports that an embedded Dolt read-only open was
// stopped before connector initialization because the database filesystem had
// no space available to the current process.
type FilesystemFullError struct {
	Path string
}

func (e *FilesystemFullError) Error() string {
	return "embedded Dolt read-only open stopped: no filesystem space is available; free space and retry"
}

// Unwrap lets callers use errors.Is(err, syscall.ENOSPC).
func (e *FilesystemFullError) Unwrap() error {
	return syscall.ENOSPC
}

type availableSpaceProbe func(path string) (bool, error)
