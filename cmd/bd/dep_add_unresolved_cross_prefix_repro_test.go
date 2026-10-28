package main

import (
	"errors"
	"testing"
)

// TestDepAddUnresolvableCrossPrefixTargetIsNotSilent reproduces GH#6556
// (bd-7drpwq): a bare target whose prefix merely differs from the source's,
// and that resolves nowhere, used to be accepted silently — written to
// depends_on_external with exit 0 and no signal to the operator, while a
// same-prefix typo was refused. The approved fix keeps the accept (the
// multi-rig "add now, route later" shape is load-bearing, #3134/#6571) and
// ends the silence: resolution now reports the accept as unverified, and the
// dep add routes warn on stderr after the write (warnUnresolvedDepTarget,
// asserted in TestUnresolvedDepTargetWarning and the embedded dep tests).
// A clean non-error accept with no unverified report is the original bug.
func TestDepAddUnresolvableCrossPrefixTargetIsNotSilent(t *testing.T) {
	resolveErr := errors.New(`no issue found matching "crm-934"`)

	toID, unverified, err := resolveUnresolvedDepTarget("fac-q5b", "crm-934", resolveErr)
	if err != nil {
		return // refused: the fail-open is closed
	}
	if unverified {
		return // accepted, and reported to the caller as unverified
	}
	t.Fatalf("dep add accepted unresolvable cross-prefix target %q (toID=%q) with no error and no unverified report",
		"crm-934", toID)
}
