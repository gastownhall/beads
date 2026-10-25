//go:build !unix && !windows

package doltutil

// sweepsOtherRunsProbes is false here: with no liveness check implemented,
// the sweep removes only this process's own leftover.
const sweepsOtherRunsProbes = false

// probeProcessAlive has no liveness check implemented for this platform, so
// every other pid is treated as alive: the sweep then removes only this
// process's own leftover, never another run's, which errs on the side of
// leaving a remote in place.
func probeProcessAlive(_ int) bool { return true }
