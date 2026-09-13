//go:build !windows

package testutil

import (
	"fmt"
	"os"
	"sync"

	"github.com/testcontainers/testcontainers-go"
)

// resolvedHome returns the $HOME testcontainers resolves its properties file
// against, so a disabled-Ryuk error can name the exact path to check. Kept
// separate from testcontainers' own config resolution (cached process-wide
// behind an unexported sync.Once) so it stays independently testable.
func resolvedHome() string {
	return os.Getenv("HOME")
}

// checkRyukDisabled reports an error when the Ryuk reaper is disabled and no
// explicit opt-out is set. disabled is the effective
// testcontainers.ReadConfig().RyukDisabled value; allowUnreaped is the
// BEADS_ALLOW_UNREAPED_TESTCONTAINERS opt-out. See be-ovg86: a disabled
// reaper leaks every container from a killed test run permanently, and a
// warning alone went unnoticed for four months (2026-04-25 to 2026-09-03) —
// this fails outright instead.
func checkRyukDisabled(home string, disabled, allowUnreaped bool) error {
	if !disabled || allowUnreaped {
		return nil
	}
	return fmt.Errorf(
		"testcontainers Ryuk reaper is disabled (via %s/.testcontainers.properties "+
			"or the TESTCONTAINERS_RYUK_DISABLED env var) — a killed test run will leak its "+
			"containers permanently; set BEADS_ALLOW_UNREAPED_TESTCONTAINERS=1 to proceed anyway",
		home,
	)
}

// ryukGuardExitCode is the process exit status checkRyukEnabled uses. Distinct
// from go test's own failure status (1) so a Ryuk-disabled box is
// distinguishable from an ordinary test failure in CI logs.
const ryukGuardExitCode = 2

var ryukCheckOnce sync.Once

// checkRyukEnabled runs checkRyukDisabled against the live testcontainers
// config once per process, for every container-harness entry point
// (NewContainerProvider, startDoltContainer, StartIsolatedDoltContainerHandle).
//
// It does not return an error: on a Ryuk-disabled box it prints the reason to
// stderr and terminates the process with ryukGuardExitCode, which go test
// reports as a package-level FAIL.
//
// Exiting rather than returning is the whole point, and is deliberate. An
// error here is swallowed by 13 of the 18 call sites that reach a
// container-start path — 11 TestMains downgrade it to
// "WARN: ..., skipping Dolt tests" and carry on green, and the two
// NewContainerProvider callers t.Skipf on any error — which reproduces
// exactly the "tests ran normally, nothing to see" mode this guard exists to
// close (be-ovg86). Callers therefore get no say in the matter. This is
// test-harness-only code; nothing in a shipped binary reaches it.
//
// Every call site runs this after checkDolt(), so a box with no container
// runtime still skips cleanly instead of being killed over a reaper it was
// never going to use. TestRyukDisabled_SwallowingCallerStillDies pins the
// behavior end-to-end through the swallowing caller shape.
func checkRyukEnabled() {
	ryukCheckOnce.Do(func() {
		allowUnreaped := os.Getenv("BEADS_ALLOW_UNREAPED_TESTCONTAINERS") == "1"
		err := checkRyukDisabled(resolvedHome(), testcontainers.ReadConfig().RyukDisabled, allowUnreaped)
		if err == nil {
			return
		}
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(ryukGuardExitCode)
	})
}
