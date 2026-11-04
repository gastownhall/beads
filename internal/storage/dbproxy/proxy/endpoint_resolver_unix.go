//go:build unix

package proxy

import (
	"errors"
	"syscall"
)

// isConnectGone reports whether a failed connect says nothing serves the
// address any more: it was refused, or reset or aborted before it completed.
// Windows has its own errnos for these (endpoint_resolver_windows.go).
func isConnectGone(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED)
}

// isConnLost reports whether a read or write on an established connection
// failed because the peer reset or closed it.
func isConnLost(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}
