//go:build !linux && !darwin && !windows

package procid

import (
	"fmt"
	"os"
	"runtime"
)

// ErrUnsupported marks platforms with no process-birth identity
// implementation (no /proc, no pidfd, no SysctlKinfoProc in x/sys). The
// dbproxy machinery that needs birth identity refuses cleanly at startup
// there instead of running with an unverifiable PID: reattach-and-signal
// without birth identity is exactly the PID-reuse race this package exists
// to close. Everything outside dbproxy is independent of procid, so ordinary
// bd use on these platforms is unaffected — which matches what they had
// before dbproxy existed (v1.1.2 shipped FreeBSD with no dbproxy at all).
var ErrUnsupported = fmt.Errorf("procid: process-birth identity is not implemented on %s", runtime.GOOS)

// Handle exists so cross-platform callers type-check; no instance can be
// constructed because Open always fails.
type Handle struct{}

func Capture(pid int) (Token, error) { return "", ErrUnsupported }

func Verify(pid int, tok Token) (bool, error) { return false, ErrUnsupported }

func Open(pid int, tok Token) (*Handle, error) { return nil, ErrUnsupported }

func (h *Handle) Signal(sig os.Signal) error { return ErrUnsupported }

func (h *Handle) Kill() error { return ErrUnsupported }

func (h *Handle) Close() error { return nil }

// IsProcessGone reports false: ErrUnsupported is a capability statement, not
// evidence about any process, and this platform can produce no other error.
func IsProcessGone(err error) bool { return false }
