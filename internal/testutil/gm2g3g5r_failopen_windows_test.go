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
// The stub previously returned its error without clearing the ambient port --
// the same fail-open this issue is about, just on the platform where it is
// guaranteed to trigger rather than merely likely to.
//
// Coverage, stated exactly: this gate is COMPILE-CHECKED, not CI-EXECUTED.
// The repo does run windows-latest jobs, but every one of them is a named
// selector on another package (.github/workflows/pr.yml runs
// -run '^TestIsServerProbablyRunning' ./cmd/bd, ./internal/doltversion/,
// -run '^TestWorktreeRemove' ./cmd/bd, and
// ./internal/storage/dbproxy/server/), main.yml's Windows job is build/smoke
// with no `go test`, and the Bazel lane builds host-platform Linux, which
// filters this file out by build tag. No lane names ./internal/testutil/, so a
// revert of the stub's neutralizeAmbientDoltPort() call would go unnoticed by
// every gate; `GOOS=windows go vet ./internal/testutil/` is what actually
// holds this file honest today.
//
// Executing it is newly feasible -- this branch is what made the package's
// Windows test binary compile at all (testdoltserver_env_test.go is now
// //go:build !windows) and the gates here are deterministic and Docker-free --
// but adding that CI step would also newly execute the four pre-existing test
// functions in this package that compile on Windows and have never run there
// (suite_temp_test.go, testdoltbranch_test.go, tmpfs_test.go), so it is
// deliberately left to a follow-up rather than smuggled into this fix.
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
