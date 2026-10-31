//go:build unix

package proxy

import "syscall"

// unreachableDialErrnos are the dial failures that mean nothing is accepting
// connections at the upstream right now, each paired with the wording the
// client's message uses for it. The Windows table (upstream_error_windows.go)
// pairs the Winsock codes with the same wording, so one dial failure reads the
// same on both platforms.
//
// A timeout or a reset is deliberately absent. The upstream may be slow rather
// than gone, and the client's full transient retry is the right answer there
// (see the comment above IsUpstreamOutageError).
var unreachableDialErrnos = []struct {
	errno  syscall.Errno
	reason string
}{
	{syscall.ECONNREFUSED, "connection refused"},
	{syscall.ENOENT, "no such file or directory"},
	{syscall.EHOSTUNREACH, "no route to host"},
	{syscall.ENETUNREACH, "network is unreachable"},
}
