//go:build cgo

package main

import (
	"os"
	"testing"
)

// TestEmbeddedCloseClaimNextJournalsClaimedRecord: GH#6146 — `bd close
// --claim-next` journals its own close record for the closed issue and then
// exactly one claimed record for the ready issue it returned.
func TestEmbeddedCloseClaimNextJournalsClaimedRecord(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt close tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cn")
	done := bdCreate(t, bd, dir, "Issue to close")
	next := bdCreate(t, bd, dir, "Ready successor")

	claimAuditRun(t, bd, dir, "close", done.ID, "--claim-next")

	entries := claimAuditEntries(t, beadsDir)
	assertClaimAuditFieldChanges(t, beadsDir, done.ID, []claimAuditFieldChange{
		{Field: "status", OldValue: "open", NewValue: "closed"},
	})
	assertClaimAuditClaimed(t, beadsDir, next.ID, "in_progress")
	// The close record precedes the claim it reported: the sidecar is
	// append-only, so file order is the order the command emitted them.
	closeIdx, claimedIdx := -1, -1
	for i, e := range entries {
		if e.Kind == "field_change" && e.IssueID == done.ID {
			closeIdx = i
		}
		if e.Kind == "claimed" && e.IssueID == next.ID {
			claimedIdx = i
		}
	}
	if closeIdx == -1 || claimedIdx == -1 || closeIdx > claimedIdx {
		t.Errorf("close record for %s at %d, claimed record for %s at %d, want close first", done.ID, closeIdx, next.ID, claimedIdx)
	}
}

// TestEmbeddedCloseWithoutClaimNextWritesOnlyCloseRecord: a close that claims
// nothing journals only its own close record — no claimed entries at all.
func TestEmbeddedCloseWithoutClaimNextWritesOnlyCloseRecord(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt close tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "co")
	done := bdCreate(t, bd, dir, "Issue to close")
	other := bdCreate(t, bd, dir, "Ready issue left alone")

	claimAuditRun(t, bd, dir, "close", done.ID)

	assertClaimAuditFieldChanges(t, beadsDir, done.ID, []claimAuditFieldChange{
		{Field: "status", OldValue: "open", NewValue: "closed"},
	})
	for _, e := range claimAuditEntries(t, beadsDir) {
		if e.Kind == "claimed" {
			t.Errorf("plain bd close appended a claimed record %+v, want none", e)
		}
		if e.IssueID == other.ID {
			t.Errorf("plain bd close appended a record for untouched issue %s: %+v", other.ID, e)
		}
	}
}
