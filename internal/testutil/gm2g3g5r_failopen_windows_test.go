//go:build windows

package testutil

import (
	"os"
	"testing"
)

// TestEnsureDoltContainerForTestMain_ClearsAmbientPortOnWindows is the
// regression gate for gm-2g3g5r on Windows, where EnsureDoltContainerForTestMain
// is an unconditional failure stub and so needs no probe seam.
//
// Windows is a real lane here (.github/workflows/pr.yml runs windows-latest,
// including in the cross-platform matrix), and the stub previously returned its
// error without clearing the ambient port -- the same fail-open this issue is
// about, just on the platform where it is guaranteed to trigger rather than
// merely likely to.
func TestEnsureDoltContainerForTestMain_ClearsAmbientPortOnWindows(t *testing.T) {
	t.Setenv("BEADS_DOLT_SERVER_PORT", "59999")
	t.Setenv("BEADS_DOLT_PORT", "59999")

	err := EnsureDoltContainerForTestMain()
	if err == nil {
		t.Fatal("EnsureDoltContainerForTestMain() = nil on Windows; want an error")
	}

	for _, name := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT"} {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("FAIL-OPEN: %s still %q after the Windows stub returned %q; "+
				"test-mode stores will resolve to it", name, v, err)
		}
	}
}
