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
		"testcontainers Ryuk reaper is disabled for $HOME=%s (via %s/.testcontainers.properties "+
			"or the TESTCONTAINERS_RYUK_DISABLED env var) — a killed test run will leak its "+
			"containers permanently; set BEADS_ALLOW_UNREAPED_TESTCONTAINERS=1 to proceed anyway",
		home, home,
	)
}

var (
	ryukCheckOnce sync.Once
	ryukCheckErr  error
)

// checkRyukEnabled runs checkRyukDisabled against the live testcontainers
// config once per process and caches the result, so every container-harness
// entry point (NewContainerProvider, startDoltContainer,
// StartIsolatedDoltContainerHandle) reports the same unmissable failure
// instead of only whichever one happens to run first.
func checkRyukEnabled() error {
	ryukCheckOnce.Do(func() {
		allowUnreaped := os.Getenv("BEADS_ALLOW_UNREAPED_TESTCONTAINERS") == "1"
		ryukCheckErr = checkRyukDisabled(resolvedHome(), testcontainers.ReadConfig().RyukDisabled, allowUnreaped)
	})
	return ryukCheckErr
}
