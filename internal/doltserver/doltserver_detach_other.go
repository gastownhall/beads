//go:build !linux && !windows

package doltserver

import "syscall"

// procAttrDetached returns SysProcAttr to detach the managed dolt sql-server
// child into its own process group (Setpgid) so it survives parent exit.
//
// On non-Linux Unix (macOS/BSD) syscall.SysProcAttr has no Pdeathsig field, so
// the BEADS_DOLT_KILL_ON_PARENT_DEATH test/controller knob (GH#4505) is a no-op
// here and the child is always detached. The orphan the knob targets is itself
// Linux-specific (reparenting to systemd --user); see doltserver_detach_linux.go.
func procAttrDetached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
