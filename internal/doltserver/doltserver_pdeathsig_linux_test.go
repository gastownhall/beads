//go:build linux

package doltserver

import (
	"syscall"
	"testing"
)

// TestProcAttrDetachedPdeathsig covers the GH#4505 test/controller knob.
//
// BEADS_DOLT_KILL_ON_PARENT_DEATH gates a kernel-enforced PR_SET_PDEATHSIG on
// the dolt sql-server child so a real-dolt test that is SIGKILLed or hits a
// `go test` timeout does not leak an orphaned server reparented to
// systemd --user. Production detach (Setpgid only) is the default and must be
// preserved when the knob is unset.
func TestProcAttrDetachedPdeathsig(t *testing.T) {
	t.Run("default preserves detach without Pdeathsig", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_KILL_ON_PARENT_DEATH", "")
		attr := procAttrDetached()
		if !attr.Setpgid {
			t.Error("expected Setpgid true (production detach preserved)")
		}
		if attr.Pdeathsig != 0 {
			t.Errorf("expected no Pdeathsig by default, got %v", attr.Pdeathsig)
		}
	})

	t.Run("knob adds Pdeathsig SIGKILL while keeping detach", func(t *testing.T) {
		t.Setenv("BEADS_DOLT_KILL_ON_PARENT_DEATH", "1")
		attr := procAttrDetached()
		if !attr.Setpgid {
			t.Error("expected Setpgid true (detach still set)")
		}
		if attr.Pdeathsig != syscall.SIGKILL {
			t.Errorf("expected Pdeathsig SIGKILL, got %v", attr.Pdeathsig)
		}
	})
}
