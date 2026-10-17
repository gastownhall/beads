//go:build windows

package proxy

import (
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWinsockCodesClassifyAsUnreachable pins the Windows half of the dial
// classification directly. The end-to-end tests cover a refusal through the
// proxy, but they say nothing about the other codes, and nothing about what
// must NOT be classified: a reset or a timeout has to keep the client's full
// transient retry rather than being reported as a proven outage.
func TestWinsockCodesClassifyAsUnreachable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		errno syscall.Errno
		want  bool
	}{
		{"WSAECONNREFUSED", windows.WSAECONNREFUSED, true},
		{"WSAEHOSTUNREACH", windows.WSAEHOSTUNREACH, true},
		{"WSAENETUNREACH", windows.WSAENETUNREACH, true},
		{"WSAECONNRESET", windows.WSAECONNRESET, false},
		{"WSAETIMEDOUT", windows.WSAETIMEDOUT, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUpstreamUnreachableDialError(tc.errno); got != tc.want {
				t.Errorf("isUpstreamUnreachableDialError(%s = %d) = %v, want %v",
					tc.name, uintptr(tc.errno), got, tc.want)
			}
		})
	}
}

// Winsock's own sentence about the target machine is not a reason to show a bd
// user, so the refusal must read as a refusal on Windows too.
func TestRefusalReasonReadsTheSameOnWindows(t *testing.T) {
	if got := dialFailureReason(windows.WSAECONNREFUSED); got != "connection refused" {
		t.Errorf("dialFailureReason(WSAECONNREFUSED) = %q, want %q", got, "connection refused")
	}
}
