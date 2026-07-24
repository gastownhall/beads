//go:build !windows && !linux

package doltserver

import "syscall"

// procAttrDetached returns SysProcAttr to detach a child process from the
// parent process group so it survives parent exit. This detachment is a
// deliberate production feature: a shared dolt sql-server must outlive the
// `bd` process that started it.
//
// syscall.SysProcAttr.Pdeathsig — used on Linux (see procattr_linux.go) to
// test-gate parent-death cleanup — does not exist on darwin/BSD, so this
// platform has no test-mode variant of procAttrDetached: behavior is the
// same in tests and production.
func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
