//go:build windows

package proxy

import "golang.org/x/sys/windows"

// The errnos Windows sockets report for a refused connect and for a
// connection the peer reset. syscall's ECONNREFUSED and ECONNRESET are
// invented values there that no socket call returns.
const (
	connRefused = windows.WSAECONNREFUSED
	connReset   = windows.WSAECONNRESET
)
