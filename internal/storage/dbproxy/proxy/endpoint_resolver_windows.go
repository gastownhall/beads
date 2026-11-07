//go:build windows

package proxy

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isConnectGone reports whether a failed connect says nothing serves the
// address any more: it was refused, or reset or aborted before it completed.
//
// Windows sockets report these as WSA errnos. syscall.ECONNREFUSED and its
// siblings are invented values there that no socket call returns, and
// syscall.Errno.Is maps none of them onto the WSA codes, so the unix checks
// would never match (net splits its own isConnError the same way, in
// net/error_windows.go).
func isConnectGone(err error) bool {
	return errors.Is(err, windows.WSAECONNREFUSED) ||
		errors.Is(err, windows.WSAECONNRESET) ||
		errors.Is(err, windows.WSAECONNABORTED)
}

// isConnLost reports whether a read or write on an established connection
// failed because the peer reset or closed it. Windows has no EPIPE for
// sockets: a write after the peer reset the connection fails with
// WSAECONNRESET or WSAECONNABORTED, and one on a socket already shut down
// for sending with WSAESHUTDOWN.
func isConnLost(err error) bool {
	return errors.Is(err, windows.WSAECONNRESET) ||
		errors.Is(err, windows.WSAECONNABORTED) ||
		errors.Is(err, windows.WSAESHUTDOWN)
}
