//go:build linux

package doltserver

import (
	"syscall"
	"testing"
)

// TestProcAttrDetached_Pdeathsig verifies procAttrDetached only sets
// Pdeathsig when BEADS_TEST_PDEATHSIG=1, and never changes Setpgid — the
// production detach behavior must stay byte-identical regardless of test
// mode (gastownhall/beads mybd-q6cz). It also verifies the general-purpose
// BEADS_TEST_MODE flag no longer controls Pdeathsig on its own: tying it to
// that broader flag previously SIGTERMed servers started by short-lived `bd`
// subprocesses (e.g. `bd dolt start`) that inherit BEADS_TEST_MODE=1 from an
// exec'ing test but are not themselves the process that should own the
// server's lifetime (gastownhall/beads#4592 review thread, 2026-07-07).
func TestProcAttrDetached_Pdeathsig(t *testing.T) {
	t.Run("BEADS_TEST_PDEATHSIG=1 sets Pdeathsig=SIGTERM", func(t *testing.T) {
		t.Setenv("BEADS_TEST_PDEATHSIG", "1")
		attr := procAttrDetached()
		if !attr.Setpgid {
			t.Error("Setpgid = false, want true")
		}
		if attr.Pdeathsig != syscall.SIGTERM {
			t.Errorf("Pdeathsig = %v, want SIGTERM", attr.Pdeathsig)
		}
	})

	t.Run("production (unset) has no Pdeathsig", func(t *testing.T) {
		t.Setenv("BEADS_TEST_PDEATHSIG", "")
		attr := procAttrDetached()
		if !attr.Setpgid {
			t.Error("Setpgid = false, want true")
		}
		if attr.Pdeathsig != 0 {
			t.Errorf("Pdeathsig = %v, want 0 (unset)", attr.Pdeathsig)
		}
	})

	t.Run("BEADS_TEST_PDEATHSIG=0 has no Pdeathsig", func(t *testing.T) {
		t.Setenv("BEADS_TEST_PDEATHSIG", "0")
		attr := procAttrDetached()
		if attr.Pdeathsig != 0 {
			t.Errorf("Pdeathsig = %v, want 0 (unset)", attr.Pdeathsig)
		}
	})

	t.Run("BEADS_TEST_MODE=1 alone (BEADS_TEST_PDEATHSIG unset) has no Pdeathsig", func(t *testing.T) {
		t.Setenv("BEADS_TEST_MODE", "1")
		t.Setenv("BEADS_TEST_PDEATHSIG", "")
		attr := procAttrDetached()
		if attr.Pdeathsig != 0 {
			t.Errorf("Pdeathsig = %v, want 0 (unset) — BEADS_TEST_MODE alone must not gate Pdeathsig", attr.Pdeathsig)
		}
	})
}
