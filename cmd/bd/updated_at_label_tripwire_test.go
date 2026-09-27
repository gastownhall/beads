//go:build cgo

package main

import (
	"os"
	"testing"
	"time"
)

// Tripwire for upstream issue #5442 (spec §3.6 T21).
//
// CURRENT documented behavior, verified empirically on bd 1.3.1-rc.1: a
// label-ONLY mutation (bd update --add-label) exits 0 and grows the label
// set WITHOUT bumping updated_at — label writes bypass the generation. Two
// consecutive --add-label calls leave the stamp untouched both times, which
// means a `--if-updated-at` fence taken before the label writes still
// matches afterwards: the stamp guard is (for now) blind to label history.
// The consequence half of this test pins that blind spot so it is a
// DECISION on record, not an accident.
//
// UPSTREAM PR #5793 WILL FLIP THIS TEST: once label mutations start bumping
// updated_at (the #5442 fix), the two unchanged-stamp assertions below fail
// and the fenced write with the pre-label stamp refuses with 13 — flip the
// two equality expectations and the final bdUpdate to the refusal form, and
// cite #5442/#5793 in the same commit. Until then this test is a regression
// fence AROUND the documented behavior: the #5793 rebase must not silently
// change either half.
func TestUpdateLabelMutationDoesNotBumpUpdatedAt(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "utw")

	issue := bdCreate(t, bd, dir, "Label stamp tripwire", "--type", "task")
	stamp0 := bdShow(t, bd, dir, issue.ID).UpdatedAt.UTC().Format(time.RFC3339)

	// First consecutive label-only write: exit 0, stamp unchanged (#5442).
	bdUpdate(t, bd, dir, issue.ID, "--add-label", "tripwire-a")
	row := bdShow(t, bd, dir, issue.ID)
	if got := row.UpdatedAt.UTC().Format(time.RFC3339); got != stamp0 {
		t.Errorf("first --add-label bumped updated_at: %s -> %s (#5442 behavior changed — see PR #5793 flip note)", stamp0, got)
	}
	if len(row.Labels) != 1 || row.Labels[0] != "tripwire-a" {
		t.Errorf("labels = %v, want [tripwire-a]; the write itself must still land", row.Labels)
	}

	// Second consecutive label-only write: still unchanged.
	bdUpdate(t, bd, dir, issue.ID, "--add-label", "tripwire-b")
	row = bdShow(t, bd, dir, issue.ID)
	if got := row.UpdatedAt.UTC().Format(time.RFC3339); got != stamp0 {
		t.Errorf("second --add-label bumped updated_at: %s -> %s (#5442 behavior changed — see PR #5793 flip note)", stamp0, got)
	}
	if len(row.Labels) != 2 {
		t.Errorf("labels = %v, want both tripwire labels", row.Labels)
	}

	// Consequence: a fence read BEFORE the label writes still authorizes a
	// field write now — the stamp guard cannot see label-only history. When
	// PR #5793 lands, this call starts exiting 13 and the assertion flips.
	waitPastStampBoundary(t)
	bdUpdate(t, bd, dir, issue.ID, "--priority", "1", "--if-updated-at", stamp0)
	if got := bdShow(t, bd, dir, issue.ID).Priority; got != 1 {
		t.Errorf("priority = %d, want 1; the pre-label fence should still match while #5442 stands", got)
	}
}
