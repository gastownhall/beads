//go:build unix

package atomicfile

import "syscall"

const noFollowFlag = syscall.O_NOFOLLOW
