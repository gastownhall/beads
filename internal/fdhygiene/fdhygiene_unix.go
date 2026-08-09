//go:build unix

package fdhygiene

import (
	"os"
	"sort"
	"strconv"

	"golang.org/x/sys/unix"
)

// fdDirCandidates are the per-process fd directories, in preference order.
// Linux exposes /proc/self/fd; darwin exposes /dev/fd (on Linux /dev/fd is a
// symlink to the same place, so listing it twice is harmless). BSDs without
// fdescfs mounted list only 0-2 under /dev/fd, which would make the scan a
// silent no-op there — acceptable, since bd's supported platforms are
// linux/darwin/windows.
var fdDirCandidates = []string{"/proc/self/fd", "/dev/fd"}

// maxScanFD bounds the brute-force fallback used when no fd directory is
// readable. RLIMIT_NOFILE is commonly 1<<20 or higher, and probing every slot
// would cost more than the leak it prevents; descriptors inherited from a
// caller in practice sit in the low hundreds.
const maxScanFD = 4096

func markInheritedCloexec() []int {
	var marked []int
	for fd := range openFDs() {
		if fd <= 2 {
			// Leaving stdio alone is deliberate: os/exec rewires 0/1/2 for the
			// child from Cmd.Stdin/Stdout/Stderr, and marking bd's own stdio
			// CLOEXEC would affect every other exec bd makes.
			continue
		}
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, flags|unix.FD_CLOEXEC); err != nil {
			continue
		}
		marked = append(marked, fd)
	}
	sort.Ints(marked)
	return marked
}

// openFDs returns the set of descriptors that appear open in this process.
// The set may include the descriptor used to read the fd directory itself;
// that one is Go-opened and therefore already CLOEXEC, so the caller skips it.
func openFDs() map[int]struct{} {
	for _, dir := range fdDirCandidates {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		fds := make(map[int]struct{}, len(entries))
		for _, e := range entries {
			if fd, err := strconv.Atoi(e.Name()); err == nil {
				fds[fd] = struct{}{}
			}
		}
		return fds
	}

	// No fd directory (a minimal container without /proc, say). Probe the
	// bounded low range instead; F_GETFD on a closed slot just returns EBADF
	// and the caller skips it.
	limit := maxScanFD
	var rlim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &rlim); err == nil && rlim.Cur > 0 && int(rlim.Cur) < limit {
		limit = int(rlim.Cur)
	}
	fds := make(map[int]struct{}, limit)
	for fd := 0; fd < limit; fd++ {
		fds[fd] = struct{}{}
	}
	return fds
}
