//go:build linux

package doltserver

import "syscall"

// procAttrDetached returns SysProcAttr for the managed dolt sql-server child.
//
// The child is detached into its own process group (Setpgid) so it survives
// parent exit — the production default, where the orchestrator/systemd owns the
// dolt lifecycle by design (CHANGELOG be-0eyj / #3550).
//
// When the BEADS_DOLT_KILL_ON_PARENT_DEATH test/controller knob is set, the
// child additionally gets PR_SET_PDEATHSIG = SIGKILL: the kernel kills the dolt
// server when the parent dies by ANY means, including SIGKILL and a `go test`
// timeout where no userspace teardown runs. This stops real-dolt tests from
// leaking an orphaned sql-server reparented to systemd --user (GH#4505).
// Pdeathsig is a Linux-only field; see doltserver_detach_other.go for the
// non-Linux Unix path.
func procAttrDetached() *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if killOnParentDeath() {
		attr.Pdeathsig = syscall.SIGKILL
	}
	return attr
}
