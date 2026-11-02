//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package atomicfile

import "syscall"

// noFollowFlag makes the kernel refuse a symbolic link at the final path
// component when OpenNoFollow opens the file.
const noFollowFlag = syscall.O_NOFOLLOW
