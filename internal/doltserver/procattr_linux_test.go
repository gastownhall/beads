//go:build linux

package doltserver

import (
	"syscall"
	"testing"
)

// TestProcAttrDetached_Pdeathsig verifies procAttrDetached only sets
// Pdeathsig when BEADS_TEST_MODE=1, and never changes Setpgid — the
// production detach behavior must stay byte-identical regardless of test
// mode (gastownhall/beads mybd-q6cz).
func TestProcAttrDetached_Pdeathsig(t *testing.T) {
	t.Run("test mode sets Pdeathsig=SIGTERM", func(t *testing.T) {
		t.Setenv("BEADS_TEST_MODE", "1")
		attr := procAttrDetached()
		if !attr.Setpgid {
			t.Error("Setpgid = false, want true")
		}
		if attr.Pdeathsig != syscall.SIGTERM {
			t.Errorf("Pdeathsig = %v, want SIGTERM", attr.Pdeathsig)
		}
	})

	t.Run("production (unset) has no Pdeathsig", func(t *testing.T) {
		t.Setenv("BEADS_TEST_MODE", "")
		attr := procAttrDetached()
		if !attr.Setpgid {
			t.Error("Setpgid = false, want true")
		}
		if attr.Pdeathsig != 0 {
			t.Errorf("Pdeathsig = %v, want 0 (unset)", attr.Pdeathsig)
		}
	})

	t.Run("BEADS_TEST_MODE=0 has no Pdeathsig", func(t *testing.T) {
		t.Setenv("BEADS_TEST_MODE", "0")
		attr := procAttrDetached()
		if attr.Pdeathsig != 0 {
			t.Errorf("Pdeathsig = %v, want 0 (unset)", attr.Pdeathsig)
		}
	})
}
