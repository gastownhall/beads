//go:build linux

package doltserver

import (
	"os"
	"syscall"
)

// procAttrDetached returns SysProcAttr to detach a child process from the
// parent process group so it survives parent exit. This detachment is a
// deliberate production feature: a shared dolt sql-server must outlive the
// `bd` process that started it, so other `bd` invocations (and other
// terminals) can reuse it.
//
// When BEADS_TEST_MODE=1, this additionally sets Pdeathsig: SIGTERM so the
// detached server is asked to exit if its parent (a test binary) dies
// before calling Stop() — e.g. when a test run is SIGKILLed or its process
// group is torn down uncleanly. Without this, a detached test server has no
// way to notice its parent is gone and leaks indefinitely, still holding
// its now-deleted temp data directory open (gastownhall/beads mybd-q6cz).
//
// This is deliberately gated to test mode only: production behavior
// (BEADS_TEST_MODE unset) is byte-identical to procAttrDetached before this
// change (Setpgid: true, no Pdeathsig) — the whole point of the detached
// process group is to survive the parent, and Pdeathsig would defeat that
// for real shared servers.
//
// Caveat: Pdeathsig delivers when the specific *thread* that issued the
// clone(2)/fork+exec (i.e., the OS thread running cmd.Start()) exits, not
// necessarily when the whole Go process exits — the Go runtime can retire
// that thread independently of process lifetime. It is therefore a
// best-effort safety net for tests, not a guaranteed kill switch; the
// suite-exit sweep (see sweep_linux.go) is the backstop for cases where
// Pdeathsig does not fire.
func procAttrDetached() *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if os.Getenv("BEADS_TEST_MODE") == "1" {
		attr.Pdeathsig = syscall.SIGTERM
	}
	return attr
}
