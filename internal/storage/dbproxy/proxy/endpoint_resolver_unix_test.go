//go:build unix

package proxy

import "syscall"

// The errnos this platform's sockets report for a refused connect and for a
// connection the peer reset.
const (
	connRefused = syscall.ECONNREFUSED
	connReset   = syscall.ECONNRESET
)
