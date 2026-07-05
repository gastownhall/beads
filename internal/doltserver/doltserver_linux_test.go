//go:build linux

package doltserver

import (
	"syscall"
	"testing"
)

func TestProcAttrDetachedAddsParentDeathSignalInTestMode(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "1")

	attr := procAttrDetached()
	if !attr.Setpgid {
		t.Fatal("procAttrDetached() did not keep Setpgid=true")
	}
	if attr.Pdeathsig != syscall.SIGTERM {
		t.Fatalf("Pdeathsig = %v, want SIGTERM", attr.Pdeathsig)
	}
}

func TestProcAttrDetachedLeavesProductionServerDetached(t *testing.T) {
	t.Setenv("BEADS_TEST_MODE", "")

	attr := procAttrDetached()
	if !attr.Setpgid {
		t.Fatal("procAttrDetached() did not keep Setpgid=true")
	}
	if attr.Pdeathsig != 0 {
		t.Fatalf("Pdeathsig = %v, want 0 outside test mode", attr.Pdeathsig)
	}
}
