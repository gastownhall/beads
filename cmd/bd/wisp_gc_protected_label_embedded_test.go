//go:build cgo

package main

import (
	"os"
	"testing"
)

// The embedded end-to-end tests for the wisp GC protected-label guard
// (bd-961adp): a wisp carrying a label the workspace named in
// wisp.protected_labels is never reclaimed by `bd mol wisp gc`, on any route
// or mode, while unlabeled peers stay reclaimable. Every test asserts both
// directions, and every test runs against a throwaway bdInit store — this
// command deletes, and deletes without a digest.

// TestEmbeddedWispGCProtectedLabel is the headline regression for bd-961adp:
// `bd mol wisp gc --age` reclaimed a stale open wisp carrying the protected
// label, destroying unread mail and open escalations that an orchestration
// layer stores as write-once ephemeral beads. With --all the sweep also takes
// closed wisps: closing a record does not make it disposable.
func TestEmbeddedWispGCProtectedLabel(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wpl")

	// Workspace policy: a wisp carrying keep:mail is never reclaimed by GC.
	// Deliberate deletion stays available (`bd delete`); only the sweep is
	// fenced.
	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`)

	// An unread escalation: plain open status for exactly as long as nobody
	// has acted on it — the shape no pre-existing protection could save.
	openKept := bdCreate(t, bd, dir, "unread escalation", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", openKept, "keep:mail")
	openPlain := bdCreate(t, bd, dir, "stale noise", "--ephemeral").ID

	// The same pair, closed: --all reaches closed wisps, and the label must
	// protect them there too.
	closedKept := bdCreate(t, bd, dir, "kept record, closed", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", closedKept, "keep:mail")
	bdClose(t, bd, dir, closedKept)
	closedPlain := bdCreate(t, bd, dir, "closed noise", "--ephemeral").ID
	bdClose(t, bd, dir, closedPlain)

	// --age 0s makes every wisp old enough to reclaim; protection is checked
	// before age, so only the unlabeled pair should go.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s", "--all")

	if bdShowSucceeds(t, bd, dir, openPlain) {
		t.Errorf("unlabeled open wisp %s should have been reclaimed", openPlain)
	}
	if bdShowSucceeds(t, bd, dir, closedPlain) {
		t.Errorf("unlabeled closed wisp %s should have been reclaimed by --all", closedPlain)
	}
	if !bdShowSucceeds(t, bd, dir, openKept) {
		t.Errorf("open wisp %s carrying protected label keep:mail was reclaimed by age GC", openKept)
	}
	if !bdShowSucceeds(t, bd, dir, closedKept) {
		t.Errorf("closed wisp %s carrying protected label keep:mail was reclaimed by --all", closedKept)
	}
}

// TestEmbeddedWispGCCascadeSparesLabeledDependent carries the guard onto the
// cascade expansion, which reaches wisps the parent scan never saw: two old
// closed dependents of an abandoned parent, only one labeled. Closed wisps
// are invisible to the parent scan without --all, so only the cascade can
// collect either — the unlabeled one goes with its parent, the labeled one
// survives, orphaned.
func TestEmbeddedWispGCCascadeSparesLabeledDependent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "wpc")

	bdCommand(t, bd, dir, "config", "set", "wisp.protected_labels", `["keep:mail"]`)

	parent := bdCreate(t, bd, dir, "abandoned parent", "--ephemeral").ID
	keptChild := bdCreate(t, bd, dir, "kept dependent", "--ephemeral").ID
	bdCommand(t, bd, dir, "label", "add", keptChild, "keep:mail")
	bdClose(t, bd, dir, keptChild)
	plainChild := bdCreate(t, bd, dir, "gone dependent", "--ephemeral").ID
	bdClose(t, bd, dir, plainChild)
	bdDepAdd(t, bd, dir, keptChild, parent)
	bdDepAdd(t, bd, dir, plainChild, parent)

	// No --all: the closed dependents are reachable only through the cascade.
	bdCommand(t, bd, dir, "mol", "wisp", "gc", "--age", "0s")

	if bdShowSucceeds(t, bd, dir, parent) {
		t.Errorf("abandoned parent %s should have been reclaimed", parent)
	}
	if bdShowSucceeds(t, bd, dir, plainChild) {
		t.Errorf("unlabeled closed dependent %s should have been cascaded with its parent", plainChild)
	}
	if !bdShowSucceeds(t, bd, dir, keptChild) {
		t.Errorf("labeled closed dependent %s was cascaded; want it spared (orphaned, not deleted)", keptChild)
	}
}
