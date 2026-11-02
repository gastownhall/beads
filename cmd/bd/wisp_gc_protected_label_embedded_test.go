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
