//go:build !(aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris)

package atomicfile

// noFollowFlag is 0 where syscall has no O_NOFOLLOW; OpenNoFollow then relies
// on its Lstat check alone.
const noFollowFlag = 0
