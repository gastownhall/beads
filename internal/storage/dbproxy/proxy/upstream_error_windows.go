//go:build windows

package proxy

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// unreachableDialErrnos is the Windows half of the table described in
// upstream_error_unix.go.
//
// Windows reports these conditions with Winsock codes, and Go's syscall
// package exposes the POSIX names there as synthetic values instead:
// syscall.ECONNREFUSED is 0x20000016, while a refused connection arrives as
// WSAECONNREFUSED, 10061. Matching only the POSIX sentinels therefore
// classified a Windows refusal as something other than an unreachable
// upstream, and the proxy answered a bd client with a plain close instead of
// its ERR packet, which the MySQL driver reported as "invalid connection".
//
// windows.WSAECONNREFUSED is a syscall.Errno (x/sys aliases the type), so the
// codes go in the table unconverted.
var unreachableDialErrnos = []struct {
	errno  syscall.Errno
	reason string
}{
	{windows.WSAECONNREFUSED, "connection refused"},
	{syscall.ENOENT, "no such file or directory"},
	{windows.WSAEHOSTUNREACH, "no route to host"},
	{windows.WSAENETUNREACH, "network is unreachable"},
}
