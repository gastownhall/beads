package embeddeddolt

import "sync/atomic"

// deferWakeDisabled gates the lazy defer-wake sweep (wakeExpiredDefers) at its
// single entry point. Zero (the process default) means enabled: every ready
// read sweeps, as be-vbhpf restored. Store construction in cmd/bd's factory
// flips it off for a classified read opened under a MIGRATION-FREEZE marker
// (#7634) — the freeze's read-only-during-freeze contract forbids the sweep's
// write transaction, and reads must keep working rather than fail.
//
// The gate is package-level, not per store, for the same reason the Open cache
// shares store instances by data directory (cache.go): a classified-read
// command constructs the store through the same cached constructor intents as
// every other command, so the freeze decision belongs to the one process that
// resolved it, once, in cmd/bd's single commandFreeze snapshot. Tests flip it
// with SetDeferWakeSweepEnabled; flipping it does not affect already-open
// transactions, only sweeps that have not started.
var deferWakeDisabled atomic.Bool

// SetDeferWakeSweepEnabled enables or disables the embedded defer-wake sweep.
// enabled=false makes ready reads skip the sweep silently — the same silence
// strict --readonly reads get, because both are deliberate stand-downs, not
// failures. The zero value is enabled.
func SetDeferWakeSweepEnabled(enabled bool) {
	deferWakeDisabled.Store(!enabled)
}
